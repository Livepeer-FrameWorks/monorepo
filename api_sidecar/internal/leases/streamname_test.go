package leases

import "testing"

func TestParseDVRRollingPlaybackID(t *testing.T) {
	tests := []struct {
		name      string
		streamarg string
		wantToken string
		wantOK    bool
	}{
		{"happy", "dvr+abc123", "abc123", true},
		{"trims_surrounding_space", "dvr+  abc123 ", "abc123", true},
		{"wrong_prefix_vod", "vod+abc123", "", false},
		{"no_prefix", "abc123", "", false},
		{"prefix_only", "dvr+", "", false},
		{"prefix_only_whitespace", "dvr+   ", "", false},
		{"empty", "", "", false},
		// Prefix match is exact: "dvr" without the '+' separator is not a DVR name.
		{"prefix_without_plus", "dvrabc123", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token, ok := ParseDVRRollingPlaybackID(tt.streamarg)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if token != tt.wantToken {
				t.Fatalf("token = %q, want %q", token, tt.wantToken)
			}
		})
	}
}

func TestParseVODInternalName(t *testing.T) {
	tests := []struct {
		name      string
		streamarg string
		wantName  string
		wantOK    bool
	}{
		{"happy", "vod+stream_internal", "stream_internal", true},
		{"trims_surrounding_space", "vod+ stream_internal ", "stream_internal", true},
		{"wrong_prefix_dvr", "dvr+stream_internal", "", false},
		{"no_prefix", "stream_internal", "", false},
		{"prefix_only", "vod+", "", false},
		{"prefix_only_whitespace", "vod+  ", "", false},
		{"empty", "", "", false},
		{"prefix_without_plus", "vodstream", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, ok := ParseVODInternalName(tt.streamarg)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if name != tt.wantName {
				t.Fatalf("name = %q, want %q", name, tt.wantName)
			}
		})
	}
}
