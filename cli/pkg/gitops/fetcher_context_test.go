package gitops

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

type cancelProbeTransport struct {
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (p *cancelProbeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if p.calls.Add(1) == 1 {
		close(p.started)
	}
	select {
	case <-request.Context().Done():
		return nil, request.Context().Err()
	case <-p.release:
		return nil, errors.New("released cancellation fixture")
	}
}

func TestManifestFetchContextCancelsRequestAndRetry(t *testing.T) {
	fetcher, err := NewFetcher(FetchOptions{Repository: "https://manifest.invalid", CacheDir: t.TempDir(), RetryCount: 3, RetryDelay: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	probe := &cancelProbeTransport{started: make(chan struct{}), release: make(chan struct{})}
	fetcher.client = &http.Client{Transport: probe}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := fetcher.FetchContext(ctx, "stable", "v0.3.14"); done <- err }()
	defer close(probe.release)
	<-probe.started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("fetch cancellation cause=%v", err)
		}
		if probe.calls.Load() != 1 {
			t.Errorf("cancelled fetch retried %d times", probe.calls.Load())
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("manifest HTTP request ignored command cancellation")
	}
}
