package storage

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

func responseError(status int) error {
	return &awshttp.ResponseError{ResponseError: &smithyhttp.ResponseError{
		Response: &smithyhttp.Response{Response: &http.Response{StatusCode: status}},
		Err:      errors.New("provider response"),
	}}
}

// A request the object store never answers is abandoned at the attempt timeout and retried, so the call succeeds
// on the next attempt instead of waiting out the caller's whole deadline.
func TestRetryS3_AbandonsAStalledAttemptAndRetries(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	calls := 0
	var retried []S3AttemptFailure
	started := time.Now()
	err := RetryS3(ctx, S3AttemptPolicy{MaxAttempts: 3, AttemptTimeout: 50 * time.Millisecond}, func(attemptCtx context.Context) error {
		calls++
		if calls == 1 {
			<-attemptCtx.Done()
			return attemptCtx.Err()
		}
		return nil
	}, func(f S3AttemptFailure) { retried = append(retried, f) })
	if err != nil {
		t.Fatalf("second attempt should succeed: %v", err)
	}
	if calls != 2 || len(retried) != 1 {
		t.Fatalf("want 2 calls and 1 retry, got calls=%d retries=%d", calls, len(retried))
	}
	if !errors.Is(retried[0].Err, ErrS3AttemptTimedOut) || retried[0].Elapsed < 50*time.Millisecond {
		t.Fatalf("retry must report the attempt timeout and its elapsed time: %+v", retried[0])
	}
	if time.Since(started) > 2*time.Second {
		t.Fatalf("stalled attempt was not abandoned at its timeout (took %s)", time.Since(started))
	}
}

// Every attempt timing out ends with an error naming the attempt count and wrapping the timeout.
func TestRetryS3_ReportsAttemptsWhenEveryAttemptStalls(t *testing.T) {
	calls := 0
	err := RetryS3(context.Background(), S3AttemptPolicy{MaxAttempts: 2, AttemptTimeout: 20 * time.Millisecond}, func(attemptCtx context.Context) error {
		calls++
		<-attemptCtx.Done()
		return attemptCtx.Err()
	}, nil)
	if calls != 2 || !errors.Is(err, ErrS3AttemptTimedOut) {
		t.Fatalf("want 2 timed-out attempts, got calls=%d err=%v", calls, err)
	}
	if got := err.Error(); !strings.Contains(got, "after 2 attempt(s)") {
		t.Fatalf("error must name the attempt count: %q", got)
	}
}

// A definitive provider answer (412 precondition failed, 404) is returned at once; a 503 is retried.
func TestRetryS3_RetriesOnlyTransientFailures(t *testing.T) {
	for _, tc := range []struct {
		status    int
		wantCalls int
	}{
		{http.StatusPreconditionFailed, 1},
		{http.StatusNotFound, 1},
		{http.StatusForbidden, 1},
		{http.StatusServiceUnavailable, 3},
		{http.StatusTooManyRequests, 3},
	} {
		calls := 0
		err := RetryS3(context.Background(), S3AttemptPolicy{MaxAttempts: 3}, func(context.Context) error {
			calls++
			return responseError(tc.status)
		}, nil)
		if err == nil || calls != tc.wantCalls {
			t.Fatalf("status %d: want %d call(s) and an error, got calls=%d err=%v", tc.status, tc.wantCalls, calls, err)
		}
	}
}

// The caller's own cancellation is not retried.
func TestRetryS3_StopsOnCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	err := RetryS3(ctx, S3AttemptPolicy{MaxAttempts: 3, AttemptTimeout: time.Second}, func(context.Context) error {
		calls++
		cancel()
		return context.Canceled
	}, nil)
	if calls != 1 || !errors.Is(err, context.Canceled) {
		t.Fatalf("caller cancellation must end the call: calls=%d err=%v", calls, err)
	}
}

// PromoteObject returns the destination ETag the provider reported in the copy result.
func TestPromoteObject_ReturnsCopyResultETag(t *testing.T) {
	api := &fakeS3API{copyETag: `"dst-etag"`}
	c := newTestClient(t, api, &fakePresigner{})
	etag, err := c.PromoteObject(context.Background(), "a/src", "a/dst", `"src-etag"`)
	if err != nil || etag != `"dst-etag"` {
		t.Fatalf("want copy-result ETag, got %q err=%v", etag, err)
	}
	if api.copyIn == nil || *api.copyIn.CopySourceIfMatch != `"src-etag"` || *api.copyIn.Key != "prod/a/dst" {
		t.Fatalf("copy must be conditional on the source ETag: %+v", api.copyIn)
	}

	api.copyETag = ""
	if etag, err := c.PromoteObject(context.Background(), "a/src", "a/dst", `"src-etag"`); err != nil || etag != "" {
		t.Fatalf("a provider that omits the copy ETag yields \"\", got %q err=%v", etag, err)
	}
}
