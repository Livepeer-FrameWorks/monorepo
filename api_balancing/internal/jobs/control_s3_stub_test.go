package jobs

import (
	"context"
	"time"

	"frameworks/api_balancing/internal/control"
)

// testCellBackendID is the fingerprint of the test cell's S3 store wired into control (see controlS3Stub). Tests that
// assert or match a recorded/adopted backend compare against it.
var testCellBackendID = control.BackendFingerprint("s3", "cell-bucket", "https://cell.s3", "eu-central", "prod")

// controlS3Stub is a control.S3ClientInterface whose only meaningful method is BackendDescriptor — enough for the
// freeze/thumbnail/staging attribution paths (ClaimFreezeAttempt, EnqueueStagingCleanupTx, RecordStreamCleanupObligation)
// to attribute this cell's backend. The rest panic: jobs tests never route bytes through control's S3 client (the
// artifacts.Cleaner owns S3 deletion).
type controlS3Stub struct{}

func (controlS3Stub) BackendDescriptor() (string, string, string, string) {
	return "cell-bucket", "https://cell.s3", "eu-central", "prod"
}
func (controlS3Stub) GeneratePresignedPUT(string, time.Duration) (string, error) { panic("unused") }
func (controlS3Stub) GeneratePresignedGET(string, time.Duration) (string, error) { panic("unused") }
func (controlS3Stub) PutObject(context.Context, string, []byte, string) error    { panic("unused") }
func (controlS3Stub) ListPrefix(context.Context, string) ([]string, error)       { panic("unused") }
func (controlS3Stub) Delete(context.Context, string) error                       { panic("unused") }
func (controlS3Stub) DeleteByURL(context.Context, string) error                  { panic("unused") }
func (controlS3Stub) DeletePrefix(context.Context, string) (int, error)          { panic("unused") }
func (controlS3Stub) ParseS3URL(string) (string, error)                          { panic("unused") }
func (controlS3Stub) ParseLocalS3URL(string) (string, error)                     { panic("unused") }
func (controlS3Stub) BuildClipS3Key(string, string, string, string) string       { panic("unused") }
func (controlS3Stub) BuildDVRS3Key(string, string, string) string                { panic("unused") }
func (controlS3Stub) BuildVodS3Key(string, string, string) string                { panic("unused") }
func (controlS3Stub) BuildS3URL(string) string                                   { panic("unused") }
func (controlS3Stub) Exists(context.Context, string) (bool, error)               { panic("unused") }
func (controlS3Stub) GetObjectSize(context.Context, string) (int64, error)       { panic("unused") }
func (controlS3Stub) HeadObjectInfo(context.Context, string) (bool, int64, string, error) {
	panic("unused")
}
func (controlS3Stub) PromoteObject(context.Context, string, string, string) error { panic("unused") }
