package mist

import "testing"

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
