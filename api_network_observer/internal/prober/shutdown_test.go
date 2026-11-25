package prober

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	obs "github.com/Livepeer-FrameWorks/monorepo/pkg/networkobservation"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/telemetryspool"
)

type shutdownTransport func(*http.Request) (*http.Response, error)

func (f shutdownTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestRunWaitsForDeliveryShutdown(t *testing.T) {
	queue, err := telemetryspool.Open(telemetryspool.Config{Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var enteredOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	client := &http.Client{Transport: shutdownTransport(func(req *http.Request) (*http.Response, error) {
		body := `{}`
		if req.Method == http.MethodPost {
			var batch obs.Batch
			if err := json.NewDecoder(req.Body).Decode(&batch); err != nil {
				return nil, err
			}
			enteredOnce.Do(func() { close(entered) })
			<-release
			body = fmt.Sprintf(`{"accepted":true,"event_id":%q}`, batch.ID)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	p := Prober{URL: "http://observer.test", ID: "shutdown", Secret: "secret", Queue: queue, Client: client, Concurrency: 1, Timeout: time.Second}
	ctx, cancel := context.WithCancel(t.Context())
	finished := make(chan struct{})
	var runErr error
	go func() {
		defer close(finished)
		runErr = p.Run(ctx, nil)
	}()
	defer func() {
		cancel()
		unblock()
		select {
		case <-finished:
		case <-time.After(2 * time.Second):
			t.Error("prober did not finish after delivery was released")
		}
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("delivery did not start")
	}
	cancel()
	select {
	case <-finished:
		t.Error("Run returned while its delivery worker was still running")
	case <-time.After(50 * time.Millisecond):
	}
	unblock()
	select {
	case <-finished:
		if !errors.Is(runErr, context.Canceled) {
			t.Fatalf("shutdown returned %v, want context cancellation", runErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not join its released delivery worker")
	}
}
