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

// ClusterResolver decides where a durable write for an artifact lands and whether this Foghorn can mint URLs for
// it locally or must delegate via federation. Durable storage belongs to the artifact's ORIGIN cluster (the cell that
// produced it); there is no tenant-level storage destination.
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

	// Logger is optional; used for resolution-decision logs.
	Logger logging.Logger

	// Metrics is optional. When set, StorageUnavailable verdicts increment
	// `WithLabelValues("service_unavailable", "storage")`.
	Metrics *prometheus.CounterVec
}

// ResolveOriginDurable resolves the durable-write destination for an artifact produced by originClusterID. The
// destination is always the origin cluster itself; there is no fallback to another cluster:
//
//  1. The origin advertises an S3 backing: StorageMintLocal when this pool serves the origin and its configured S3
//     client addresses the same keyspace, otherwise StorageMintViaFederation (the origin's Foghorn signs).
//  2. The origin advertises no backing: StorageMintLocal when this cell serves the origin (it is this Foghorn's
//     configured cluster or one of the clusters its pool serves) and an S3 client is configured; the cell's
//     storage is then the origin's storage.
//  3. Otherwise (empty origin, or an origin without usable storage) StorageUnavailable with a logged reason.
func (r *ClusterResolver) ResolveOriginDurable(originClusterID string) (clusterID string, mode StorageMintMode) {
	origin := strings.TrimSpace(originClusterID)
	if origin == "" {
		return "", r.unavailable(logging.Fields{"origin": originClusterID, "reason": "origin cluster unknown"})
	}
	if r.AdvertisedBacking != nil {
		if backing, ok := r.AdvertisedBacking(origin); ok && strings.TrimSpace(backing.Bucket) != "" {
			if r.canMintLocally(origin, backing) {
				return origin, StorageMintLocal
			}
			return origin, StorageMintViaFederation
		}
	}
	if r.LocalS3ClientPresent && r.servesCluster(origin) {
		return origin, StorageMintLocal
	}
	return "", r.unavailable(logging.Fields{
		"origin":        origin,
		"local_cluster": strings.TrimSpace(r.LocalClusterID),
		"reason":        "origin cluster has no usable storage backing",
	})
}

// unavailable records the rejected-storage metric + a warn log and returns StorageUnavailable.
func (r *ClusterResolver) unavailable(fields logging.Fields) StorageMintMode {
	if r.Metrics != nil {
		r.Metrics.WithLabelValues("service_unavailable", "storage").Inc()
	}
	if r.Logger != nil {
		r.Logger.WithFields(fields).Warn("storage resolver: no usable origin-durable backing")
	}
	return StorageUnavailable
}

func (r *ClusterResolver) servesCluster(clusterID string) bool {
	if clusterID == strings.TrimSpace(r.LocalClusterID) {
		return true
	}
	return r.LocalClusterServed != nil && r.LocalClusterServed(clusterID)
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
