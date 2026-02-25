package grpc

import "testing"

func TestMaskTargetURIRedactsQueryAndCredentials(t *testing.T) {
	got := maskTargetURI("srt://user:secret@example.com:9999?streamid=#!::r=live/test,m=publish&passphrase=topsecret")
	want := "srt://example.com:9999"
	if got != want {
		t.Fatalf("maskTargetURI() = %q, want %q", got, want)
	}
}

func TestMaskTargetURIMasksPathSecret(t *testing.T) {
	got := maskTargetURI("rtmp://live.twitch.tv/app/live_abc123def")
	want := "rtmp://live.twitch.tv/app/livexxxxdef"
	if got != want {
		t.Fatalf("maskTargetURI() = %q, want %q", got, want)
	}
}
