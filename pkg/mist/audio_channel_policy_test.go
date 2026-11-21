package mist

import (
	"encoding/json"
	"testing"
)

func TestPlatformOpusIsStereoAndAACRetainsSurround(t *testing.T) {
	raw := `[{"process":"AV","codec":"opus"},{"process":"AV","codec":"AAC"},{"process":"AV","codec":"H264"}]`
	got := ApplyAudioChannelPolicy(raw)
	var p []map[string]any
	if err := json.Unmarshal([]byte(got), &p); err != nil {
		t.Fatal(err)
	}
	if p[0]["max_channels"] != float64(2) || p[1]["max_channels"] != nil || p[2]["max_channels"] != nil {
		t.Fatalf("platform audio policy changed surround or failed to cap Opus: %s", got)
	}
	if ApplyAudioChannelPolicy(got) != got {
		t.Fatal("audio policy is not stable on retries")
	}
}

func TestLocalLiveFallbackUsesBackoffAndVODKeepsRestartsDisabled(t *testing.T) {
	for _, mode := range []string{"", "fixed", "disabled"} {
		raw := `[{"process":"Livepeer","restart_type":"` + mode + `","target_profiles":[{"height":360,"profile":"H264Main"}]}]`
		var p []map[string]any
		if err := json.Unmarshal([]byte(ReplaceLivepeerWithLocal(raw)), &p); err != nil {
			t.Fatal(err)
		}
		if mode == "disabled" {
			if p[0]["restart_type"] != "disabled" {
				t.Fatal("one-shot VOD restarts enabled")
			}
			continue
		}
		if p[0]["restart_type"] != "backoff" || p[0]["restart_delay"] == nil || p[0]["restart_delay"].(float64) < 1000 {
			t.Fatalf("live fallback can respawn in a loop: %v", p[0])
		}
	}
}

func TestLiveAudioAndVideoRestartsAreBounded(t *testing.T) {
	raw := `[{"process":"AV","codec":"opus"},{"process":"AV","codec":"H264"},{"process":"AV","codec":"AAC","restart_type":"disabled"}]`
	var p []map[string]any
	if err := json.Unmarshal([]byte(BoundLiveProcessRestarts(raw)), &p); err != nil {
		t.Fatal(err)
	}
	for _, i := range []int{0, 1} {
		if p[i]["restart_type"] != "backoff" || p[i]["restart_delay"] != float64(30000) {
			t.Fatalf("unbounded live process: %v", p[i])
		}
	}
	if p[2]["restart_type"] != "disabled" {
		t.Fatal("disabled audio was restarted")
	}
}
