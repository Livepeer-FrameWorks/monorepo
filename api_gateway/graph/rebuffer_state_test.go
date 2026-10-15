package graph

import (
	"context"
	"testing"

	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"

	"frameworks/api_gateway/graph/model"
)

// The first buffer edge of a stream on a node has no earlier edge; its
// previous state is UNKNOWN, not one of the states Mist reported. The edges
// after it keep the state the earlier edge reported.
func TestRebufferingEventPreviousStateOfFirstEdgeIsUnknown(t *testing.T) {
	r := &rebufferingEventResolver{&Resolver{}}
	for prev, want := range map[string]model.BufferState{
		"":        model.BufferState("UNKNOWN"),
		"FULL":    model.BufferState("FULL"),
		"DRY":     model.BufferState("DRY"),
		"RECOVER": model.BufferState("RECOVER"),
		"EMPTY":   model.BufferState("EMPTY"),
	} {
		got, err := r.PreviousState(context.Background(), &periscopepb.RebufferingEvent{BufferState: "DRY", PrevState: prev})
		if err != nil {
			t.Fatalf("PreviousState(%q): %v", prev, err)
		}
		if got != want {
			t.Errorf("PreviousState(%q) = %s, want %s", prev, got, want)
		}
		if !got.IsValid() {
			t.Errorf("PreviousState(%q) = %s is not a value of the BufferState enum", prev, got)
		}
	}
}
