package config

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

type recordingMistAPI struct {
	addedProtocols []map[string]interface{}
}

func (m *recordingMistAPI) ConfigBackup() (map[string]interface{}, error) {
	return nil, nil
}

func (m *recordingMistAPI) UpdateConfig(map[string]interface{}) (map[string]interface{}, error) {
	return nil, nil
}

func (m *recordingMistAPI) Save() error {
	return nil
}

func (m *recordingMistAPI) AddProtocols(protocols []map[string]interface{}) error {
	m.addedProtocols = append(m.addedProtocols, protocols...)
	return nil
}

func (m *recordingMistAPI) UpdateProtocol(map[string]interface{}, map[string]interface{}) error {
	return nil
}

func (m *recordingMistAPI) DeleteProtocols([]map[string]interface{}) error {
	return nil
}

func (m *recordingMistAPI) AddStreams(map[string]map[string]interface{}) error {
	return nil
}

func (m *recordingMistAPI) DeleteStreams([]string) error {
	return nil
}

func TestEnsureProtocolsAddsRTMP(t *testing.T) {
	mist := &recordingMistAPI{}
	manager := &Manager{mistClient: mist, logger: logging.NewLogger()}
	t.Setenv("EDGE_PUBLIC_URL", "https://edge.example/view")
	current := map[string]any{
		"config": map[string]any{
			"protocols": []any{},
		},
	}

	if err := manager.ensureProtocols(current); err != nil {
		t.Fatalf("ensureProtocols() error = %v", err)
	}

	wantConnectors := []string{
		"AAC", "CMAF", "DTSC", "EBML", "FLAC", "FLV", "H264", "HDS", "HLS",
		"HTTP", "HTTPTS", "JSON", "MP3", "MP4", "OGG", "RTMP", "RTSP", "SDP",
		"SubRip", "TSSRT", "WAV", "WebRTC", "JPG", "WSRaw", "ThumbVTT",
	}
	for _, connector := range wantConnectors {
		findAddedProtocol(t, mist.addedProtocols, connector)
	}

	http := findAddedProtocol(t, mist.addedProtocols, "HTTP")
	if got := http["pubaddr"]; !stringSlicesEqual(got, []string{"https://edge.example/view/"}) {
		t.Fatalf("HTTP pubaddr = %v, want https://edge.example/view/", got)
	}
	if got := http["default_track_sorting"]; got != "id_lth" {
		t.Fatalf("HTTP default_track_sorting = %v, want id_lth", got)
	}

	webrtc := findAddedProtocol(t, mist.addedProtocols, "WebRTC")
	if got := webrtc["bindhost"]; got != "0.0.0.0" {
		t.Fatalf("WebRTC bindhost = %v, want 0.0.0.0", got)
	}
	if got := webrtc["pubhost"]; got != "edge.example" {
		t.Fatalf("WebRTC pubhost = %v, want edge.example", got)
	}

	cmaf := findAddedProtocol(t, mist.addedProtocols, "CMAF")
	if got := cmaf["mergesessions"]; got != true {
		t.Fatalf("CMAF mergesessions = %v, want true", got)
	}
	if got := cmaf["nonchunked"]; got != true {
		t.Fatalf("CMAF nonchunked = %v, want true", got)
	}
}

func TestEnsureProtocolsDoesNotDuplicateExistingRTMP(t *testing.T) {
	mist := &recordingMistAPI{}
	manager := &Manager{mistClient: mist, logger: logging.NewLogger()}
	current := map[string]any{
		"config": map[string]any{
			"protocols": []any{
				map[string]any{"connector": "RTMP", "bindhost": "0.0.0.0"},
			},
		},
	}

	if err := manager.ensureProtocols(current); err != nil {
		t.Fatalf("ensureProtocols() error = %v", err)
	}

	for _, protocol := range mist.addedProtocols {
		if protocol["connector"] == "RTMP" {
			t.Fatalf("ensureProtocols() added duplicate RTMP protocol: %#v", protocol)
		}
	}
}

func findAddedProtocol(t *testing.T, protocols []map[string]interface{}, connector string) map[string]interface{} {
	t.Helper()
	for _, protocol := range protocols {
		if protocol["connector"] == connector {
			return protocol
		}
	}
	t.Fatalf("missing added protocol %q in %#v", connector, protocols)
	return nil
}

func stringSlicesEqual(got any, want []string) bool {
	switch typed := got.(type) {
	case []string:
		if len(typed) != len(want) {
			return false
		}
		for i := range typed {
			if typed[i] != want[i] {
				return false
			}
		}
		return true
	case []any:
		if len(typed) != len(want) {
			return false
		}
		for i := range typed {
			if typed[i] != want[i] {
				return false
			}
		}
		return true
	default:
		return false
	}
}
