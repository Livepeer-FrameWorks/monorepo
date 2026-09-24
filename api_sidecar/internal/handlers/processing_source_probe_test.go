package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/sirupsen/logrus"
)

func claimProbeJob(t *testing.T, stream string) {
	t.Helper()
	oldLogger := logger
	logger = logging.NewLogger()
	t.Cleanup(func() { logger = oldLogger })
	if _, _, claimed := claimPendingJob(stream, "job-probe"); !claimed {
		t.Fatalf("could not claim %s", stream)
	}
	t.Cleanup(func() { releasePendingJob(stream, "job-probe") })
	registerProcessingSourceFailureListener(stream)
	t.Cleanup(func() { unregisterProcessingSourceFailureListener(stream) })
}

// A URL import whose source answers 404 fails its job at STREAM_SOURCE time
// with the HTTP status, and the readiness wait returns that failure at once
// instead of letting Mist open an error body ("MP4 input only supports seekable
// data sources") and leaving the job PROCESSING.
func TestProcessingSourceProbeFailsJobOnHTTPError(t *testing.T) {
	var sawRange string
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawRange = r.Header.Get("Range")
		http.NotFound(w, r)
	}))
	defer source.Close()
	const stream = "processing+probe404"
	claimProbeJob(t, stream)

	if !failProcessingSourceIfUnavailable(context.Background(), stream, source.URL+"/import.mp4") {
		t.Fatal("a 404 source must be refused")
	}
	if sawRange != "bytes=0-0" {
		t.Fatalf("probe Range = %q, want a one-byte ranged GET", sawRange)
	}

	h := &ProcessingJobHandler{}
	req := &ipcpb.ProcessingJobRequest{ProcessesJson: `[]`}
	start := time.Now()
	_, _, err := h.waitForProcessingStreamReady(context.Background(), logrus.NewEntry(logrus.New()), nil, req, stream, req.GetProcessesJson(), nil, nil, nil, map[string]int{})
	if err == nil || !strings.Contains(err.Error(), "source unavailable: 404") {
		t.Fatalf("readiness err = %v, want the source failure", err)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("readiness took %s to report the source failure", time.Since(start))
	}
}

func TestProcessingSourceProbeAcceptsReachableAndUnprobeableSources(t *testing.T) {
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Range", "bytes 0-0/1000")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte{0})
	}))
	defer source.Close()
	const stream = "processing+probeok"
	claimProbeJob(t, stream)

	if failProcessingSourceIfUnavailable(context.Background(), stream, source.URL+"/upload.mp4") {
		t.Fatal("a reachable source must not be refused")
	}
	if failProcessingSourceIfUnavailable(context.Background(), stream, "/var/lib/frameworks/processing/staged.mkv") {
		t.Fatal("a local path is not probed")
	}
	if failProcessingSourceIfUnavailable(context.Background(), stream, "http://127.0.0.1:1/unreachable.mp4") {
		t.Fatal("an unreachable probe is not a verdict")
	}
	if _, ok := takeProcessingSourceFailure(stream); ok {
		t.Fatal("no failure may be signalled for an acceptable source")
	}
}

func TestProcessingSourceProbeSkipsStreamsWithoutJob(t *testing.T) {
	source := httptest.NewServer(http.NotFoundHandler())
	defer source.Close()
	if failProcessingSourceIfUnavailable(context.Background(), "processing+nojob", source.URL) {
		t.Fatal("a processing stream without an active job is not probed")
	}
	if failProcessingSourceIfUnavailable(context.Background(), "live+abc", source.URL) {
		t.Fatal("live streams are not probed")
	}
}
