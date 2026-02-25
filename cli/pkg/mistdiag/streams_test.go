package mistdiag

import (
	"testing"
)

func TestParseActiveStreams_Map(t *testing.T) {
	body := `{"active_streams":{"live+abc":{"source":"push://"},"live+def":{"source":"push://"}}}`

	streams, err := parseActiveStreams(body)
	if err != nil {
		t.Fatalf("parseActiveStreams() error = %v", err)
	}

	if len(streams) != 2 {
		t.Fatalf("expected 2 streams, got %d", len(streams))
	}

	names := map[string]bool{}
	for _, s := range streams {
		names[s.Name] = true
		if s.HLSURL == "" {
			t.Errorf("stream %s should have HLSURL", s.Name)
		}
	}

	if !names["live+abc"] || !names["live+def"] {
		t.Errorf("missing expected stream names, got %v", names)
	}
}

func TestParseActiveStreams_Array(t *testing.T) {
	body := `{"active_streams":["live+abc","live+def"]}`

	streams, err := parseActiveStreams(body)
	if err != nil {
		t.Fatalf("parseActiveStreams() error = %v", err)
	}

	if len(streams) != 2 {
		t.Fatalf("expected 2 streams, got %d", len(streams))
	}
}

func TestParseActiveStreams_Empty(t *testing.T) {
	body := `{"active_streams":{}}`

	streams, err := parseActiveStreams(body)
	if err != nil {
		t.Fatalf("parseActiveStreams() error = %v", err)
	}

	if len(streams) != 0 {
		t.Errorf("expected 0 streams, got %d", len(streams))
	}
}

func TestParseActiveStreams_Missing(t *testing.T) {
	body := `{"some_other_key":true}`

	streams, err := parseActiveStreams(body)
	if err != nil {
		t.Fatalf("parseActiveStreams() error = %v", err)
	}

	if streams != nil {
		t.Errorf("expected nil streams, got %v", streams)
	}
}

func TestStreamHLSURL(t *testing.T) {
	got := StreamHLSURL("live+abc123")
	want := "http://localhost:8080/hls/live+abc123/index.m3u8"
	if got != want {
		t.Errorf("StreamHLSURL() = %q, want %q", got, want)
	}
}
