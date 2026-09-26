package graph

import (
	"context"
	"testing"

	"frameworks/api_gateway/graph/model"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

func TestStreamSourceModeProjection(t *testing.T) {
	resolver := &streamResolver{}
	tests := []struct {
		name     string
		wireMode string
		wantMode model.IngestMode
		wantKey  bool
	}{
		{name: "legacy empty defaults to push", wireMode: "", wantMode: model.IngestModePush, wantKey: true},
		{name: "push", wireMode: "push", wantMode: model.IngestModePush, wantKey: true},
		{name: "pull", wireMode: "pull", wantMode: model.IngestModePull, wantKey: false},
		{name: "managed wire name", wireMode: "mist_native", wantMode: model.IngestModeManaged, wantKey: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stream := &commodorepb.Stream{IngestMode: tt.wireMode, StreamKey: "secret"}
			mode, err := resolver.IngestMode(context.Background(), stream)
			if err != nil || mode != tt.wantMode {
				t.Fatalf("IngestMode() = (%q, %v), want (%q, nil)", mode, err, tt.wantMode)
			}
			key, err := resolver.StreamKey(streamKeyScopeCtx("jwt"), stream)
			if err != nil || (key != nil) != tt.wantKey {
				t.Fatalf("StreamKey() = (%v, %v), want present=%v", key, err, tt.wantKey)
			}
		})
	}
}
