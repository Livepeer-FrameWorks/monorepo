package relay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"frameworks/api_sidecar/internal/admission"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func serveProcessingInput(t *testing.T, hash string, upstream http.HandlerFunc, method string, readerTimeout time.Duration) int {
	t.Helper()
	up := httptest.NewServer(upstream)
	t.Cleanup(up.Close)
	resolver := &fakeResolver{out: map[string]*ResolveResult{"upload/" + hash: {
		State:             ipcpb.AssetState_ASSET_STATE_PLAYABLE,
		MediaPresignedURL: up.URL + "/" + hash,
		ExpectedSizeBytes: 4096,
		URLTTLSeconds:     60,
	}}}
	ts := mount(t, newTestServer(t, t.TempDir(), admission.CacheToDisk, resolver, nil))
	t.Cleanup(ts.Close)
	TakeProcessingInputFailure(hash)
	t.Cleanup(func() { TakeProcessingInputFailure(hash) })

	req, err := http.NewRequestWithContext(context.Background(), method, ts.URL+"/internal/artifact/upload/"+hash+".mp4", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("User-Agent", "MistServer/test")
	resp, err := (&http.Client{Timeout: readerTimeout}).Do(req)
	if err != nil {
		// The reader gave up; the relay notes the failure once its upstream
		// request observes the hang-up.
		time.Sleep(100 * time.Millisecond)
		return 0
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// The relay keeps, per processing input, the upstream failure that kept Mist
// from reading it: a storage server error is answered as retry-later and
// noted, a missing object stays a 404 and is not a storage stall.
func TestProcessingInputUpstreamFailuresAreNotedAndAnswered(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		status := serveProcessingInput(t, "busyinput", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}, method, 5*time.Second)
		if status != http.StatusServiceUnavailable {
			t.Fatalf("%s: storage 503 answered as %d, want 503", method, status)
		}
		if _, ok := TakeProcessingInputFailure("busyinput"); !ok {
			t.Fatalf("%s: a storage 503 must be noted for the processing job", method)
		}

		status = serveProcessingInput(t, "goneinput", func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		}, method, 5*time.Second)
		if status != http.StatusNotFound {
			t.Fatalf("%s: missing object answered as %d, want 404", method, status)
		}
		if reason, ok := TakeProcessingInputFailure("goneinput"); ok {
			t.Fatalf("%s: a missing object is not a storage stall, noted %q", method, reason)
		}

		if status := serveProcessingInput(t, "stalledinput", func(_ http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
		}, method, 300*time.Millisecond); status != 0 {
			t.Fatalf("%s: a stalled upstream answered %d", method, status)
		}
		if reason, ok := TakeProcessingInputFailure("stalledinput"); !ok || reason != errUpstreamNoAnswer.Error() {
			t.Fatalf("%s: stalled upstream noted %q (%v), want %q", method, reason, ok, errUpstreamNoAnswer)
		}
	}
}
