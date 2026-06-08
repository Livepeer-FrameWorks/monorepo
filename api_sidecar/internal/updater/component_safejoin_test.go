package updater

import (
	"path/filepath"
	"testing"
)

// safeJoin is the zip-slip / path-traversal guard used while unpacking
// downloaded artifacts (extractTarGz / extractZip). It must resolve only to
// paths contained within the destination root and reject anything that escapes.
func TestSafeJoin(t *testing.T) {
	root := filepath.Clean("/opt/app")

	cases := []struct {
		name    string
		input   string
		want    string // expected path when wantErr is false
		wantErr bool
	}{
		{name: "nested file", input: "bin/app", want: filepath.Join(root, "bin/app")},
		{name: "clean-safe interior dotdot", input: "app/../app", want: filepath.Join(root, "app")},
		{name: "empty name resolves to root", input: "", want: root},
		{name: "dot resolves to root", input: ".", want: root},
		{name: "trailing slash", input: "sub/", want: filepath.Join(root, "sub")},
		{name: "leading slash is treated as relative", input: "/etc/passwd", want: filepath.Join(root, "etc/passwd")},
		{name: "escape with dotdot", input: "../../etc/passwd", wantErr: true},
		{name: "sibling-prefix trap", input: "../app-evil", wantErr: true},
		{name: "deep escape", input: "a/../../x", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := safeJoin(root, tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("safeJoin(%q, %q) = %q, want error", root, tc.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("safeJoin(%q, %q) unexpected error: %v", root, tc.input, err)
			}
			if got != tc.want {
				t.Fatalf("safeJoin(%q, %q) = %q, want %q", root, tc.input, got, tc.want)
			}
		})
	}
}
