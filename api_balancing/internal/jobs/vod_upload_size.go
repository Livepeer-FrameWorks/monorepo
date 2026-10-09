package jobs

import (
	"context"
	"database/sql"
	"fmt"

	"frameworks/api_balancing/internal/artifactoutbox"
	"frameworks/api_balancing/internal/database/foghorndb"
	publicv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/public/v1"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/proto"
)

// VodObjectHeader reads a stored object's existence and size. *storage.S3Client satisfies it.
type VodObjectHeader interface {
	HeadObjectInfo(ctx context.Context, key string) (exists bool, size int64, etag string, err error)
}

// VodStoredObjectCheck is what a HEAD of a completed upload's object found.
type VodStoredObjectCheck struct {
	Present     bool
	StoredBytes int64
	// SizeMismatch is set when the stored object's size differs from the size
	// the upload declared. The multipart part ETags only prove each part arrived
	// as sent; the declared size is the one independent statement of what the
	// client meant to upload.
	SizeMismatch bool
}

// CheckCompletedVodObject HEADs a completed upload's object and compares its
// size with declaredBytes. A declared size of zero or less is not compared.
func CheckCompletedVodObject(ctx context.Context, header VodObjectHeader, key string, declaredBytes int64) (VodStoredObjectCheck, error) {
	exists, size, _, err := header.HeadObjectInfo(ctx, key)
	if err != nil {
		return VodStoredObjectCheck{}, err
	}
	if !exists {
		return VodStoredObjectCheck{}, nil
	}
	return VodStoredObjectCheck{
		Present:      true,
		StoredBytes:  size,
		SizeMismatch: declaredBytes > 0 && size != declaredBytes,
	}, nil
}

// VodSizeMismatchMessage is the failure reason recorded on an upload whose stored
// object size differs from its declared size.
func VodSizeMismatchMessage(storedBytes, declaredBytes int64) string {
	return fmt.Sprintf("upload size mismatch: stored object is %d bytes, declared %d bytes", storedBytes, declaredBytes)
}

// VodSizeMismatchFailure identifies a 'completing' upload whose stored object
// does not match its declared size.
type VodSizeMismatchFailure struct {
	ArtifactHash  string
	TenantID      string
	UploadID      string
	UserID        string
	StoredBytes   int64
	DeclaredBytes int64
}

// FailVodSizeMismatchTx moves a 'completing' upload to 'failed' with the size
// mismatch as its reason, records the stored size on the artifact, and enqueues
// the FAILED lifecycle event and upload.failed (invalid input) on tx. It reports
// whether the row moved; a row that already left 'completing' emits nothing.
func FailVodSizeMismatchTx(ctx context.Context, tx *sql.Tx, f VodSizeMismatchFailure) (bool, error) {
	errMsg := VodSizeMismatchMessage(f.StoredBytes, f.DeclaredBytes)
	affected, err := foghorndb.New(tx).FailVodCompletionSizeMismatch(ctx, foghorndb.FailVodCompletionSizeMismatchParams{
		ErrorMessage:    sql.NullString{String: errMsg, Valid: true},
		StoredSizeBytes: f.StoredBytes,
		ArtifactHash:    f.ArtifactHash,
		TenantID:        f.TenantID,
	})
	if err != nil || affected == 0 {
		return false, err
	}
	data := &ipcpb.VodLifecycleData{
		Status:    ipcpb.VodLifecycleData_STATUS_FAILED,
		VodHash:   f.ArtifactHash,
		TenantId:  proto.String(f.TenantID),
		Error:     proto.String(errMsg),
		SizeBytes: proto.Uint64(uint64(max(f.StoredBytes, 0))),
	}
	if f.UploadID != "" {
		data.UploadId = proto.String(f.UploadID)
	}
	if f.UserID != "" {
		data.UserId = proto.String(f.UserID)
	}
	return true, artifactoutbox.EnqueueVodTransitionTx(ctx, tx, data, &publicv1.UploadFailed{
		Artifact: artifactoutbox.UploadArtifact(f.ArtifactHash),
		Reason:   publicv1.MediaFailureReason_MEDIA_FAILURE_REASON_INVALID_INPUT,
	})
}
