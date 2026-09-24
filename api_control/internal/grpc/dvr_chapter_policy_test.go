package grpc

import (
	"database/sql"
	"testing"
)

// A stream's NULL chapter mode is NONE and must reach Foghorn as an explicit
// "none": Foghorn records window-sized chapters for an empty mode. The
// interval travels only with fixed_interval.
func TestStreamDVRChapterPolicy(t *testing.T) {
	cases := []struct {
		name         string
		mode         sql.NullString
		interval     sql.NullInt32
		wantMode     string
		wantInterval int32
	}{
		{"NULL is none", sql.NullString{}, sql.NullInt32{}, "none", 0},
		{"empty is none", sql.NullString{String: "", Valid: true}, sql.NullInt32{}, "none", 0},
		{"window drops stale interval", sql.NullString{String: "window_sized_chapters", Valid: true}, sql.NullInt32{Int32: 7200, Valid: true}, "window_sized_chapters", 0},
		{"fixed keeps interval", sql.NullString{String: "fixed_interval", Valid: true}, sql.NullInt32{Int32: 7200, Valid: true}, "fixed_interval", 7200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mode, interval := streamDVRChapterPolicy(tc.mode, tc.interval)
			if mode != tc.wantMode || interval != tc.wantInterval {
				t.Fatalf("streamDVRChapterPolicy = (%q, %d), want (%q, %d)", mode, interval, tc.wantMode, tc.wantInterval)
			}
		})
	}
}
