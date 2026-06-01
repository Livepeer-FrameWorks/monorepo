package control

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
	goredis "github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Peer-relay authorization uses online capability grants instead of
// self-signed tokens. At resolve time Foghorn mints a short-lived grant
// (random id + the artifact/paths it authorizes) and hands the id back; the
// serving edge holds no signing key and asks its Foghorn to authorize each
// inbound pull (AuthorizeRelayPull). This keeps Foghorn the sole authority and
// removes all relay key material from edges. Access ends when the grant's
// short TTL expires; there is no explicit revoke call — the TTL is the bound.
//
// The store is Redis-backed when configured (so any HA instance can validate
// a grant another instance minted) and falls back to an in-memory map for
// single-instance/dev deployments.

const relayGrantTTL = 5 * time.Minute

type relayGrant struct {
	ArtifactHash string   `json:"artifact_hash"`
	OriginNodeID string   `json:"origin_node_id"`
	AllowedPaths []string `json:"allowed_paths"`
}

type relayGrantStore struct {
	mu    sync.Mutex
	mem   map[string]relayGrantMemEntry
	redis goredis.UniversalClient
}

type relayGrantMemEntry struct {
	grant relayGrant
	exp   time.Time
}

var relayGrants = &relayGrantStore{mem: make(map[string]relayGrantMemEntry)}

// SetRelayGrantRedis wires the shared Redis client so grants survive across HA
// Foghorn instances. Called from main.go when Redis is configured; nil keeps
// the in-memory fallback.
func SetRelayGrantRedis(client goredis.UniversalClient) {
	relayGrants.mu.Lock()
	relayGrants.redis = client
	relayGrants.mu.Unlock()
}

func relayGrantKey(id string) string {
	return "foghorn:" + GetLocalClusterID() + ":relaygrant:" + id
}

// MintRelayGrant stores a grant and returns its opaque id. originNodeID is the
// node the grant authorizes reads from; allowedPaths are the exact request
// paths (media + its .dtsh sidecar) the holder may pull. Called by the local
// resolver and by the federation server (cross-cluster PrepareArtifact); both
// run on the cluster whose Foghorn will later authorize the pull.
func MintRelayGrant(artifactHash, originNodeID string, allowedPaths []string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(b[:])
	g := relayGrant{ArtifactHash: artifactHash, OriginNodeID: originNodeID, AllowedPaths: allowedPaths}

	relayGrants.mu.Lock()
	client := relayGrants.redis
	relayGrants.mu.Unlock()

	if client != nil {
		payload, err := json.Marshal(g)
		if err != nil {
			return "", err
		}
		if err := client.Set(context.Background(), relayGrantKey(id), payload, relayGrantTTL).Err(); err != nil {
			return "", err
		}
		return id, nil
	}

	relayGrants.mu.Lock()
	relayGrants.mem[id] = relayGrantMemEntry{grant: g, exp: time.Now().Add(relayGrantTTL)}
	relayGrants.mu.Unlock()
	return id, nil
}

// lookupRelayGrant returns the grant for an id, or ok=false when absent or
// expired.
func lookupRelayGrant(id string) (relayGrant, bool) {
	relayGrants.mu.Lock()
	client := relayGrants.redis
	relayGrants.mu.Unlock()

	if client != nil {
		val, err := client.Get(context.Background(), relayGrantKey(id)).Result()
		if err != nil {
			return relayGrant{}, false
		}
		var g relayGrant
		if json.Unmarshal([]byte(val), &g) != nil {
			return relayGrant{}, false
		}
		return g, true
	}

	relayGrants.mu.Lock()
	defer relayGrants.mu.Unlock()
	e, ok := relayGrants.mem[id]
	if !ok {
		return relayGrant{}, false
	}
	if time.Now().After(e.exp) {
		delete(relayGrants.mem, id)
		return relayGrant{}, false
	}
	return e.grant, true
}

// sweepRelayGrants drops expired in-memory entries (no-op when Redis-backed —
// Redis expires keys itself). Cheap; runs on a coarse interval.
func sweepRelayGrants() {
	relayGrants.mu.Lock()
	defer relayGrants.mu.Unlock()
	now := time.Now()
	for id, e := range relayGrants.mem {
		if now.After(e.exp) {
			delete(relayGrants.mem, id)
		}
	}
}

// StartRelayGrantSweeper starts the in-memory expiry sweep. Safe to call once
// at startup regardless of Redis configuration.
func StartRelayGrantSweeper(ctx context.Context) {
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				sweepRelayGrants()
			}
		}
	}()
}

// relayGrantAllows is the authorization decision: the grant must exist, be for
// the serving node that asked (a grant minted for node A must not authorize a
// pull on node B), be for the requested artifact, and cover the exact request
// path (media or its .dtsh). Returns the allow verdict and a diagnostic reason.
func relayGrantAllows(g relayGrant, found bool, servingNodeID, artifactHash, requestPath string) (bool, string) {
	switch {
	case !found:
		return false, "unknown or expired grant"
	case g.OriginNodeID != servingNodeID:
		return false, "node mismatch"
	case g.ArtifactHash != artifactHash:
		return false, "artifact mismatch"
	case !slices.Contains(g.AllowedPaths, requestPath):
		return false, "path not authorized"
	default:
		return true, ""
	}
}

// processAuthorizeRelayPullRequest answers a serving edge's authorization
// query for an inbound peer-relay pull. Foghorn is authoritative: it matches
// the presented grant against the serving node, artifact, and exact request
// path it minted. Unknown/expired grant, node/hash mismatch, or path outside
// the grant → deny. servingNodeID is the node id of the control connection
// this request arrived on — never client-supplied.
func processAuthorizeRelayPullRequest(req *pb.AuthorizeRelayPullRequest, servingNodeID string, stream pb.HelmsmanControl_ConnectServer, logger logging.Logger) {
	resp := &pb.AuthorizeRelayPullResponse{RequestId: req.GetRequestId()}

	g, ok := lookupRelayGrant(strings.TrimSpace(req.GetGrantId()))
	resp.Allowed, resp.Reason = relayGrantAllows(g, ok, servingNodeID, req.GetArtifactHash(), req.GetRequestPath())

	if !resp.Allowed && logger != nil {
		logger.WithField("reason", resp.Reason).WithField("artifact_hash", req.GetArtifactHash()).Debug("AuthorizeRelayPull denied")
	}

	msg := &pb.ControlMessage{
		RequestId: req.GetRequestId(),
		SentAt:    timestamppb.Now(),
		Payload:   &pb.ControlMessage_AuthorizeRelayPullResponse{AuthorizeRelayPullResponse: resp},
	}
	if err := stream.Send(msg); err != nil && logger != nil {
		logger.WithError(err).Warn("Failed to send AuthorizeRelayPullResponse")
	}
}
