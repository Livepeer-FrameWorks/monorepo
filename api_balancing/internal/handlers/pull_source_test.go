package handlers

import "testing"

func TestPullUpstreamScoreStartsColdStream(t *testing.T) {
	if got := pullUpstreamScore("https://origin.example.com/live/master.m3u8", 0); got == 0 {
		t.Fatal("pullUpstreamScore returned 0 for valid cold upstream")
	}
}

func TestPullUpstreamScoreDoesNotBeatActiveDTSC(t *testing.T) {
	if got := pullUpstreamScore("https://origin.example.com/live/master.m3u8", 100); got >= 100 {
		t.Fatalf("pullUpstreamScore = %d, should not beat active DTSC score", got)
	}
}

func TestPullUpstreamScoreRejectsUnsafeURI(t *testing.T) {
	if got := pullUpstreamScore("https://127.0.0.1/live/master.m3u8", 0); got != 0 {
		t.Fatalf("pullUpstreamScore = %d, want 0 for unsafe URI", got)
	}
}
