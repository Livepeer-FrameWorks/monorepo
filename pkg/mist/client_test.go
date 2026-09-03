package mist

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func TestGetActiveStreamsFilteredContextCancelsInFlightRequest(t *testing.T) {
	requestStarted := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Query().Get("command"), "authorize") {
			_, _ = w.Write([]byte(`{"authorize":{"status":"OK"}}`))
			return
		}
		requestStarted <- struct{}{}
		<-r.Context().Done()
	}))
	defer srv.Close()

	client := NewClient(logging.NewLogger())
	client.BaseURL = srv.URL
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := client.GetActiveStreamsFilteredContext(ctx, []string{"live+alpha"})
		result <- err
	}()
	<-requestStarted
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("expected canceled request to return an error")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("canceled request waited for the HTTP client timeout")
	}
}

func TestGetStreamInfoContextCancelsInFlightRequest(t *testing.T) {
	requestStarted := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Query().Get("command"), "authorize") {
			_, _ = w.Write([]byte(`{"authorize":{"status":"OK"}}`))
			return
		}
		requestStarted <- struct{}{}
		<-r.Context().Done()
	}))
	defer srv.Close()

	client := NewClient(logging.NewLogger())
	client.BaseURL = srv.URL
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := client.GetStreamInfoContext(ctx, "live+alpha")
		result <- err
	}()
	<-requestStarted
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("expected canceled request to return an error")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("canceled request waited for the HTTP client timeout")
	}
}

// Ambiguity must distinguish "the mutation may have reached Mist" (accepted-but-
// unconfirmed) from "authentication failed before the mutation was ever sent".
// Only the former is ErrMistAmbiguous; the latter proves the command was not sent.
func TestPushStartAmbiguityClassification(t *testing.T) {
	t.Run("auth transport failure is not ambiguous", func(t *testing.T) {
		// Close the server so the auth handshake's transport fails. Auth runs
		// before the command is transmitted, so the command was never sent.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		u := srv.URL
		srv.Close()

		c := NewClient(logging.NewLogger())
		c.BaseURL = u

		err := c.PushStart("live+x", "/data/dvr/x/seg.ts")
		if err == nil {
			t.Fatal("expected an error when the auth transport fails")
		}
		if errors.Is(err, ErrMistAmbiguous) {
			t.Fatalf("auth-time failure must NOT be ambiguous (command never sent), got: %v", err)
		}
	})

	t.Run("command failure after auth stays ambiguous", func(t *testing.T) {
		// Auth succeeds; the push_start response body is unparseable, so Mist may
		// have accepted the mutation but we cannot confirm — genuinely ambiguous.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Query().Get("command"), "authorize") {
				_, _ = w.Write([]byte(`{"authorize":{"status":"OK"}}`))
				return
			}
			_, _ = w.Write([]byte(`{not-json`))
		}))
		defer srv.Close()

		c := NewClient(logging.NewLogger())
		c.BaseURL = srv.URL

		err := c.PushStart("live+x", "/data/dvr/x/seg.ts")
		if err == nil {
			t.Fatal("expected an error when the command response is unparseable")
		}
		if !errors.Is(err, ErrMistAmbiguous) {
			t.Fatalf("a failed command send after successful auth must stay ambiguous, got: %v", err)
		}
	})
}

func TestParsePushList_NullMeansEmpty(t *testing.T) {
	pushes, err := parsePushList(nil)
	if err != nil {
		t.Fatalf("parsePushList(nil) error = %v", err)
	}
	if len(pushes) != 0 {
		t.Fatalf("parsePushList(nil) returned %d pushes, want 0", len(pushes))
	}
}

func TestParsePushList_Array(t *testing.T) {
	pushes, err := parsePushList([]interface{}{
		[]interface{}{float64(123), "live+stream-a", "/tmp/out.ts", "/tmp/out-actual.ts"},
	})
	if err != nil {
		t.Fatalf("parsePushList(array) error = %v", err)
	}
	if len(pushes) != 1 {
		t.Fatalf("got %d pushes, want 1", len(pushes))
	}
	got := pushes[0]
	if got.ID != 123 || got.StreamName != "live+stream-a" || got.TargetURI != "/tmp/out.ts" || got.ActualURI != "/tmp/out-actual.ts" {
		t.Fatalf("unexpected push parsed: %+v", got)
	}
}

func TestNukeStreamSendsRuntimeResetCommand(t *testing.T) {
	var commands []map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := r.URL.Query().Get("command")
		var cmd map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &cmd); err != nil {
			t.Fatalf("command JSON: %v", err)
		}
		commands = append(commands, cmd)
		_, _ = w.Write([]byte(`{"authorize":{"status":"OK"}}`))
	}))
	defer srv.Close()

	c := NewClient(logging.NewLogger())
	c.BaseURL = srv.URL

	if err := c.NukeStream("processing+artifact"); err != nil {
		t.Fatalf("NukeStream error = %v", err)
	}
	if len(commands) != 2 {
		t.Fatalf("got %d commands, want auth + nuke", len(commands))
	}
	if got := commands[1]["nuke_stream"]; got != "processing+artifact" {
		t.Fatalf("nuke_stream = %#v, want processing+artifact", got)
	}
	if _, ok := commands[1]["deletestream"]; ok {
		t.Fatal("NukeStream must not send deletestream")
	}
}

func TestPushKillSendsHardKillCommand(t *testing.T) {
	var commands []map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := r.URL.Query().Get("command")
		var cmd map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &cmd); err != nil {
			t.Fatalf("command JSON: %v", err)
		}
		commands = append(commands, cmd)
		_, _ = w.Write([]byte(`{"authorize":{"status":"OK"}}`))
	}))
	defer srv.Close()

	c := NewClient(logging.NewLogger())
	c.BaseURL = srv.URL

	if err := c.PushKill(42); err != nil {
		t.Fatalf("PushKill error = %v", err)
	}
	if len(commands) != 2 {
		t.Fatalf("got %d commands, want auth + push_kill", len(commands))
	}
	if got := commands[1]["push_kill"]; got != float64(42) {
		t.Fatalf("push_kill = %#v, want 42", got)
	}
}
