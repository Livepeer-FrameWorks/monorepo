package relay

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	dto "github.com/prometheus/client_model/go"
	"github.com/sirupsen/logrus"
	logrustest "github.com/sirupsen/logrus/hooks/test"

	"frameworks/api_sidecar/internal/admission"
)

func firstBodyByteSamples(t *testing.T, source string) (uint64, float64) {
	t.Helper()
	var m dto.Metric
	if err := defrostFirstBodyByte.WithLabelValues(source).(interface{ Write(*dto.Metric) error }).Write(&m); err != nil {
		t.Fatal(err)
	}
	return m.GetHistogram().GetSampleCount(), m.GetHistogram().GetSampleSum()
}

// The upstream answers headers at once and holds the body back. Response
// headers alone (defrost_ttfb_seconds) report that block as fast; the
// first-body-byte histogram and the slow-body warning must see the gap.
func TestColdBlockFetchRecordsLateFirstBodyByte(t *testing.T) {
	prev := slowFirstBodyByteAfter
	slowFirstBodyByteAfter = 100 * time.Millisecond
	t.Cleanup(func() { slowFirstBodyByteAfter = prev })

	const bodyDelay = 300 * time.Millisecond
	body := bytes.Repeat([]byte{'x'}, 4096)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", len(body)-1, len(body)))
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusPartialContent)
		w.(http.Flusher).Flush()
		time.Sleep(bodyDelay)
		_, _ = w.Write(body)
	}))
	defer up.Close()

	s := relayForUpstream(t, up, "late", int64(len(body)), admission.CacheMemoryOnly, int64(len(body)))
	logger, hook := logrustest.NewNullLogger()
	s.logger = logger
	ts := mount(t, s)
	defer ts.Close()

	countBefore, sumBefore := firstBodyByteSamples(t, "s3")
	resp, err := doGet(t, ts.URL+"/internal/artifact/vod/late.mkv")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || len(got) != len(body) {
		t.Fatalf("relay body = %d bytes (err %v), want %d", len(got), err, len(body))
	}

	countAfter, sumAfter := firstBodyByteSamples(t, "s3")
	if countAfter != countBefore+1 {
		t.Fatalf("first-body-byte samples = %d, want %d", countAfter, countBefore+1)
	}
	if observed := sumAfter - sumBefore; observed < bodyDelay.Seconds() {
		t.Fatalf("first-body-byte observation = %.3fs, want >= %.3fs", observed, bodyDelay.Seconds())
	}

	upHost := mustURLHost(t, up.URL)
	var warned bool
	for _, e := range hook.AllEntries() {
		if e.Level != logrus.WarnLevel || !strings.Contains(e.Message, "first body byte") {
			continue
		}
		warned = true
		if e.Data["upstream_host"] != upHost {
			t.Errorf("upstream_host = %v, want %s", e.Data["upstream_host"], upHost)
		}
		if e.Data["range"] != fmt.Sprintf("bytes=0-%d", len(body)-1) {
			t.Errorf("range = %v", e.Data["range"])
		}
		if ms, _ := e.Data["elapsed_ms"].(int64); ms < bodyDelay.Milliseconds() {
			t.Errorf("elapsed_ms = %v, want >= %d", e.Data["elapsed_ms"], bodyDelay.Milliseconds())
		}
	}
	if !warned {
		t.Fatal("no warning for a first body byte later than the threshold")
	}
}

// An upstream that sends block headers and then drops the connection fails
// the span copy, not the preflight; the error names the phase that failed.
func TestColdSpanCopyErrorNamesSpanPhase(t *testing.T) {
	const size = 4096
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", size-1, size))
		w.Header().Set("Content-Length", strconv.Itoa(size))
		w.WriteHeader(http.StatusPartialContent)
		w.(http.Flusher).Flush()
		conn, _, err := http.NewResponseController(w).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}))
	defer up.Close()

	s := relayForUpstream(t, up, "cut", size, admission.CacheMemoryOnly, size)
	logger, hook := logrustest.NewNullLogger()
	s.logger = logger
	gin.SetMode(gin.TestMode)
	ts := mount(t, s)
	defer ts.Close()

	resp, err := doGet(t, ts.URL+"/internal/artifact/vod/cut.mkv")
	if err != nil {
		t.Fatal(err)
	}
	msg, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500 (body %q)", resp.StatusCode, msg)
	}
	if !strings.HasPrefix(string(msg), "upstream block copy:") {
		t.Fatalf("error body = %q, want the upstream block copy phase", msg)
	}
	for _, e := range hook.AllEntries() {
		if e.Message == "relay server error" && e.Data["op"] != "upstream block copy" {
			t.Fatalf("op = %v, want upstream block copy", e.Data["op"])
		}
	}
}

func mustURLHost(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Host
}
