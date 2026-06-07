package triggers

import (
	"testing"

	"frameworks/api_balancing/internal/state"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// TestMapOperationalMode pins the proto→internal node-mode translation. The
// critical invariant is the default arm: an unknown/unspecified proto mode must
// report ok=false rather than silently coercing to a real mode, which would
// mis-drive node draining/maintenance.
func TestMapOperationalMode(t *testing.T) {
	tests := []struct {
		name     string
		mode     ipcpb.NodeOperationalMode
		wantMode state.NodeOperationalMode
		wantOK   bool
	}{
		{
			name:     "normal",
			mode:     ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_NORMAL,
			wantMode: state.NodeModeNormal,
			wantOK:   true,
		},
		{
			name:     "draining",
			mode:     ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_DRAINING,
			wantMode: state.NodeModeDraining,
			wantOK:   true,
		},
		{
			name:     "maintenance",
			mode:     ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_MAINTENANCE,
			wantMode: state.NodeModeMaintenance,
			wantOK:   true,
		},
		{
			name:     "unspecified_is_not_ok",
			mode:     ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_UNSPECIFIED,
			wantMode: "",
			wantOK:   false,
		},
		{
			name:     "unknown_enum_is_not_ok",
			mode:     ipcpb.NodeOperationalMode(9999),
			wantMode: "",
			wantOK:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotMode, gotOK := mapOperationalMode(tt.mode)
			if gotOK != tt.wantOK {
				t.Errorf("ok = %v, want %v", gotOK, tt.wantOK)
			}
			if gotMode != tt.wantMode {
				t.Errorf("mode = %q, want %q", gotMode, tt.wantMode)
			}
		})
	}
}
