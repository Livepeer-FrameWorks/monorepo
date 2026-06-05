package grpc

import (
	"database/sql"
	"testing"
)

// parseDVRPolicy tolerantly decodes a tier's DVR entitlements JSON: a null/empty
// column or invalid JSON yields nil (no policy), and each field is decoded
// independently so a partial document produces a partial policy rather than
// being rejected wholesale.
func TestParseDVRPolicy(t *testing.T) {
	if parseDVRPolicy(sql.NullString{Valid: false}) != nil {
		t.Error("invalid NullString should yield nil")
	}
	if parseDVRPolicy(sql.NullString{Valid: true, String: ""}) != nil {
		t.Error("empty string should yield nil")
	}
	if parseDVRPolicy(sql.NullString{Valid: true, String: "{not json"}) != nil {
		t.Error("invalid JSON should yield nil")
	}

	// Partial document: only one field set; the rest default to zero values.
	partial := parseDVRPolicy(sql.NullString{Valid: true, String: `{"dvr_max_entries": 7}`})
	if partial == nil {
		t.Fatal("partial JSON should yield a policy")
	}
	if partial.GetMaxEntries() != 7 {
		t.Errorf("MaxEntries = %d, want 7", partial.GetMaxEntries())
	}
	if partial.GetDefaultWindowSeconds() != 0 {
		t.Errorf("unset DefaultWindowSeconds should be 0, got %d", partial.GetDefaultWindowSeconds())
	}

	full := parseDVRPolicy(sql.NullString{Valid: true, String: `{
		"dvr_default_window_seconds": 60,
		"dvr_max_window_seconds": 3600,
		"dvr_default_segment_duration_seconds": 6,
		"dvr_max_entries": 100,
		"dvr_allow_cluster_extension": true
	}`})
	if full == nil {
		t.Fatal("full JSON should yield a policy")
	}
	if full.GetDefaultWindowSeconds() != 60 || full.GetMaxWindowSeconds() != 3600 ||
		full.GetDefaultSegmentDurationSeconds() != 6 || full.GetMaxEntries() != 100 ||
		!full.GetAllowClusterExtension() {
		t.Errorf("full policy not fully populated: %+v", full)
	}
}

// formatOptionalMoney renders a nullable money column at 2 decimal places;
// !Valid is empty, an unparseable value errors rather than coercing to zero.
func TestFormatOptionalMoney(t *testing.T) {
	tests := []struct {
		name    string
		in      sql.NullString
		want    string
		wantErr bool
	}{
		{"null is empty", sql.NullString{Valid: false}, "", false},
		{"whole number gets cents", sql.NullString{Valid: true, String: "10"}, "10.00", false},
		{"one decimal padded", sql.NullString{Valid: true, String: "12.5"}, "12.50", false},
		{"rounds up", sql.NullString{Valid: true, String: "12.999"}, "13.00", false},
		{"rounds down", sql.NullString{Valid: true, String: "12.344"}, "12.34", false},
		{"invalid errors", sql.NullString{Valid: true, String: "abc"}, "", true},
	}
	for _, tt := range tests {
		got, err := formatOptionalMoney(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("%s: err = %v, wantErr %v", tt.name, err, tt.wantErr)
		}
		if got != tt.want {
			t.Errorf("%s: formatOptionalMoney = %q, want %q", tt.name, got, tt.want)
		}
	}
}
