package placement

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"slices"
	"strings"

	clusterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
)

// CommercialEntitlementDigest binds only permission and ownership facts. Health,
// addresses, labels and a reader's lease-renewal clock are not permission changes.
func CommercialEntitlementDigest(tenantID string, peers []*clusterpb.TenantClusterPeer) (string, error) {
	tenant, err := uuid.Parse(tenantID)
	if err != nil || tenant == uuid.Nil || tenant.String() != tenantID || len(peers) == 0 || len(peers) > 4096 {
		return "", fmt.Errorf("invalid commercial entitlement scope")
	}
	ordered := slices.Clone(peers)
	slices.SortFunc(ordered, func(a, b *clusterpb.TenantClusterPeer) int {
		return strings.Compare(a.GetClusterId(), b.GetClusterId())
	})
	hash := sha256.New()
	_, _ = hash.Write([]byte("frameworks/placement/quote-entitlement/v2\x00" + tenantID + "\x00"))
	for index, peer := range ordered {
		if peer == nil || !validReviewID(peer.GetClusterId()) || len(peer.GetClusterId()) > 100 || index > 0 && peer.GetClusterId() == ordered[index-1].GetClusterId() || !peer.GetAccessActive() || peer.GetSubscriptionStatus() != "active" {
			return "", fmt.Errorf("invalid commercial entitlement membership")
		}
		if err := rejectUnknownWire(peer); err != nil {
			return "", err
		}
		if peer.GetMediaConsent() == nil || peer.GetMediaConsent().GetRevision() > math.MaxInt64 || peer.GetAccessSource() < clusterpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_PLATFORM_TIER || peer.GetAccessSource() > clusterpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_OPERATOR_OVERRIDE {
			return "", fmt.Errorf("invalid commercial entitlement provenance")
		}
		if peer.GetClusterClass() != "platform_official" && peer.GetClusterClass() != "tenant_private" && peer.GetClusterClass() != "third_party_marketplace" {
			return "", fmt.Errorf("invalid commercial entitlement class")
		}
		if owner := peer.GetOwnerTenantId(); owner != "" {
			id, parseErr := uuid.Parse(owner)
			if parseErr != nil || id == uuid.Nil || id.String() != owner {
				return "", fmt.Errorf("invalid commercial owner")
			}
		}
		if peer.GetAccessSource() == clusterpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_OWNER && peer.GetOwnerTenantId() != tenantID || peer.GetAccessSource() == clusterpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_PLATFORM_TIER && peer.GetClusterClass() != "platform_official" {
			return "", fmt.Errorf("commercial provenance contradicts ownership or class")
		}
		if expiry := peer.GetAccessExpiresAt(); expiry != nil && !expiry.IsValid() {
			return "", fmt.Errorf("invalid commercial access expiry")
		}
		bound := &clusterpb.TenantClusterPeer{ClusterId: peer.GetClusterId(), ClusterClass: peer.GetClusterClass(), OwnerTenantId: peer.GetOwnerTenantId(), AccessActive: true, SubscriptionStatus: "active", AccessSource: peer.GetAccessSource(), AccessExpiresAt: proto.CloneOf(peer.GetAccessExpiresAt()), MediaConsent: proto.CloneOf(peer.GetMediaConsent())}
		encoded, encodeErr := proto.MarshalOptions{Deterministic: true}.Marshal(bound)
		if encodeErr != nil {
			return "", encodeErr
		}
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(encoded)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write(encoded)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
