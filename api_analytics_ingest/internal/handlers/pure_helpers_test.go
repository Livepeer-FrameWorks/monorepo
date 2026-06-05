package handlers

import "testing"

// boundedSchemaVersion clamps an int32 schema version into a ClickHouse uint8
// column. Out-of-range versions must saturate, never wrap.
func TestBoundedSchemaVersion(t *testing.T) {
	tests := []struct {
		in   int32
		want uint8
	}{
		{-1, 0},
		{0, 0},
		{1, 1},
		{255, 255},
		{256, 255},
		{1 << 20, 255},
	}
	for _, tt := range tests {
		if got := boundedSchemaVersion(tt.in); got != tt.want {
			t.Errorf("boundedSchemaVersion(%d) = %d, want %d", tt.in, got, tt.want)
		}
	}
}
