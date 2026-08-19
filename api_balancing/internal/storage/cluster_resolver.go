package storage

import (
	"strings"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	"github.com/prometheus/client_golang/prometheus"
)

// S3Backing identifies the physical S3 (or S3-compatible) KEYSPACE a Foghorn can sign for —
// bucket/endpoint/region/prefix. Equality on this tuple answers "this Foghorn can mint locally for that backing".
// Bucket name alone collides across providers (MinIO, R2, Bunny Storage, etc.) where the same bucket name lives behind
// different endpoints; and two clusters can share one provider tuple (bucket/endpoint/region) but write under DIFFERENT
// prefixes — so prefix is part of the identity. Without it, this Foghorn (configured for one prefix) would classify a
// cluster addressing another prefix as locally mintable and write objects through its OWN prefix, at a key nothing else
// can address.
type S3Backing struct {
	Bucket   string
	Endpoint string // empty == AWS default endpoint
	Region   string
	Prefix   string // S3 keyspace prefix; compared EXACTLY (a different prefix is a different keyspace)
}

// Normalize applies the ONE canonical descriptor normalization shared with the immutable-backend identity
// (control.BackendFingerprint), the first-boot establishment guard, and the CLI deploy gate: bucket/endpoint/prefix are
// compared BYTE-FOR-BYTE (a case/whitespace difference names a different physical keyspace and must NOT collapse), and
// the ONLY transformation is an empty region defaulting to us-east-1. Diverging from that (e.g. lowercasing the
// endpoint) would let this resolver classify a remote descriptor as locally mintable that the backend-identity layer
// treats as a DIFFERENT backend — minting an object whose recorded backend_id cleanup can never match.
func (b S3Backing) Normalize() S3Backing {
	region := b.Region
	if region == "" {
		region = "us-east-1"
	}
	return S3Backing{Bucket: b.Bucket, Endpoint: b.Endpoint, Region: region, Prefix: b.Prefix}
}

// Equal reports whether two backings are the same physical keyspace under the canonical descriptor semantics
// (bucket/endpoint/prefix exact, region empty→us-east-1).
func (b S3Backing) Equal(other S3Backing) bool {
	a := b.Normalize()
	o := other.Normalize()
	return a.Bucket == o.Bucket && a.Endpoint == o.Endpoint && a.Region == o.Region && a.Prefix == o.Prefix
}

// StorageMintMode is the resolver's verdict on how to mint presigned URLs for
// the chosen storage cluster.
type StorageMintMode int

const (
	// StorageUnavailable means no candidate cluster owns usable storage.
	// Callers must reject the operation and emit service_unavailable.
	StorageUnavailable StorageMintMode = iota

	// StorageMintLocal means this Foghorn process can sign URLs against the
	// chosen cluster's S3 directly using its configured S3 client.
	StorageMintLocal

	// StorageMintViaFederation means the chosen cluster owns the storage but
	// this Foghorn cannot sign for it — caller must delegate via federation
	// (MintStorageURLs RPC) to the Foghorn pool that owns it.
	StorageMintViaFederation
)

// String renders a mint mode for log fields.
func (m StorageMintMode) String() string {
	switch m {
	case StorageMintLocal:
		return "local"
	case StorageMintViaFederation:
		return "federation"
	default:
		return "unavailable"
	}
}

// ResolverInput is the cluster context drawn from the stream / artifact / tenant
// row. The resolver applies request-owned candidates first, then its configured
// LocalClusterID. Empty fields are skipped, duplicates are deduped.
type ResolverInput struct {
	// OriginClusterID is the stream's/artifact's INGEST cluster. It is source-authority attribution only and
	// MUST NOT be supplied when selecting a DURABLE-WRITE destination: an advertised BYOC origin would then win
	// the durable write over the tenant's official cluster. Durable callers (freeze, VOD, thumbnail) pass
	// OfficialClusterID only; leave this empty for them. It remains for read/generality paths that legitimately
	// prefer the origin copy.
	OriginClusterID   string
	OfficialClusterID string
}

// ClusterResolver picks the storage cluster that should own a write/read for a
// given stream/artifact and reports whether this Foghorn can mint URLs locally
// or must delegate via federation.
//
// Resolution order, applied to [Origin, Official, LocalClusterID]:
//
//  1. If AdvertisedBacking returns a backing for the candidate AND the candidate
//     is locally served AND LocalS3Backing matches the advertised backing on
//     the full identity tuple → StorageMintLocal.
//  2. If AdvertisedBacking returns a backing but local conditions don't hold →
//     StorageMintViaFederation.
//  3. If the configured LocalClusterID has no advertised backing AND this
//     Foghorn has a configured S3 client → StorageMintLocal.
//  4. Otherwise: try the next candidate.
//
// If no candidate clears the chain, returns ("", StorageUnavailable) and
// increments the rejected counter with reason="service_unavailable".
type ClusterResolver struct {
	// LocalClusterID is this Foghorn process's configured cluster identity.
	LocalClusterID string

	// LocalClusterServed reports whether this Foghorn pool serves the given
	// cluster (typically wraps control.IsServedCluster).
	LocalClusterServed func(clusterID string) bool

	// LocalS3Backing is this Foghorn's configured STORAGE_S3_* values.
	LocalS3Backing S3Backing

	// LocalS3ClientPresent reports whether s3Client != nil for this Foghorn.
	LocalS3ClientPresent bool

	// AdvertisedBacking returns the cluster's S3 backing per Quartermaster
	// metadata. ok=false when the cluster does not advertise any S3 backing.
	AdvertisedBacking func(clusterID string) (S3Backing, bool)

	// Logger is optional; used for resolution-decision debug logs.
	Logger logging.Logger

	// Metrics is optional. When set, StorageUnavailable verdicts increment
	// `WithLabelValues("service_unavailable", "storage")`.
	Metrics *prometheus.CounterVec
}

// Resolve runs the chain. The returned clusterID is empty only when mode is
// StorageUnavailable.
func (r *ClusterResolver) Resolve(in ResolverInput) (clusterID string, mode StorageMintMode) {
	localClusterID := strings.TrimSpace(r.LocalClusterID)
	candidates := []string{
		strings.TrimSpace(in.OriginClusterID),
		strings.TrimSpace(in.OfficialClusterID),
		localClusterID,
	}

	// Pass 1: advertised-backing path across all candidates, deduped.
	seen := map[string]struct{}{}
	for _, id := range candidates {
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}

		if r.AdvertisedBacking == nil {
			continue
		}
		backing, ok := r.AdvertisedBacking(id)
		if !ok || strings.TrimSpace(backing.Bucket) == "" {
			continue
		}
		if r.canMintLocally(id, backing) {
			return id, StorageMintLocal
		}
		return id, StorageMintViaFederation
	}

	if localClusterID != "" && r.LocalS3ClientPresent {
		return localClusterID, StorageMintLocal
	}

	return "", r.unavailable(logging.Fields{
		"origin":        in.OriginClusterID,
		"official":      in.OfficialClusterID,
		"local_cluster": localClusterID,
	})
}

// ResolveOfficialDurable resolves ONLY the tenant's official cluster for a DURABLE write. Unlike Resolve it has
// NO origin candidate and NO generic local fallback: an unresolved/empty official cluster returns
// StorageUnavailable rather than silently minting (and billing) against this cell. The one local outcome it still
// allows is the official cluster BEING this cell — same cluster id AND a configured S3 client — which is a
// positive local resolution of the official destination, not a fall-through to a different/local cluster. This is
// the strict durable-write path (invariant I1); read/generality callers use Resolve.
func (r *ClusterResolver) ResolveOfficialDurable(officialClusterID string) (clusterID string, mode StorageMintMode) {
	official := strings.TrimSpace(officialClusterID)
	if official == "" {
		return "", r.unavailable(logging.Fields{"official": officialClusterID, "reason": "official cluster unresolved"})
	}
	if r.AdvertisedBacking != nil {
		if backing, ok := r.AdvertisedBacking(official); ok && strings.TrimSpace(backing.Bucket) != "" {
			if r.canMintLocally(official, backing) {
				return official, StorageMintLocal
			}
			return official, StorageMintViaFederation
		}
	}
	// No advertised backing for the official cluster: mint locally ONLY when the official cluster IS this cell
	// (id match + S3 client present) — that is the official destination being local, not a fallback.
	if official == strings.TrimSpace(r.LocalClusterID) && r.LocalS3ClientPresent {
		return official, StorageMintLocal
	}
	return "", r.unavailable(logging.Fields{"official": official, "reason": "official cluster has no usable backing"})
}

// unavailable records the rejected-storage metric + a warn log and returns StorageUnavailable.
func (r *ClusterResolver) unavailable(fields logging.Fields) StorageMintMode {
	if r.Metrics != nil {
		r.Metrics.WithLabelValues("service_unavailable", "storage").Inc()
	}
	if r.Logger != nil {
		r.Logger.WithFields(fields).Warn("storage resolver: no usable official-durable backing")
	}
	return StorageUnavailable
}

func (r *ClusterResolver) canMintLocally(clusterID string, backing S3Backing) bool {
	if r.LocalClusterServed == nil || !r.LocalClusterServed(clusterID) {
		return false
	}
	if !r.LocalS3ClientPresent {
		return false
	}
	return r.LocalS3Backing.Equal(backing)
}
