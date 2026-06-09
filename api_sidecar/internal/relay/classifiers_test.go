package relay

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"frameworks/api_sidecar/internal/admission"
)

// IntentFromHint maps Foghorn's cache hint to a local admission intent: only
// "upload" is a processing input; everything else is a playback cache fill.
func TestIntentFromHint(t *testing.T) {
	if got := IntentFromHint("upload"); got != admission.IntentProcessingInput {
		t.Fatalf("upload hint => %v, want IntentProcessingInput", got)
	}
	for _, kind := range []string{"vod", "clip", "dvr", "", "unknown"} {
		if got := IntentFromHint(kind); got != admission.IntentPlaybackCache {
			t.Fatalf("%q hint => %v, want IntentPlaybackCache", kind, got)
		}
	}
}

// isUpstreamAuthError fires only on a 401/403 upstreamStatusError — the dead
// peer-grant signature that must drop the resolve-cache entry. Other statuses
// and unrelated errors must not match.
func TestIsUpstreamAuthError(t *testing.T) {
	if !isUpstreamAuthError(upstreamStatusError{StatusCode: http.StatusUnauthorized}) {
		t.Fatal("401 must classify as auth error")
	}
	if !isUpstreamAuthError(upstreamStatusError{StatusCode: http.StatusForbidden}) {
		t.Fatal("403 must classify as auth error")
	}
	// Wrapped still matches via errors.As.
	if !isUpstreamAuthError(fmt.Errorf("fetch: %w", upstreamStatusError{StatusCode: http.StatusForbidden})) {
		t.Fatal("wrapped 403 must classify as auth error")
	}
	if isUpstreamAuthError(upstreamStatusError{StatusCode: http.StatusNotFound}) {
		t.Fatal("404 must not be an auth error")
	}
	if isUpstreamAuthError(errors.New("connection reset")) {
		t.Fatal("unrelated error must not be an auth error")
	}
	if isUpstreamAuthError(nil) {
		t.Fatal("nil must not be an auth error")
	}
}

// isClientGone distinguishes client-disconnect noise (which is logged at debug
// and swallowed) from real upstream failures, which must NOT be suppressed.
func TestIsClientGone(t *testing.T) {
	gone := []error{
		errors.New("write: broken pipe"),
		errors.New("read: connection reset by peer"),
		context.Canceled, // Error() == "context canceled"
	}
	for _, err := range gone {
		if !isClientGone(err) {
			t.Fatalf("%v should be classified as client-gone", err)
		}
	}
	notGone := []error{
		nil,
		errors.New("upstream returned 500"),
		context.DeadlineExceeded, // a real timeout, not a client disconnect
	}
	for _, err := range notGone {
		if isClientGone(err) {
			t.Fatalf("%v must NOT be classified as client-gone", err)
		}
	}
}

// probeTotalSize issues a bytes=0-0 range GET and reads the asset's full size
// from the upstream Content-Range total; a non-206 status surfaces an error.
func TestProbeTotalSize(t *testing.T) {
	body := make([]byte, 1234)
	up := upstreamServer(t, body)
	defer up.Close()

	s := newTestServer(t, t.TempDir(), admission.CacheToDisk, &fakeResolver{}, nil)

	total, err := s.probeTotalSize(context.Background(), up.URL, "")
	if err != nil {
		t.Fatalf("probeTotalSize: %v", err)
	}
	if total != int64(len(body)) {
		t.Fatalf("total = %d, want %d", total, len(body))
	}

	// A bad URL host yields a transport error, not a size.
	if _, err := s.probeTotalSize(context.Background(), "http://127.0.0.1:0/nope", ""); err == nil {
		t.Fatal("expected an error probing an unreachable upstream")
	}
}
