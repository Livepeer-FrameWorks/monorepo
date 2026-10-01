package control

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"frameworks/api_balancing/internal/storage"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// s3StatusError is an object-store failure carrying an HTTP status, as the SDK returns it.
func s3StatusError(status int, code string) error {
	return &awshttp.ResponseError{ResponseError: &smithyhttp.ResponseError{
		Response: &smithyhttp.Response{Response: &http.Response{StatusCode: status}},
		Err:      errors.New(code),
	}}
}

func shortenThumbnailObjectStore(t *testing.T, attemptTimeout time.Duration) {
	t.Helper()
	prev := thumbnailObjectStore
	thumbnailObjectStore = thumbnailObjectStorePolicy{
		headPolicy: storage.S3AttemptPolicy{MaxAttempts: 3, AttemptTimeout: attemptTimeout},
		copyPolicy: storage.S3AttemptPolicy{MaxAttempts: 3, AttemptTimeout: attemptTimeout},
	}
	t.Cleanup(func() { thumbnailObjectStore = prev })
}

// Every HEAD and CopyObject of the deterministic projection carries its own deadline, well under the 2-minute
// completion deadline, even when the caller passes an unbounded context.
func TestCopyThumbnailObjectsToDeterministic_EveryRequestHasAnAttemptDeadline(t *testing.T) {
	var mu sync.Mutex
	var unbounded []string
	check := func(ctx context.Context, op string) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 30*time.Second {
			mu.Lock()
			unbounded = append(unbounded, op)
			mu.Unlock()
		}
	}
	m := &mockS3Client{
		headObjectInfoFn: func(ctx context.Context, key string) (bool, int64, string, error) {
			check(ctx, "HEAD "+key)
			return true, 10, "etag", nil
		},
		promoteObjectFn: func(ctx context.Context, src, dst, _ string) error {
			check(ctx, "COPY "+dst)
			return nil
		},
	}
	objs := thumbnailObjectsFromToken("stream-1", "tok", []string{"poster.jpg", "sprite.jpg"})
	if !copyThumbnailObjectsToDeterministic(context.Background(), m, "stream-1", objs, logging.NewLoggerWithService("test")) {
		t.Fatal("expected every file copied")
	}
	if len(unbounded) != 0 {
		t.Fatalf("object-store requests without a per-attempt deadline: %v", unbounded)
	}
}

// A CopyObject the store never answers is abandoned at its attempt timeout and retried, so the projection lands
// instead of spending the caller's whole deadline on one stalled request.
func TestCopyThumbnailObjectsToDeterministic_RetriesAStalledCopy(t *testing.T) {
	shortenThumbnailObjectStore(t, 50*time.Millisecond)
	var mu sync.Mutex
	stalls := 0
	m := &mockS3Client{
		promoteObjectFn: func(ctx context.Context, _, _, _ string) error {
			mu.Lock()
			first := stalls == 0
			stalls++
			mu.Unlock()
			if first {
				<-ctx.Done()
				return ctx.Err()
			}
			return nil
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := time.Now()
	objs := thumbnailObjectsFromToken("stream-1", "tok", []string{"poster.jpg"})
	if !copyThumbnailObjectsToDeterministic(ctx, m, "stream-1", objs, logging.NewLoggerWithService("test")) {
		t.Fatal("the retried copy must land")
	}
	if len(m.promoteCalls) != 2 {
		t.Fatalf("want the stalled copy retried once, got %v", m.promoteCalls)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatalf("stalled copy held the projection for %s", time.Since(started))
	}
}

// With the source ETag already known, the projection copies without a HEAD; a copy refused on that ETag (412)
// re-reads the source and copies under the fresh ETag.
func TestCopyThumbnailObjectsToDeterministic_KnownETagSkipsTheHead(t *testing.T) {
	logger := logging.NewLoggerWithService("test")
	objs := thumbnailObjectsFromToken("stream-1", "tok", []string{"poster.jpg", "sprite.jpg"})
	for i := range objs {
		objs[i].ETag = "etag-version"
	}

	var used []string
	m := &mockS3Client{promoteObjectFn: func(_ context.Context, _, _, ifMatch string) error {
		used = append(used, ifMatch)
		return nil
	}}
	if !copyThumbnailObjectsToDeterministic(context.Background(), m, "stream-1", objs, logger) {
		t.Fatal("expected every file copied")
	}
	if len(m.headCalls) != 0 || len(used) != 2 || used[0] != "etag-version" || used[1] != "etag-version" {
		t.Fatalf("known ETag must be used with no HEAD: heads=%v ifMatch=%v", m.headCalls, used)
	}

	refused := s3StatusError(http.StatusPreconditionFailed, "PreconditionFailed")
	used = nil
	m = &mockS3Client{promoteObjectFn: func(_ context.Context, _, _, ifMatch string) error {
		used = append(used, ifMatch)
		if ifMatch == "etag-version" {
			return refused
		}
		return nil
	}}
	if !copyThumbnailObjectsToDeterministic(context.Background(), m, "stream-1", objs[:1], logger) {
		t.Fatal("a copy refused on a stale ETag must fall back to the HEAD and land")
	}
	if len(m.headCalls) != 1 || len(used) != 2 || used[1] != "etag-mock" {
		t.Fatalf("want one fallback HEAD then a copy under its ETag: heads=%v ifMatch=%v", m.headCalls, used)
	}
}
