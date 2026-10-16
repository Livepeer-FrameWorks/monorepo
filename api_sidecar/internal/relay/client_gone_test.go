package relay

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/sirupsen/logrus"
	logrustest "github.com/sirupsen/logrus/hooks/test"

	"frameworks/api_sidecar/internal/admission"
)

// Staging v0.3.13-rc10: Mist abandoning a cold read after its 5 s source read
// timeout was logged as "relay server error", answered 500 and counted as
// helmsman_relay_requests_total{status="error"}. A reader's own departure is
// counted as client_gone and logs nothing at error level.

func relayRequestCount(format, status string) float64 {
	return testutil.ToFloat64(relayRequests.WithLabelValues(format, "s3", status))
}

// waitRelayRequest waits for the handler, which outlives the reader that left,
// to record its relay_requests_total sample.
func waitRelayRequest(t *testing.T, format string, goneBefore, errBefore float64) (gone, errs float64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		gone = relayRequestCount(format, statusClientGone) - goneBefore
		errs = relayRequestCount(format, "error") - errBefore
		if gone+errs > 0 || time.Now().After(deadline) {
			return gone, errs
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func assertNoRelayErrorLog(t *testing.T, hook *logrustest.Hook) {
	t.Helper()
	for _, e := range hook.AllEntries() {
		if e.Level <= logrus.ErrorLevel {
			t.Fatalf("reader departure logged at %s: %q %v", e.Level, e.Message, e.Data)
		}
	}
}

// holdUpstream keeps an upstream answer back past the reader's budget. Probes
// and fills outlive their reader, so the hold is bounded for the test to end.
func holdUpstream(r *http.Request) {
	select {
	case <-r.Context().Done():
	case <-time.After(time.Second):
	}
}

// readAndLeave reads url until budget passes, then cancels the request.
func readAndLeave(t *testing.T, url string, budget time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

// A reader that leaves while the relay still waits for the source's size.
func TestReaderLeavingDuringSizeProbeIsClientGone(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		holdUpstream(r)
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(up.Close)
	s := relayForUpstream(t, up, "probegone", 0, admission.CacheToDisk, 64*1024)
	logger, hook := logrustest.NewNullLogger()
	logger.SetLevel(logrus.DebugLevel)
	s.logger = logger
	ts := mount(t, s)
	t.Cleanup(ts.Close)

	goneBefore, errBefore := relayRequestCount("webm", statusClientGone), relayRequestCount("webm", "error")
	readAndLeave(t, ts.URL+"/internal/artifact/vod/probegone.webm", 200*time.Millisecond)
	gone, errs := waitRelayRequest(t, "webm", goneBefore, errBefore)
	if gone != 1 || errs != 0 {
		t.Fatalf("relay_requests_total client_gone=+%v error=+%v, want client_gone=+1 error=+0", gone, errs)
	}
	assertNoRelayErrorLog(t, hook)
}

// A reader that leaves in the middle of a block span, after the response
// headers and some body bytes went out.
func TestReaderLeavingMidSpanIsClientGone(t *testing.T) {
	bounds := testUpstreamBounds
	bounds.readStall = 5 * time.Second
	withUpstreamBounds(t, bounds)
	const size = 64 * 1024
	body := patternBody(size)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start, end := parseTestRange(r, size)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
		w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(body[start : start+4096])
		w.(http.Flusher).Flush()
		holdUpstream(r)
	}))
	t.Cleanup(up.Close)
	s := relayForUpstream(t, up, "spangone", size, admission.CacheMemoryOnly, size)
	logger, hook := logrustest.NewNullLogger()
	logger.SetLevel(logrus.DebugLevel)
	s.logger = logger
	ts := mount(t, s)
	t.Cleanup(ts.Close)

	goneBefore, errBefore := relayRequestCount("webm", statusClientGone), relayRequestCount("webm", "error")
	readAndLeave(t, ts.URL+"/internal/artifact/vod/spangone.webm", 300*time.Millisecond)
	gone, errs := waitRelayRequest(t, "webm", goneBefore, errBefore)
	if gone != 1 || errs != 0 {
		t.Fatalf("relay_requests_total client_gone=+%v error=+%v, want client_gone=+1 error=+0", gone, errs)
	}
	assertNoRelayErrorLog(t, hook)
	for _, e := range hook.AllEntries() {
		if strings.Contains(e.Message, "span serve aborted") {
			t.Fatalf("reader departure logged as an aborted span: %v", e.Data)
		}
	}
}

// An upstream that fails while the reader is still there remains an error.
func TestUpstreamFailureWithReaderPresentIsError(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		conn, _, err := http.NewResponseController(w).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}))
	t.Cleanup(up.Close)
	s := relayForUpstream(t, up, "upfail", 0, admission.CacheToDisk, 64*1024)
	logger, hook := logrustest.NewNullLogger()
	s.logger = logger
	ts := mount(t, s)
	t.Cleanup(ts.Close)

	goneBefore, errBefore := relayRequestCount("webm", statusClientGone), relayRequestCount("webm", "error")
	resp, err := doGet(t, ts.URL+"/internal/artifact/vod/upfail.webm")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode < 500 {
		t.Fatalf("status %d, want a 5xx for a failed upstream", resp.StatusCode)
	}
	gone, errs := waitRelayRequest(t, "webm", goneBefore, errBefore)
	if gone != 0 || errs != 1 {
		t.Fatalf("relay_requests_total client_gone=+%v error=+%v, want error=+1", gone, errs)
	}
	var logged bool
	for _, e := range hook.AllEntries() {
		logged = logged || e.Message == "relay server error"
	}
	if !logged {
		t.Fatal("an upstream failure with the reader present was not logged")
	}
}
