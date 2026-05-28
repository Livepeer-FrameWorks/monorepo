package mist

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

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
