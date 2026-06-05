package control

import (
	"testing"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// parseRequestedMode maps an operator-typed mode string (with short aliases) to
// the node operational mode enum. Unknown input must fall to UNSPECIFIED, not
// silently to NORMAL, so a typo can't accidentally un-drain a node.
func TestParseRequestedMode(t *testing.T) {
	tests := []struct {
		in   string
		want ipcpb.NodeOperationalMode
	}{
		{"draining", ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_DRAINING},
		{"drain", ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_DRAINING},
		{"MAINTENANCE", ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_MAINTENANCE},
		{"maint", ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_MAINTENANCE},
		{"normal", ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_NORMAL},
		{"  ", ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_NORMAL},
		{"", ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_NORMAL},
		{"bogus", ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_UNSPECIFIED},
	}
	for _, tt := range tests {
		if got := parseRequestedMode(tt.in); got != tt.want {
			t.Errorf("parseRequestedMode(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}
