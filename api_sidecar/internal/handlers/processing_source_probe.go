package handlers

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// processingSourceProbeTimeout bounds the source check inside the blocking
// STREAM_SOURCE trigger. A probe that cannot complete in time is not a verdict:
// Mist opens the source itself.
const processingSourceProbeTimeout = 8 * time.Second

var processingSourceProbeClient = &http.Client{Timeout: processingSourceProbeTimeout}

var (
	processingSourceFailures   = map[string]chan string{}
	processingSourceFailuresMu sync.Mutex
)

// registerProcessingSourceFailureListener lets the processing job on a stream
// learn that its resolved source answered with an HTTP error.
func registerProcessingSourceFailureListener(streamName string) {
	processingSourceFailuresMu.Lock()
	defer processingSourceFailuresMu.Unlock()
	processingSourceFailures[streamName] = make(chan string, 1)
}

func unregisterProcessingSourceFailureListener(streamName string) {
	processingSourceFailuresMu.Lock()
	defer processingSourceFailuresMu.Unlock()
	delete(processingSourceFailures, streamName)
}

func signalProcessingSourceFailure(streamName, failure string) bool {
	processingSourceFailuresMu.Lock()
	ch, ok := processingSourceFailures[streamName]
	processingSourceFailuresMu.Unlock()
	if !ok {
		return false
	}
	select {
	case ch <- failure:
	default:
	}
	return true
}

func takeProcessingSourceFailure(streamName string) (string, bool) {
	processingSourceFailuresMu.Lock()
	ch, ok := processingSourceFailures[streamName]
	processingSourceFailuresMu.Unlock()
	if !ok {
		return "", false
	}
	select {
	case failure := <-ch:
		return failure, true
	default:
		return "", false
	}
}

// probeProcessingSourceStatus fetches the first byte of an http(s) source and
// returns its HTTP status. A ranged GET (not HEAD) keeps presigned GET URLs
// valid. ok is false when the source is not http(s) or the probe could not
// complete; neither is evidence the source is gone.
func probeProcessingSourceStatus(ctx context.Context, sourceURL string) (status int, statusText string, ok bool) {
	lower := strings.ToLower(strings.TrimSpace(sourceURL))
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return 0, "", false
	}
	ctx, cancel := context.WithTimeout(ctx, processingSourceProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSpace(sourceURL), nil)
	if err != nil {
		return 0, "", false
	}
	req.Header.Set("Range", "bytes=0-0")
	resp, err := processingSourceProbeClient.Do(req)
	if err != nil {
		return 0, "", false
	}
	defer resp.Body.Close()
	return resp.StatusCode, resp.Status, true
}

// failProcessingSourceIfUnavailable probes the source Foghorn resolved for a
// processing+ stream with an active job. An HTTP error status fails the job at
// once with the status, instead of letting Mist open an error body and leave
// the job waiting. It reports whether the source was refused.
func failProcessingSourceIfUnavailable(ctx context.Context, streamName, sourceURL string) bool {
	if !strings.HasPrefix(streamName, "processing+") || !HasPendingJob(streamName) {
		return false
	}
	status, statusText, ok := probeProcessingSourceStatus(ctx, sourceURL)
	if !ok || status < http.StatusBadRequest {
		return false
	}
	if statusText == "" {
		statusText = fmt.Sprintf("%d %s", status, http.StatusText(status))
	}
	failure := "source unavailable: " + statusText
	logger.WithFields(logging.Fields{
		"stream_name": streamName,
		"status":      status,
	}).Warn("Processing source probe failed; failing the job")
	signalProcessingSourceFailure(streamName, failure)
	return true
}
