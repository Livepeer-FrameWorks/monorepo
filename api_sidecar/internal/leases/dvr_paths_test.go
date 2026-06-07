package leases

import "testing"

func TestDeriveDvrHashFromRollingManifestPath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{
			name: "happy_mirror_shape",
			path: "/srv/dvr/livestream/9f8e7d/9f8e7d.m3u8",
			want: "9f8e7d",
		},
		{
			name: "relative_path",
			path: "dvr/stream/abc/abc.m3u8",
			want: "abc",
		},
		{"empty", "", ""},
		// Only the rolling manifest (.m3u8) carries the dvr_hash; segments do not.
		{"wrong_extension_ts", "/srv/dvr/s/abc/abc.ts", ""},
		{"no_extension", "/srv/dvr/s/abc/abc", ""},
		// The directory and the file stem must mirror each other.
		{"stem_dir_mismatch", "/srv/dvr/s/abc/def.m3u8", ""},
		// A manifest sitting directly in a flat dir has parent != stem.
		{"flat_manifest", "/srv/index.m3u8", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DeriveDvrHashFromRollingManifestPath(tt.path); got != tt.want {
				t.Fatalf("DeriveDvrHashFromRollingManifestPath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}
