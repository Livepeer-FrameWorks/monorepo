package storage

import (
	"strings"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	"github.com/prometheus/client_golang/prometheus"
)

// S3Backing identifies an S3 (or S3-compatible) bucket. Equality on the full
// tuple is required for "this Foghorn can mint locally for that backing" — bucket
// name alone collides across providers (MinIO, R2, Bunny Storage, etc.) where
// the same bucket name lives behind different endpoints.
type S3Backing struct {
	Bucket   string
	Endpoint string // empty == AWS default endpoint
	Region   string
}

// Normalize lowercases endpoint/region and trims surrounding whitespace so two
// equivalent backings compare equal.
func (b S3Backing) Normalize() S3Backing {
	return S3Backing{
		Bucket:   strings.TrimSpace(b.Bucket),
		Endpoint: strings.ToLower(strings.TrimSpace(b.Endpoint)),
		Region:   strings.ToLower(strings.TrimSpace(b.Region)),
	}
}

// Equal reports whether two backings are the same after normalization.
func (b S3Backing) Equal(other S3Backing) bool {
	a := b.Normalize()
	o := other.Normalize()
	return a.Bucket == o.Bucket && a.Endpoint == o.Endpoint && a.Region == o.Region
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

	if r.Metrics != nil {
		r.Metrics.WithLabelValues("service_unavailable", "storage").Inc()
	}
	if r.Logger != nil {
		r.Logger.WithFields(logging.Fields{
			"origin":        in.OriginClusterID,
			"official":      in.OfficialClusterID,
			"local_cluster": localClusterID,
		}).Warn("storage resolver: no candidate cluster has usable backing")
	}
	return "", StorageUnavailable
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
