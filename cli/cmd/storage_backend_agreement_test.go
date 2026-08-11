package cmd

import (
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"
)

func agreementManifest(clusterBucket, clusterEndpoint, clusterRegion, clusterPrefix string) *inventory.Manifest {
	return &inventory.Manifest{
		Services: map[string]inventory.ServiceConfig{
			"foghorn-eu": {Deploy: "foghorn", Cluster: "media-eu-1"},
		},
		Clusters: map[string]inventory.ClusterConfig{
			"media-eu-1": {S3Bucket: clusterBucket, S3Endpoint: clusterEndpoint, S3Region: clusterRegion, S3Prefix: &clusterPrefix},
		},
	}
}

// A Foghorn whose env S3 descriptor matches the cluster row passes; any mismatch (bucket/endpoint/region/prefix) is
// refused pre-deploy so a divergent/repointed backend never reaches Foghorn's crash-on-boot guard or a mis-serving
// Chandler. Prefix is part of the immutable tuple Quartermaster owns, so it is compared here too.
func TestValidateStorageBackendAgreement(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		"STORAGE_S3_BUCKET":   "bucket-A",
		"STORAGE_S3_ENDPOINT": "https://a.s3",
		"STORAGE_S3_REGION":   "eu-west-1",
		"STORAGE_S3_PREFIX":   "artifacts",
	}

	// Agreement (full tuple incl prefix) → nil.
	if err := validateStorageBackendAgreement(agreementManifest("bucket-A", "https://a.s3", "eu-west-1", "artifacts"), "foghorn-eu", "foghorn", "", env); err != nil {
		t.Fatalf("matching descriptor must pass, got: %v", err)
	}

	// Bucket mismatch → refused.
	if err := validateStorageBackendAgreement(agreementManifest("bucket-B", "https://a.s3", "eu-west-1", "artifacts"), "foghorn-eu", "foghorn", "", env); err == nil || !strings.Contains(err.Error(), "s3_bucket") {
		t.Fatalf("bucket mismatch must be refused, got: %v", err)
	}
	// Endpoint mismatch → refused.
	if err := validateStorageBackendAgreement(agreementManifest("bucket-A", "https://b.s3", "eu-west-1", "artifacts"), "foghorn-eu", "foghorn", "", env); err == nil || !strings.Contains(err.Error(), "s3_endpoint") {
		t.Fatalf("endpoint mismatch must be refused, got: %v", err)
	}
	// Region mismatch → refused.
	if err := validateStorageBackendAgreement(agreementManifest("bucket-A", "https://a.s3", "us-east-1", "artifacts"), "foghorn-eu", "foghorn", "", env); err == nil || !strings.Contains(err.Error(), "s3_region") {
		t.Fatalf("region mismatch must be refused, got: %v", err)
	}
	// Prefix mismatch → refused (a split keyspace between Foghorn and the cluster row Chandler serves from).
	if err := validateStorageBackendAgreement(agreementManifest("bucket-A", "https://a.s3", "eu-west-1", "other"), "foghorn-eu", "foghorn", "", env); err == nil || !strings.Contains(err.Error(), "s3_prefix") {
		t.Fatalf("prefix mismatch must be refused, got: %v", err)
	}

	// A non-Foghorn deploy is not gated here.
	if err := validateStorageBackendAgreement(agreementManifest("bucket-B", "", "", ""), "chandler-eu", "chandler", "", env); err != nil {
		t.Fatalf("non-foghorn deploy must not be gated, got: %v", err)
	}
	// Genuinely storage-less: env has no bucket AND the cluster declares no s3_bucket → nothing to agree on.
	if err := validateStorageBackendAgreement(agreementManifest("", "", "", ""), "foghorn-eu", "foghorn", "", map[string]string{}); err != nil {
		t.Fatalf("a storage-less Foghorn (no env, no cluster descriptor) must not be gated, got: %v", err)
	}
	// FAIL CLOSED: the cluster declares an s3_bucket but the Foghorn env has none — it would deploy storage-disabled
	// against an S3-declaring cluster and silently fail durable writes. Must be refused.
	if err := validateStorageBackendAgreement(agreementManifest("bucket-A", "https://a.s3", "eu-west-1", "artifacts"), "foghorn-eu", "foghorn", "", map[string]string{}); err == nil || !strings.Contains(err.Error(), "storage backend missing") {
		t.Fatalf("an S3-declaring cluster with no Foghorn STORAGE_S3_BUCKET must be refused, got: %v", err)
	}
	// An S3-enabled Foghorn whose cluster declares NO descriptor is REJECTED — the cluster descriptor is required so
	// Chandler and first-boot adoption have an authoritative backend (the legacy env-only shape is refused).
	if err := validateStorageBackendAgreement(agreementManifest("", "", "", ""), "foghorn-eu", "foghorn", "", env); err == nil || !strings.Contains(err.Error(), "descriptor absent") {
		t.Fatalf("an absent cluster descriptor for an S3-enabled Foghorn must be rejected, got: %v", err)
	}

	// An omitted region (both env and cluster empty → us-east-1) with matching empty prefix must NOT false-mismatch.
	envNoRegion := map[string]string{"STORAGE_S3_BUCKET": "bucket-A", "STORAGE_S3_ENDPOINT": "https://a.s3"}
	if err := validateStorageBackendAgreement(agreementManifest("bucket-A", "https://a.s3", "", ""), "foghorn-eu", "foghorn", "", envNoRegion); err != nil {
		t.Fatalf("an omitted region (defaulting to us-east-1 on both sides) must pass, got: %v", err)
	}
}

// Whitespace is significant: Foghorn's immutable cell identity compares the descriptor byte-for-byte against the live
// Quartermaster row, so a leading/trailing-space difference addresses a different backend and MUST be refused
// pre-deploy — trimming here would let it pass and then crash Foghorn on boot after a partial rollout. Only a
// genuinely UNSET bucket disables S3; a whitespace-only value is present-and-configured to the runtime, so it is
// rejected rather than trimmed-to-disabled.
func TestValidateStorageBackendAgreement_WhitespaceIsSignificant(t *testing.T) {
	t.Parallel()

	// Trailing space on the bucket env vs a clean cluster row → refused (not silently trimmed to agreement).
	trailingBucket := map[string]string{
		"STORAGE_S3_BUCKET":   "bucket-A ",
		"STORAGE_S3_ENDPOINT": "https://a.s3",
		"STORAGE_S3_REGION":   "eu-west-1",
		"STORAGE_S3_PREFIX":   "artifacts",
	}
	if err := validateStorageBackendAgreement(agreementManifest("bucket-A", "https://a.s3", "eu-west-1", "artifacts"), "foghorn-eu", "foghorn", "", trailingBucket); err == nil || !strings.Contains(err.Error(), "s3_bucket") {
		t.Fatalf("a trailing-space bucket difference must be refused (raw compare), got: %v", err)
	}

	// Leading space on the prefix env → refused: a whitespace-different keyspace prefix splits Foghorn from the
	// cluster row Chandler serves from.
	leadingPrefix := map[string]string{
		"STORAGE_S3_BUCKET":   "bucket-A",
		"STORAGE_S3_ENDPOINT": "https://a.s3",
		"STORAGE_S3_REGION":   "eu-west-1",
		"STORAGE_S3_PREFIX":   " artifacts",
	}
	if err := validateStorageBackendAgreement(agreementManifest("bucket-A", "https://a.s3", "eu-west-1", "artifacts"), "foghorn-eu", "foghorn", "", leadingPrefix); err == nil || !strings.Contains(err.Error(), "s3_prefix") {
		t.Fatalf("a leading-space prefix difference must be refused (raw compare), got: %v", err)
	}

	// A whitespace-only bucket is REJECTED, not read as disabled: the runtime treats a blank-but-present env value as
	// configured (only a genuinely UNSET bucket disables S3), so a whitespace-only value would boot Foghorn with a
	// backend whose identity can never match the cluster row — caught here pre-deploy.
	if err := validateStorageBackendAgreement(agreementManifest("bucket-A", "https://a.s3", "eu-west-1", "artifacts"), "foghorn-eu", "foghorn", "", map[string]string{"STORAGE_S3_BUCKET": "   "}); err == nil || !strings.Contains(err.Error(), "whitespace-only") {
		t.Fatalf("a whitespace-only bucket must be refused as misconfigured (not read as disabled), got: %v", err)
	}
	// A genuinely UNSET bucket is S3-disabled ONLY when the cluster also declares no descriptor (a storage-less cell).
	if err := validateStorageBackendAgreement(agreementManifest("", "", "", ""), "foghorn-eu", "foghorn", "", map[string]string{}); err != nil {
		t.Fatalf("an unset bucket against a descriptor-less cluster must read as S3-disabled, got: %v", err)
	}
}
