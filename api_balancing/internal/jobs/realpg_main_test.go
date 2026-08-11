//go:build schema_verify

package jobs

import (
	"os"
	"testing"

	"frameworks/api_balancing/internal/control"
)

// TestMain wires control's package-global S3 client so ClaimThumbnailAttempt / RecordStreamCleanupObligation (called by
// the drainer real-PG tests) attribute this cell's backend rather than failing closed — matching production, where a
// thumbnail-minting cell always has a local store. (controlS3Stub / testCellBackendID live in control_s3_stub_test.go
// so the untagged freeze/staging tests can reuse them.)
func TestMain(m *testing.M) {
	control.SetS3Client(controlS3Stub{})
	os.Exit(m.Run())
}
