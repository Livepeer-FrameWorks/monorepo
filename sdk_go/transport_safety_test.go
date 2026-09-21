package frameworks

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMutationWithIdempotencyKeyIsNotReplayedByTransport(t *testing.T) {
	var mutations atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "mutation") {
			mutations.Add(1)
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		_, _ = io.WriteString(w, `{"data":{}}`)
	}))
	defer srv.Close()
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	c, err := NewClient(ClientOptions{URL: srv.URL, DisableServerCheck: true, HTTPClient: &http.Client{Transport: transport}, Retry: &RetryPolicy{MaxAttempts: 1}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, warmErr := c.send(ctx, "query", "Warm", "query Warm { __typename }", nil); warmErr != nil {
		t.Fatal(warmErr)
	}
	_, err = c.send(WithIdempotencyKey(ctx, "mutation-key"), "mutation", "Delete", "mutation Delete { deleteStream(id: \"s\") }", nil)
	if err == nil || mutations.Load() != 1 {
		t.Fatalf("connection lost after mutation: attempts=%d error=%v", mutations.Load(), err)
	}
}

func TestSharedProbeWaitHonorsCallerCancellation(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		_, _ = io.WriteString(w, `{"data":{"serverInfo":{"version":"v0.3.11","features":[]}}}`)
	}))
	defer srv.Close()
	defer close(release)
	c, err := NewClient(ClientOptions{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _, _ = c.ServerInfo(context.Background()); close(done) }()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	returned := make(chan error, 1)
	go func() { _, err := c.ServerInfo(ctx); returned <- err }()
	select {
	case err := <-returned:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("second caller remained blocked behind another caller's probe")
	}
}
