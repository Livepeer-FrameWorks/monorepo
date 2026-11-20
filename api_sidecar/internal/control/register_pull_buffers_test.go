package control

import "testing"

func TestPullBufferRegistrationInventory(t *testing.T) {
	for _, test := range []struct {
		name     string
		response map[string]any
		valid    bool
		count    int
	}{
		{"missing", map[string]any{}, false, 0},
		{"empty null", map[string]any{"active_streams": nil}, true, 0},
		{"malformed root", map[string]any{"active_streams": "unavailable"}, false, 0},
		{"unknown pull PID", map[string]any{"active_streams": map[string]any{"pull+camera": map[string]any{}}}, false, 0},
		{"fractional PID", map[string]any{"active_streams": map[string]any{"pull+camera": map[string]any{"pid": 1.5}}}, false, 0},
		{"direct sources only", map[string]any{"active_streams": map[string]any{"pull+z": map[string]any{"pid": float64(4242)}, "pull+a": map[string]any{"pid": float64(4343)}, "live+publisher": map[string]any{"pid": float64(4444)}, "processing+job": map[string]any{}}}, true, 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			buffers, err := pullBuffersFromRegistrationSnapshot(test.response)
			if (err == nil) != test.valid || len(buffers) != test.count {
				t.Fatalf("buffers=%v err=%v want valid=%v count=%d", buffers, err, test.valid, test.count)
			}
			if test.count == 2 && (buffers[0].GetRuntimeName() != "pull+a" || buffers[0].GetBufferPid() != 4343 || buffers[1].GetBufferPid() != 4242) {
				t.Fatalf("buffer identities lost: %v", buffers)
			}
		})
	}
}
