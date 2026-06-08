package tools

import "testing"

// normalizeTimeRange maps a label to a concrete window; unknown/empty labels must
// fall back to the 1h default rather than producing a zero window.
func TestNormalizeTimeRange(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		wantLabel string
		wantHours float64
	}{
		{"empty_defaults_to_1h", "", "last_1h", 1},
		{"last_6h", "last_6h", "last_6h", 6},
		{"last_24h", "last_24h", "last_24h", 24},
		{"last_7d", "last_7d", "last_7d", 7 * 24},
		{"unknown_label_keeps_label_but_1h_window", "last_99y", "last_99y", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			label, opts := normalizeTimeRange(tt.in)
			if label != tt.wantLabel {
				t.Errorf("label = %q, want %q", label, tt.wantLabel)
			}
			if opts == nil {
				t.Fatal("nil time range opts")
			}
			gotHours := opts.EndTime.Sub(opts.StartTime).Hours()
			if gotHours < tt.wantHours-0.01 || gotHours > tt.wantHours+0.01 {
				t.Errorf("window = %.2fh, want %.2fh", gotHours, tt.wantHours)
			}
		})
	}
}
