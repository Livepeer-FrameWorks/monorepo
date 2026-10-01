package control

import (
	"context"
	"time"

	"frameworks/api_balancing/internal/storage"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// thumbnailObjectStorePolicy bounds every object-store request of thumbnail publication. Thumbnail objects are
// small (a poster, a sprite sheet and a VTT manifest), so a HEAD or CopyObject the store has not answered within
// its attempt timeout is stalled rather than slow; abandoning and retrying it keeps one stalled request from
// spending the whole completion deadline (thumbnailCompletionDeadline) or a recovery item's budget.
type thumbnailObjectStorePolicy struct {
	headPolicy storage.S3AttemptPolicy
	copyPolicy storage.S3AttemptPolicy
}

var thumbnailObjectStore = thumbnailObjectStorePolicy{
	headPolicy: storage.S3AttemptPolicy{MaxAttempts: 3, AttemptTimeout: 5 * time.Second, Backoff: 200 * time.Millisecond},
	copyPolicy: storage.S3AttemptPolicy{MaxAttempts: 3, AttemptTimeout: 10 * time.Second, Backoff: 200 * time.Millisecond},
}

// headThumbnailObject HEADs one thumbnail object under the bounded, retried head policy. A missing object is
// (false, 0, "", nil), as from HeadObjectInfo.
func headThumbnailObject(ctx context.Context, client S3ClientInterface, key string, logger logging.Logger) (exists bool, size int64, etag string, err error) {
	err = storage.RetryS3(ctx, thumbnailObjectStore.headPolicy, func(attemptCtx context.Context) error {
		var hErr error
		exists, size, etag, hErr = client.HeadObjectInfo(attemptCtx, key)
		return hErr
	}, logThumbnailObjectStoreRetry(logger, "HeadObject", key))
	return exists, size, etag, err
}

// promoteThumbnailObject copies srcKey to dstKey conditional on ifMatchETag under the bounded, retried copy policy
// and returns the destination ETag the provider reported ("" when it omitted it).
func promoteThumbnailObject(ctx context.Context, client S3ClientInterface, srcKey, dstKey, ifMatchETag string, logger logging.Logger) (string, error) {
	var dstETag string
	err := storage.RetryS3(ctx, thumbnailObjectStore.copyPolicy, func(attemptCtx context.Context) error {
		var pErr error
		dstETag, pErr = client.PromoteObject(attemptCtx, srcKey, dstKey, ifMatchETag)
		return pErr
	}, logThumbnailObjectStoreRetry(logger, "CopyObject", dstKey))
	return dstETag, err
}

func logThumbnailObjectStoreRetry(logger logging.Logger, op, key string) func(storage.S3AttemptFailure) {
	return func(f storage.S3AttemptFailure) {
		logger.WithFields(logging.Fields{
			"op":           op,
			"object_key":   key,
			"attempt":      f.Attempt,
			"max_attempts": f.MaxAttempts,
			"elapsed_ms":   f.Elapsed.Milliseconds(),
			"error":        f.Err,
		}).Warn("Thumbnail object-store request failed; retrying")
	}
}
