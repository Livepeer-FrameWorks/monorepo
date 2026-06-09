package config

import (
	"os"
	"path/filepath"
	"testing"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// isStaleManagedWildcardStream gates drift-repair deletion: only managed
// wildcard *templates* (bare prefix or a `$`-bearing instance) may be deleted,
// never a concrete configured stream that happens to share a prefix.
func TestIsStaleManagedWildcardStream(t *testing.T) {
	cases := map[string]bool{
		"live+":                 true,
		"vod+":                  true,
		"processing+":           true,
		"pull+":                 true,
		"dvr+":                  true,
		"processing+$":          true,
		"live+$wildcard":        true,
		"dvr+$":                 true,
		"live+concrete-stream":  false,
		"vod+abc123":            false,
		"processing+artifact":   false,
		"live":                  false,
		"clip":                  false,
		"":                      false,
		"random+name":           false,
		"some$weird+notaprefix": false,
	}
	for name, want := range cases {
		if got := isStaleManagedWildcardStream(name); got != want {
			t.Errorf("isStaleManagedWildcardStream(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestJoin(t *testing.T) {
	cases := []struct {
		base, path, want string
	}{
		{"http://h:8008", "/api", "http://h:8008/api"},
		{"http://h:8008/", "/api", "http://h:8008/api"},
		{"http://h:8008", "api", "http://h:8008/api"},
		{"http://h:8008/", "api", "http://h:8008/api"},
		{"http://h:8008//", "api", "http://h:8008/api"},
		{"http://h:8008", "//api", "http://h:8008//api"},
	}
	for _, tc := range cases {
		if got := join(tc.base, tc.path); got != tc.want {
			t.Errorf("join(%q,%q) = %q, want %q", tc.base, tc.path, got, tc.want)
		}
	}
}

// hashSeed is the dedup key that decides whether a freshly received ConfigSeed
// is already applied. It must be deterministic, order-insensitive over template
// IDs, and sensitive to every field it folds in.
func TestHashSeed(t *testing.T) {
	base := &ipcpb.ConfigSeed{
		NodeId:       "node-1",
		Latitude:     52.1,
		Longitude:    4.3,
		LocationName: "ams",
		Templates: []*ipcpb.StreamTemplate{
			{Id: "t-b"}, {Id: "t-a"},
		},
	}

	h := hashSeed(base)
	if h == "" {
		t.Fatal("hashSeed returned empty for a populated seed")
	}
	if hashSeed(nil) != "" {
		t.Error("hashSeed(nil) must be empty")
	}

	// Determinism + template-order insensitivity.
	reordered := &ipcpb.ConfigSeed{
		NodeId: "node-1", Latitude: 52.1, Longitude: 4.3, LocationName: "ams",
		Templates: []*ipcpb.StreamTemplate{{Id: "t-a"}, {Id: "t-b"}},
	}
	if hashSeed(reordered) != h {
		t.Error("hashSeed must be insensitive to template ordering")
	}

	// Sensitivity: each field change must flip the hash.
	mutators := map[string]func(*ipcpb.ConfigSeed){
		"node":      func(s *ipcpb.ConfigSeed) { s.NodeId = "node-2" },
		"latitude":  func(s *ipcpb.ConfigSeed) { s.Latitude = 0 },
		"longitude": func(s *ipcpb.ConfigSeed) { s.Longitude = 0 },
		"location":  func(s *ipcpb.ConfigSeed) { s.LocationName = "rtm" },
		"templates": func(s *ipcpb.ConfigSeed) { s.Templates = append(s.Templates, &ipcpb.StreamTemplate{Id: "t-c"}) },
	}
	for field, mutate := range mutators {
		clone := &ipcpb.ConfigSeed{
			NodeId: "node-1", Latitude: 52.1, Longitude: 4.3, LocationName: "ams",
			Templates: []*ipcpb.StreamTemplate{{Id: "t-a"}, {Id: "t-b"}},
		}
		mutate(clone)
		if hashSeed(clone) == h {
			t.Errorf("changing %s must change the seed hash", field)
		}
	}
}

func TestProtocolValuesEqual(t *testing.T) {
	cases := []struct {
		name             string
		existing, desire any
		want             bool
	}{
		{"string slices equal", []string{"a", "b"}, []string{"a", "b"}, true},
		{"string slices differ", []string{"a", "b"}, []string{"a", "c"}, false},
		{"any slice coerces equal", []any{"a", "b"}, []string{"a", "b"}, true},
		{"any slice length mismatch", []any{"a"}, []string{"a", "b"}, false},
		{"any slice element mismatch", []any{"a", "x"}, []string{"a", "b"}, false},
		{"desired not a string slice falls back to DeepEqual", 5, 5, true},
		{"existing wrong type", 5, []string{"a"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := protocolValuesEqual(tc.existing, tc.desire); got != tc.want {
				t.Errorf("protocolValuesEqual(%#v,%#v) = %v, want %v", tc.existing, tc.desire, got, tc.want)
			}
		})
	}
}

func TestHostnameFromPublicURL(t *testing.T) {
	cases := map[string]string{
		"http://localhost:18090/view":          "localhost",
		"https://edge-egress.example.com/view": "edge-egress.example.com",
		"//localhost:18090/view":               "localhost",
		"localhost:18090/view":                 "localhost",
		"edge.example.com":                     "edge.example.com",
		"":                                     "",
		"   ":                                  "",
		"https://[2001:db8::1]:443/view":       "2001:db8::1",
	}
	for in, want := range cases {
		if got := hostnameFromPublicURL(in); got != want {
			t.Errorf("hostnameFromPublicURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// atomicWriteFile underpins TLS-bundle and config writes: content + mode must
// land atomically and the temp file must not linger on success.
func TestAtomicWriteFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bundle.pem")

	if err := atomicWriteFile(path, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, path, "first", 0o600)

	// Overwrite in place with a different mode.
	if err := atomicWriteFile(path, []byte("second"), 0o644); err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, path, "second", 0o644)

	// No stray temp files left behind on success.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "bundle.pem" {
		t.Errorf("expected only the target file, got %v", entries)
	}
}

func assertFileContent(t *testing.T, path, want string, mode os.FileMode) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("content = %q, want %q", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != mode {
		t.Errorf("mode = %v, want %v", info.Mode().Perm(), mode)
	}
}
