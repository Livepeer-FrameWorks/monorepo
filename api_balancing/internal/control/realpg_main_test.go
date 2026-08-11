//go:build schema_verify

package control

import (
	"os"
	"testing"
)

// testCellBackendID is the fingerprint of the default real-PG test cell's S3 store (see TestMain). Tests that assert a
// recorded/adopted backend compare against it.
var testCellBackendID = BackendFingerprint("s3", "cell-bucket", "https://cell.s3", "eu-central", "prod")

// TestMain gives the real-PG control suite a default LOCAL S3 store, matching production: a thumbnail-minting cell
// always has one, so ClaimThumbnailAttempt and RecordStreamCleanupObligation attribute a backend at write time rather
// than failing closed. Individual tests still override s3Client (with their own cleanup) when they need a specific or
// absent descriptor.
func TestMain(m *testing.M) {
	s3Client = &mockS3Client{descBucket: "cell-bucket", descEndpoint: "https://cell.s3", descRegion: "eu-central", descPrefix: "prod"}
	os.Exit(m.Run())
}
