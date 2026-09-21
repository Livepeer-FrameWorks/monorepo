package main

import (
	"context"
	"testing"
)

type recordingStreamCloser struct {
	closed int
}

func (r *recordingStreamCloser) Shutdown() { r.closed++ }

func TestCloseStreamsHookEndsStreamsWhenDrainStarts(t *testing.T) {
	streams := &recordingStreamCloser{}
	hook := closeStreamsHook(streams)
	if streams.closed != 0 {
		t.Fatal("building the drain hook must not close streams")
	}
	hook(context.Background())
	if streams.closed != 1 {
		t.Fatalf("streams closed %d times by the drain hook, want 1", streams.closed)
	}
}
