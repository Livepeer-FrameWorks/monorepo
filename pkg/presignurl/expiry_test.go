package presignurl

import (
	"testing"
	"time"
)

func TestExpiryReadsSigV4Window(t *testing.T) {
	raw := "https://s3.example/bucket/a.mkv?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Date=20261012T085900Z&X-Amz-Expires=900&X-Amz-Signature=abc"
	got, ok := Expiry(raw)
	if !ok {
		t.Fatal("expected a readable expiry")
	}
	want := time.Date(2026, 10, 12, 9, 14, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("expiry=%s want %s", got, want)
	}
}

func TestExpiryRejectsURLsWithoutSignedWindow(t *testing.T) {
	for _, raw := range []string{
		"",
		"http://127.0.0.1:18007/internal/artifact/vod/h.mkv",
		"https://s3.example/a?X-Amz-Date=bad&X-Amz-Expires=900",
		"https://s3.example/a?X-Amz-Date=20261012T085900Z",
		"https://s3.example/a?X-Amz-Date=20261012T085900Z&X-Amz-Expires=0",
	} {
		if _, ok := Expiry(raw); ok {
			t.Fatalf("Expiry(%q) reported a window", raw)
		}
	}
}

func TestEarliestExpiryPicksSoonestAndSkipsUnsigned(t *testing.T) {
	media := "https://s3.example/a?X-Amz-Date=20261012T085900Z&X-Amz-Expires=3600"
	dtsh := "https://s3.example/a.dtsh?X-Amz-Date=20261012T085900Z&X-Amz-Expires=900"
	got, ok := EarliestExpiry(media, "", "http://peer/x", dtsh)
	if !ok {
		t.Fatal("expected a window")
	}
	if want := time.Date(2026, 10, 12, 9, 14, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("earliest=%s want %s", got, want)
	}
	if _, ok := EarliestExpiry("", "http://peer/x"); ok {
		t.Fatal("no signed URL must report no window")
	}
}
