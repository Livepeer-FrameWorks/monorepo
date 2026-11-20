package mist

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func productionOutputFixture(t *testing.T) (map[string]any, map[string]any) {
	t.Helper()
	data, err := os.ReadFile("testdata/output-catalog-production.json")
	if err != nil {
		t.Fatal(err)
	}
	var inventory map[string]any
	if err := json.Unmarshal(data, &inventory); err != nil {
		t.Fatal(err)
	}
	return inventory["outputs"].(map[string]any), inventory
}

func TestPlaybackOutputCatalogProductionMethods(t *testing.T) {
	reported, inventory := productionOutputFixture(t)
	if _, present := reported["MP4"]; present {
		t.Fatal("fixture must reproduce missing metrics MP4")
	}
	catalog, err := PlaybackOutputCatalog(reported, inventory)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"MP4 progressive":             "https://edge.example/view/$.mp4",
		"MP4 WebSocket":               "wss://edge.example/view/$.mp4",
		"JSON progressive":            "https://edge.example/view/$.json",
		"JSON WebSocket":              "wss://edge.example/view/$.json",
		"WebVTT subtitle progressive": "https://edge.example/view/$.webvtt",
		"SRT subtitle progressive":    "https://edge.example/view/$.srt",
		"Thumbnail VTT progressive":   "https://edge.example/view/$.thumbvtt",
	} {
		if catalog[name] != want {
			t.Errorf("%s = %v, want %s", name, catalog[name], want)
		}
	}
	if _, present := reported["MP4 progressive"]; present {
		t.Fatal("projection mutated metrics")
	}
}

func TestPlaybackOutputCatalogRequiresDeclaredNativeMatroska(t *testing.T) {
	for _, tc := range []struct {
		name     string
		matches  []any
		want     string
		disabled string
	}{
		{name: "existing EBML WebM advertisement"},
		{name: "similar but non-native route", matches: []any{"/$.webm", "/$.mkv?other=1"}},
		{name: "native route", matches: []any{"/$.webm", "/$.mkv"}, want: "https://edge.example/view/chapter.mkv"},
		{name: "disabled EBML connector", matches: []any{"/$.mkv"}, disabled: "EBML"},
		{name: "disabled HTTP listener", matches: []any{"/$.mkv"}, disabled: "HTTP"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reported, inventory := productionOutputFixture(t)
			reported[nativeMatroskaOutput] = "https://stale.example/$.mkv"
			connector := inventory["capabilities"].(map[string]any)["connectors"].(map[string]any)["EBML"].(map[string]any)
			connector["url_match"] = tc.matches
			base := "https://edge.example/view"
			wantWebM := base + "/chapter.webm"
			if tc.disabled != "" {
				config := inventory["config_backup"].(map[string]any)["config"].(map[string]any)
				for _, raw := range config["protocols"].([]any) {
					protocol := raw.(map[string]any)
					if protocol["connector"] == tc.disabled {
						protocol["disabled"] = true
					}
				}
				if tc.disabled == "EBML" {
					wantWebM = ""
				}
			}
			catalog, err := PlaybackOutputCatalog(reported, inventory)
			if err != nil {
				t.Fatal(err)
			}
			if got := ResolveNativeMatroskaPlaybackURL(catalog, base, "chapter"); got != tc.want {
				t.Errorf("MKV resolved to %q, want %q", got, tc.want)
			}
			if got := ResolvePlaybackURL(catalog, base, "webm", "chapter"); got != wantWebM {
				t.Errorf("WebM resolved to %q, want %q", got, wantWebM)
			}
			if got := ResolvePlaybackURL(catalog, base, "mkv", "chapter"); got != wantWebM {
				t.Errorf("EBML placement resolved to %q, want %q", got, wantWebM)
			}
		})
	}
}

func TestResolvePlaybackSeparatesMP4Transports(t *testing.T) {
	outputs := map[string]any{"MP4 progressive": "https://media/view/$.mp4", "MP4 WebSocket": "wss://media/view/$.mp4"}
	for protocol, want := range map[string]string{"mp4": "https://media/view/live+stream.mp4", "wsmp4": "wss://media/view/live+stream.mp4"} {
		if got := ResolvePlaybackURL(outputs, "https://media/view", protocol, "live+stream"); got != want {
			t.Errorf("%s resolved to %q, want %q", protocol, got, want)
		}
	}
}

func TestPlaybackOutputCatalogRequiresEnabledConnectorAndHTTP(t *testing.T) {
	for _, disabled := range []string{"MP4", "HTTP"} {
		t.Run(disabled, func(t *testing.T) {
			reported, inventory := productionOutputFixture(t)
			cfg := inventory["config_backup"].(map[string]any)["config"].(map[string]any)
			for _, raw := range cfg["protocols"].([]any) {
				p := raw.(map[string]any)
				if p["connector"] == disabled {
					p["enabled"] = false
				}
			}
			catalog, err := PlaybackOutputCatalog(reported, inventory)
			if err != nil {
				t.Fatal(err)
			}
			if _, invented := catalog["MP4 progressive"]; invented {
				t.Fatal("disabled dependency advertised MP4")
			}
		})
	}
	if _, err := PlaybackOutputCatalog(map[string]any{"HTTP": "https://media/$.html"}, nil); err == nil {
		t.Fatal("missing configuration treated as complete evidence")
	}
}

func TestPlaybackMethodURLRejectsUnsafeMetadata(t *testing.T) {
	for _, relative := range []string{"https://elsewhere/$.mp4", "//elsewhere/$.mp4", "/../$.mp4", "/%2e%2e/$.mp4", "/$.mp4?token=secret", "/$.mp4#fragment", "/no-placeholder.mp4"} {
		reported, inventory := productionOutputFixture(t)
		cap := inventory["capabilities"].(map[string]any)["connectors"].(map[string]any)["MP4"].(map[string]any)
		for _, raw := range cap["methods"].([]any) {
			raw.(map[string]any)["url_rel"] = relative
		}
		catalog, err := PlaybackOutputCatalog(reported, inventory)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := catalog["MP4 progressive"]; ok {
			t.Errorf("unsafe method accepted: %s", relative)
		}
	}
}

func TestExplicitMethodsCannotInventAnotherTransport(t *testing.T) {
	if got := ResolvePlaybackURL(map[string]any{"MP4 WebSocket": "wss://media/$.mp4"}, "https://media", "mp4", "stream"); got != "" {
		t.Fatalf("WebSocket admitted as progressive: %s", got)
	}
	if got := ResolvePlaybackURL(map[string]any{"MP4 progressive": "https://media/$.mp4"}, "https://media", "wsmp4", "stream"); got != "" {
		t.Fatalf("progressive admitted as WebSocket: %s", got)
	}
}

func TestDisabledConnectorCannotSurviveInReportedCatalog(t *testing.T) {
	reported, inventory := productionOutputFixture(t)
	reported["mp4"] = "https://edge.example/view/$.mp4"
	reported["MP4 progressive"] = "https://edge.example/view/$.mp4"
	config := inventory["config_backup"].(map[string]any)["config"].(map[string]any)
	for _, raw := range config["protocols"].([]any) {
		protocol := raw.(map[string]any)
		if protocol["connector"] == "MP4" {
			protocol["disabled"] = true
		}
	}
	catalog, err := PlaybackOutputCatalog(reported, inventory)
	if err != nil {
		t.Fatal(err)
	}
	if catalog["mp4"] != nil || catalog["MP4 progressive"] != nil {
		t.Fatal("disabled MP4 survived through a connector alias or method label")
	}
}

func TestFetchPlaybackOutputsIsReadOnlyAndCancellable(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var command map[string]any
		if err := json.Unmarshal([]byte(r.URL.Query().Get("command")), &command); err != nil {
			t.Error(err)
		}
		if len(command) != 2 || command["capabilities"] != true || command["config_backup"] != true {
			t.Error("playback inventory must use only read-only capabilities/config_backup")
		}
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	client := NewClient(logging.NewLogger(), ClientConfig{BaseURL: server.URL})
	client.authenticated = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := client.FetchPlaybackOutputsContext(ctx, nil)
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("inventory request never started")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled inventory request succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled inventory request remained active")
	}
}
