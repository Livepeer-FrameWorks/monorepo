package config

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

type recordingMistAPI struct {
	addedProtocols  []map[string]interface{}
	protocolUpdates []protocolUpdate
	updatedConfigs  []map[string]interface{}
	backupResult    map[string]interface{} // returned by ConfigBackup (nil ⇒ nil)
}

func (m *recordingMistAPI) ConfigBackup() (map[string]interface{}, error) {
	return m.backupResult, nil
}

func (m *recordingMistAPI) UpdateConfig(partial map[string]interface{}) (map[string]interface{}, error) {
	m.updatedConfigs = append(m.updatedConfigs, partial)
	return nil, nil
}

func (m *recordingMistAPI) Save() error {
	return nil
}

func (m *recordingMistAPI) AddProtocols(protocols []map[string]interface{}) error {
	m.addedProtocols = append(m.addedProtocols, protocols...)
	return nil
}

func (m *recordingMistAPI) UpdateProtocol(oldConfig, newConfig map[string]interface{}) error {
	m.protocolUpdates = append(m.protocolUpdates, protocolUpdate{old: oldConfig, new: newConfig})
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
	if got := cmaf["dashlowlatency"]; got != true {
		t.Fatalf("CMAF dashlowlatency = %v, want true", got)
	}
	for _, oldName := range []string{"nonchunked", "dashllchunked", "chunkedsegments"} {
		if got, ok := cmaf[oldName]; ok {
			t.Fatalf("CMAF %s = %v, want unset", oldName, got)
		}
	}

	hls := findAddedProtocol(t, mist.addedProtocols, "HLS")
	for _, oldName := range []string{"nonchunked", "chunkedsegments"} {
		if got, ok := hls[oldName]; ok {
			t.Fatalf("HLS %s = %v, want unset", oldName, got)
		}
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

func TestEnsureProtocolsUpdatesRenamedMistProtocolOptions(t *testing.T) {
	mist := &recordingMistAPI{}
	manager := &Manager{mistClient: mist, logger: logging.NewLogger()}
	current := map[string]any{
		"config": map[string]any{
			"protocols": []any{
				map[string]any{"connector": "CMAF", "mergesessions": true, "dashllchunked": true, "dashlowlatency": false, "chunkedsegments": true},
				map[string]any{"connector": "HLS", "nonchunked": false, "chunkedsegments": true},
			},
		},
	}

	if err := manager.ensureProtocols(current); err != nil {
		t.Fatalf("ensureProtocols() error = %v", err)
	}

	cmaf := findProtocolUpdate(t, mist.protocolUpdates, "CMAF").new
	if got := cmaf["dashlowlatency"]; got != true {
		t.Fatalf("CMAF dashlowlatency = %v, want true", got)
	}
	for _, oldName := range []string{"dashllchunked", "nonchunked", "chunkedsegments"} {
		if got, ok := cmaf[oldName]; ok {
			t.Fatalf("CMAF update kept stale %s = %v", oldName, got)
		}
	}

	hls := findProtocolUpdate(t, mist.protocolUpdates, "HLS").new
	for _, oldName := range []string{"nonchunked", "chunkedsegments"} {
		if got, ok := hls[oldName]; ok {
			t.Fatalf("HLS update kept stale %s = %v", oldName, got)
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

func findProtocolUpdate(t *testing.T, updates []protocolUpdate, connector string) protocolUpdate {
	t.Helper()
	for _, update := range updates {
		if update.old["connector"] == connector {
			return update
		}
	}
	t.Fatalf("missing protocol update %q in %#v", connector, updates)
	return protocolUpdate{}
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
