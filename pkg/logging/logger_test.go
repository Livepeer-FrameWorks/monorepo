package logging

import "testing"

func TestNewLoggerWithService(t *testing.T) {
	l := NewLoggerWithService("svc-a")
	entry := l.WithField("k", "v")
	if entry == nil {
		t.Fatalf("expected non-nil entry")
	}
}
