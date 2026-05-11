package control

import (
	"testing"
	"time"
)

func TestDVRChapterDefrostURLTTL(t *testing.T) {
	if got := dvrChapterDefrostURLTTL(0, 5*60*1000); got != 65*time.Minute {
		t.Fatalf("5m chapter ttl = %s, want 65m", got)
	}
	if got := dvrChapterDefrostURLTTL(0, int64((2 * time.Hour).Milliseconds())); got != 3*time.Hour {
		t.Fatalf("2h chapter ttl = %s, want 3h", got)
	}
	if got := dvrChapterDefrostURLTTL(0, int64((30 * 24 * time.Hour).Milliseconds())); got != 7*24*time.Hour {
		t.Fatalf("long chapter ttl = %s, want 7d", got)
	}
}
