package control

import (
	"context"
	"crypto/sha256"
	"slices"
	"sync"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// playbackGrantBuildTimeout bounds one grant fetch: a local authority read.
const playbackGrantBuildTimeout = 3 * time.Second

// PlaybackGrantBuilder composes the current grant for one Mist stream on one
// node from this cell's signed authority.
type PlaybackGrantBuilder func(ctx context.Context, nodeID, internalName string) (*ipcpb.PlaybackGrant, error)

var (
	playbackGrantBuilderMu sync.RWMutex
	playbackGrantBuilder   PlaybackGrantBuilder
)

// SetPlaybackGrantBuilder installs the builder that answers an edge's grant
// fetch.
func SetPlaybackGrantBuilder(builder PlaybackGrantBuilder) {
	playbackGrantBuilderMu.Lock()
	playbackGrantBuilder = builder
	playbackGrantBuilderMu.Unlock()
}

// playbackGrantsSent records, per node connected to this instance, the last
// grant this instance delivered for each stream (keyed by bare internal name). It makes
// the admission push once per stream per connection and names the edges an
// authority change must reach. It is connection state: a node's entries go
// when its control stream does, and the node fetches its grants again on the
// next registration.
var playbackGrantsSent = struct {
	mu     sync.Mutex
	byNode map[string]map[string]*ipcpb.PlaybackGrant
}{byNode: map[string]map[string]*ipcpb.PlaybackGrant{}}

// OfferPlaybackGrant delivers grant to nodeID over this instance's control
// stream to it, unless that connection already holds the same grant. It never
// relays: each replica pushes to the edges it holds, and learns of authority
// applied elsewhere from the replicas' announcements. Requested names
// delivered earlier for the stream stay in the grant.
func OfferPlaybackGrant(nodeID string, grant *ipcpb.PlaybackGrant) error {
	if nodeID == "" || grant == nil || grant.GetInternalName() == "" {
		return nil
	}
	key := mist.ExtractInternalName(grant.GetInternalName())
	playbackGrantsSent.mu.Lock()
	merged := mergePlaybackGrantNames(playbackGrantsSent.byNode[nodeID][key], grant)
	unchanged := playbackGrantDigest(playbackGrantsSent.byNode[nodeID][key]) == playbackGrantDigest(merged)
	playbackGrantsSent.mu.Unlock()
	if unchanged {
		return nil
	}
	if err := SendLocalPlaybackGrant(nodeID, merged); err != nil {
		return err
	}
	recordPlaybackGrantSent(nodeID, merged)
	return nil
}

// PlaybackGrantHolders returns the nodes this instance delivered a grant for
// the stream to.
func PlaybackGrantHolders(internalName string) []string {
	key := mist.ExtractInternalName(internalName)
	playbackGrantsSent.mu.Lock()
	defer playbackGrantsSent.mu.Unlock()
	var nodes []string
	for nodeID, streams := range playbackGrantsSent.byNode {
		if _, ok := streams[key]; ok {
			nodes = append(nodes, nodeID)
		}
	}
	slices.Sort(nodes)
	return nodes
}

// PlaybackGrantStreams returns the Mist names of streams this instance
// delivered grants for: those with the bare internal name, or all of the
// tenant's when internalName is empty.
func PlaybackGrantStreams(internalName, tenantID string) []string {
	key := mist.ExtractInternalName(internalName)
	playbackGrantsSent.mu.Lock()
	defer playbackGrantsSent.mu.Unlock()
	seen := map[string]bool{}
	var streams []string
	for _, byStream := range playbackGrantsSent.byNode {
		for bare, grant := range byStream {
			if (internalName != "" && bare != key) || (internalName == "" && grant.GetTenantId() != tenantID) {
				continue
			}
			if !seen[grant.GetInternalName()] {
				seen[grant.GetInternalName()] = true
				streams = append(streams, grant.GetInternalName())
			}
		}
	}
	slices.Sort(streams)
	return streams
}

func recordPlaybackGrantSent(nodeID string, grant *ipcpb.PlaybackGrant) {
	key := mist.ExtractInternalName(grant.GetInternalName())
	playbackGrantsSent.mu.Lock()
	defer playbackGrantsSent.mu.Unlock()
	if grant.GetRevoked() {
		delete(playbackGrantsSent.byNode[nodeID], key)
		return
	}
	if playbackGrantsSent.byNode[nodeID] == nil {
		playbackGrantsSent.byNode[nodeID] = map[string]*ipcpb.PlaybackGrant{}
	}
	playbackGrantsSent.byNode[nodeID][key] = grant
}

func forgetPlaybackGrantsSent(nodeID string) {
	playbackGrantsSent.mu.Lock()
	delete(playbackGrantsSent.byNode, nodeID)
	playbackGrantsSent.mu.Unlock()
}

func mergePlaybackGrantNames(previous, grant *ipcpb.PlaybackGrant) *ipcpb.PlaybackGrant {
	merged := proto.CloneOf(grant)
	if previous == nil || grant.GetRevoked() {
		return merged
	}
	for _, name := range previous.GetRequestedNames() {
		if !slices.Contains(merged.GetRequestedNames(), name) {
			merged.RequestedNames = append(merged.RequestedNames, name)
		}
	}
	slices.Sort(merged.RequestedNames)
	return merged
}

func playbackGrantDigest(grant *ipcpb.PlaybackGrant) [32]byte {
	if grant == nil {
		return [32]byte{}
	}
	encoded, _ := proto.MarshalOptions{Deterministic: true}.Marshal(grant) //nolint:errcheck // an unmarshalable grant digests as empty and is resent
	return sha256.Sum256(encoded)
}

// SendLocalPlaybackGrant sends a grant over this instance's control stream to
// the node, fenced to the current connection like every node command.
func SendLocalPlaybackGrant(nodeID string, grant *ipcpb.PlaybackGrant) error {
	if registry == nil {
		return ErrNotConnected
	}
	registry.mu.RLock()
	c := registry.conns[nodeID]
	registry.mu.RUnlock()
	if c == nil {
		return ErrNotConnected
	}
	c.sendGate.Lock()
	defer c.sendGate.Unlock()
	if c.superseded.Load() || !connIsCurrentlyRegistered(nodeID, c) {
		return ErrConnSuperseded
	}
	return c.stream.Send(&ipcpb.ControlMessage{
		Payload: &ipcpb.ControlMessage_PlaybackGrant{PlaybackGrant: grant},
		SentAt:  timestamppb.Now(),
	})
}

// processPlaybackGrantRequest answers an edge's grant fetch. nodeID is the
// authenticated identity of the connection the request arrived on.
func processPlaybackGrantRequest(req *ipcpb.PlaybackGrantRequest, nodeID string, stream ipcpb.HelmsmanControl_ConnectServer, logger logging.Logger) {
	resp := &ipcpb.PlaybackGrantResponse{RequestId: req.GetRequestId()}
	playbackGrantBuilderMu.RLock()
	build := playbackGrantBuilder
	playbackGrantBuilderMu.RUnlock()
	switch {
	case build == nil:
		resp.Error = "playback grants are not issued by this instance"
	case req.GetInternalName() == "":
		resp.Error = "internal_name is required"
	default:
		ctx, cancel := context.WithTimeout(context.Background(), playbackGrantBuildTimeout)
		grant, err := build(ctx, nodeID, req.GetInternalName())
		cancel()
		if err != nil {
			resp.Error = err.Error()
		} else {
			playbackGrantsSent.mu.Lock()
			resp.Grant = mergePlaybackGrantNames(playbackGrantsSent.byNode[nodeID][mist.ExtractInternalName(grant.GetInternalName())], grant)
			playbackGrantsSent.mu.Unlock()
		}
	}
	if resp.Error != "" && logger != nil {
		logger.WithFields(logging.Fields{
			"node_id": nodeID, "internal_name": req.GetInternalName(), "reason": resp.Error,
		}).Info("No playback grant issued to edge")
	}
	msg := &ipcpb.ControlMessage{
		RequestId: req.GetRequestId(),
		SentAt:    timestamppb.Now(),
		Payload:   &ipcpb.ControlMessage_PlaybackGrantResponse{PlaybackGrantResponse: resp},
	}
	if err := stream.Send(msg); err != nil {
		if logger != nil {
			logger.WithError(err).Warn("Failed to send PlaybackGrantResponse")
		}
		return
	}
	if resp.Grant != nil {
		recordPlaybackGrantSent(nodeID, resp.Grant)
	}
}
