package handlers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"

	"frameworks/api_sidecar/internal/admission"
	"frameworks/api_sidecar/internal/relay"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

type stallTestAdmitter struct{}

func (stallTestAdmitter) Decide(context.Context, string, admission.StorageIntent, uint64) (admission.CacheDecision, error) {
	return admission.CacheToDisk, nil
}

type stallTestResolver struct{ upstream string }

func (r stallTestResolver) Resolve(relay.ResolveContext) (*relay.ResolveResult, error) {
	return &relay.ResolveResult{
		State:             ipcpb.AssetState_ASSET_STATE_PLAYABLE,
		MediaPresignedURL: r.upstream,
		ExpectedSizeBytes: 4096,
		URLTTLSeconds:     60,
	}, nil
}

// readUploadThroughRelay serves hash's processing input through a real relay
// backed by upstream, and reads it the way Mist does: give up after a short
// data timeout.
func readUploadThroughRelay(t *testing.T, upstream http.HandlerFunc, hash string) {
	t.Helper()
	up := httptest.NewServer(upstream)
	t.Cleanup(up.Close)
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	relay.New(relay.Options{
		BasePath: t.TempDir(),
		Admitter: stallTestAdmitter{},
		Resolver: stallTestResolver{upstream: up.URL + "/" + hash},
		Logger:   logging.NewLogger(),
	}).MountRoutes(engine)
	rs := httptest.NewServer(engine)
	t.Cleanup(rs.Close)

	mistReader := &http.Client{Timeout: 300 * time.Millisecond}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, rs.URL+"/internal/artifact/upload/"+hash+".mp4", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("User-Agent", "MistServer/test")
	if resp, err := mistReader.Do(req); err == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	// The relay notices the reader left on its next upstream event.
	time.Sleep(100 * time.Millisecond)
}

// waitForUnbootedProcessingStream runs readiness against a Mist that never
// lists the processing stream, as when its input could not read the source.
func waitForUnbootedProcessingStream(t *testing.T, hash string) error {
	t.Helper()
	prevWindow := processingReadinessWindow
	processingReadinessWindow = time.Second
	t.Cleanup(func() { processingReadinessWindow = prevWindow })
	mistSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"authorize":      map[string]any{"status": "OK"},
			"active_streams": map[string]any{},
		})
	}))
	t.Cleanup(mistSrv.Close)
	h := &ProcessingJobHandler{mistServerURL: mistSrv.URL}
	client := mist.NewClient(logging.NewLogger(), mist.ClientConfig{BaseURL: mistSrv.URL})
	req := &ipcpb.ProcessingJobRequest{ArtifactHash: hash, ProcessesJson: `[]`}
	_, _, err := h.waitForProcessingStreamReady(t.Context(), logrus.NewEntry(logrus.New()), client, req, "processing+"+hash, req.GetProcessesJson(), nil, nil, nil, map[string]int{})
	if err == nil {
		t.Fatal("readiness succeeded for a stream Mist never listed")
	}
	return err
}

// An upload whose storage stalls while Mist opens it does not boot. That
// attempt is retryable within the job's retry budget, and its error names the
// stalled source read, instead of failing the upload for good.
func TestProcessingBootFailureOnStalledSourceIsRetryable(t *testing.T) {
	const hash = "stalledupload01"
	relay.TakeProcessingInputFailure(hash)
	t.Cleanup(func() { relay.TakeProcessingInputFailure(hash) })
	readUploadThroughRelay(t, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}, hash)

	err := waitForUnbootedProcessingStream(t, hash)
	if got := processingReadinessFailureStatus(err); got != processingResultRetryable {
		t.Fatalf("status = %q (%v), want %q", got, err, processingResultRetryable)
	}
	if !strings.Contains(err.Error(), "did not boot") || !strings.Contains(err.Error(), "upstream did not answer") {
		t.Fatalf("error %q must name the boot failure and the stalled source read", err)
	}
}

// A source the store reports missing is not a stall: the boot failure stays
// terminal.
func TestProcessingBootFailureOnMissingSourceIsTerminal(t *testing.T) {
	const hash = "missingupload01"
	relay.TakeProcessingInputFailure(hash)
	t.Cleanup(func() { relay.TakeProcessingInputFailure(hash) })
	readUploadThroughRelay(t, func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}, hash)

	err := waitForUnbootedProcessingStream(t, hash)
	if got := processingReadinessFailureStatus(err); got != "failed" {
		t.Fatalf("status = %q (%v), want failed", got, err)
	}
}
