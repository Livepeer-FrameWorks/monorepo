package control

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/proto"
)

const (
	balancerCapabilityTTL             = 2 * time.Hour
	BalancerCapabilityRefreshInterval = 30 * time.Minute
	balancerCapabilityPathMarker      = "/_frameworks/balancer/v2/"
)

// FoghornBalancerBaseForNode returns a node-bound, short-lived Mist balancer
// URL. The capability authorizes source lookup only; it is not a service token
// and carries no tenant or mutation authority.
func FoghornBalancerBaseForNode(clusterID, nodeID string) string {
	base := foghornBalancerBase(clusterID)
	secret := balancerCapabilitySecret()
	if base == "" || secret == "" || strings.TrimSpace(nodeID) == "" || strings.TrimSpace(clusterID) == "" {
		return ""
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	nodeID = strings.TrimSpace(nodeID)
	expires := time.Now().UTC().Add(balancerCapabilityTTL).Unix()
	signature := signBalancerCapability(secret, nodeID, clusterID, expires)
	pathPrefix := strings.TrimRight(u.Path, "/")
	rawPrefix := strings.TrimRight(u.EscapedPath(), "/")
	clusterID = strings.TrimSpace(clusterID)
	u.Path = pathPrefix + balancerCapabilityPathMarker + nodeID + "/" + clusterID + "/" + strconv.FormatInt(expires, 10) + "/" + signature
	u.RawPath = rawPrefix + balancerCapabilityPathMarker + url.PathEscape(nodeID) + "/" + url.PathEscape(clusterID) + "/" + strconv.FormatInt(expires, 10) + "/" + signature
	return u.String()
}

// VerifyBalancerCapabilityPath validates a public Mist compatibility request,
// returns its authenticated node and cluster, and strips the capability prefix
// back to the compatibility path. The cluster is signed into the URL so a
// Foghorn restart does not invalidate still-live local Mist configuration. The
// capability lives in the path because
// MistInputBalancer replaces all configured query parameters with source and
// fallback before issuing the request.
func VerifyBalancerCapabilityPath(escapedPath string, now time.Time) (nodeID, clusterID, compatibilityPath string, ok bool) {
	markerAt := strings.Index(escapedPath, balancerCapabilityPathMarker)
	if markerAt < 0 {
		return "", "", "", false
	}
	rest := escapedPath[markerAt+len(balancerCapabilityPathMarker):]
	parts := strings.SplitN(rest, "/", 5)
	if len(parts) < 4 {
		return "", "", "", false
	}
	nodeID, err := url.PathUnescape(parts[0])
	if err != nil || strings.TrimSpace(nodeID) == "" {
		return "", "", "", false
	}
	nodeID = strings.TrimSpace(nodeID)
	clusterID, err = url.PathUnescape(parts[1])
	if err != nil || strings.TrimSpace(clusterID) == "" {
		return "", "", "", false
	}
	clusterID = strings.TrimSpace(clusterID)
	expires, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || expires <= now.UTC().Unix() {
		return "", "", "", false
	}
	// Do not accept arbitrarily long attacker-chosen lifetimes even with a
	// copied query string from a future configuration generation.
	if expires > now.UTC().Add(balancerCapabilityTTL+time.Minute).Unix() {
		return "", "", "", false
	}
	want := signBalancerCapability(balancerCapabilitySecret(), nodeID, clusterID, expires)
	if want == "" || !hmac.Equal([]byte(want), []byte(parts[3])) {
		return "", "", "", false
	}
	compatibilityPath = "/"
	if len(parts) == 5 && parts[4] != "" {
		compatibilityPath = "/" + parts[4]
	}
	return nodeID, clusterID, compatibilityPath, true
}

func signBalancerCapability(secret, nodeID, clusterID string, expires int64) string {
	if strings.TrimSpace(secret) == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(strings.Join([]string{
		"foghorn-balancer-v2", strings.TrimSpace(nodeID), strings.TrimSpace(clusterID), strconv.FormatInt(expires, 10),
	}, "\x00")))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func balancerCapabilitySecret() string {
	return strings.TrimSpace(os.Getenv("FOGHORN_BALANCER_CAPABILITY_SECRET"))
}

// RefreshLocalBalancerCapabilities rotates only the Mist source-lookup
// capability for locally connected nodes. TLS/Caddy/baseline configuration is
// deliberately excluded from this frequent refresh path.
func RefreshLocalBalancerCapabilities() int {
	if registry == nil {
		return 0
	}
	type target struct {
		nodeID, clusterID string
	}
	registry.mu.RLock()
	targets := make([]target, 0, len(registry.conns))
	for nodeID, c := range registry.conns {
		if c != nil {
			targets = append(targets, target{nodeID: nodeID, clusterID: c.clusterID})
		}
	}
	registry.mu.RUnlock()

	sent := 0
	for _, target := range targets {
		base := FoghornBalancerBaseForNode(target.clusterID, target.nodeID)
		if base == "" {
			continue
		}
		persistCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		seed, err := prepareAndPersistConfigSeed(persistCtx, target.nodeID, func(latest *ipcpb.ConfigSeed) (*ipcpb.ConfigSeed, error) {
			if latest == nil {
				return nil, sql.ErrNoRows
			}
			candidate := proto.CloneOf(latest)
			candidate.FoghornBalancerBase = base
			return candidate, nil
		})
		if err != nil {
			cancel()
			// The node already holds a durable full seed. Keep rotating the
			// short-lived source capability during a Foghorn DB outage using an
			// in-memory monotonic version; the connected node persists this
			// partial update locally, and its next registration advances the DB
			// floor before any later producer allocates.
			fallbackVersion := seedVersions.next(target.nodeID)
			if SendLocalBalancerCapabilityUpdate(target.nodeID, &ipcpb.BalancerCapabilityUpdate{
				NodeId: target.nodeID, FoghornBalancerBase: base, SeedVersion: fallbackVersion,
			}) == nil {
				sent++
			}
			continue
		}
		cancel()
		if SendLocalBalancerCapabilityUpdate(target.nodeID, &ipcpb.BalancerCapabilityUpdate{
			NodeId: target.nodeID, FoghornBalancerBase: base, SeedVersion: seed.GetSeedVersion(),
		}) == nil {
			sent++
		}
	}
	return sent
}
