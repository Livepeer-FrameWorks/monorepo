package resolvers

import (
	"testing"

	"frameworks/api_gateway/graph/model"
)

func TestIngestModeToWire(t *testing.T) {
	tests := []struct {
		mode model.IngestMode
		want string
	}{
		{mode: model.IngestModePush, want: "push"},
		{mode: model.IngestModePull, want: "pull"},
		{mode: model.IngestModeManaged, want: "mist_native"},
	}
	for _, tt := range tests {
		if got := ingestModeToWire(tt.mode); got != tt.want {
			t.Errorf("ingestModeToWire(%q) = %q, want %q", tt.mode, got, tt.want)
		}
	}
}
