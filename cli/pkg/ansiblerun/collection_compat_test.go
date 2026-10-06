package ansiblerun

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeGalaxy writes an ansible-galaxy stand-in that reports ansible-core <core> and, on a
// collection install, lays down one collection declaring requires_ansible <spec>.
func fakeGalaxy(t *testing.T, dir, core, spec string) string {
	t.Helper()
	binary := filepath.Join(dir, "ansible-galaxy")
	script := `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "ansible-galaxy [core ` + core + `]"
  exit 0
fi
if [ "$1" = "collection" ]; then
  while [ $# -gt 0 ]; do
    if [ "$1" = "-p" ]; then dest="$2"; fi
    shift
  done
  c="$dest/ansible_collections/prometheus/prometheus"
  mkdir -p "$c/meta"
  printf 'requires_ansible: "` + spec + `"\n' > "$c/meta/runtime.yml"
  printf '{"collection_info": {"namespace": "prometheus", "name": "prometheus", "version": "0.27.0"}}' > "$c/MANIFEST.json"
fi
exit 0
`
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return binary
}

// A collection declares the ansible-core releases it runs on. An operator's ansible-core outside
// that range fails mid-provision inside the collection's tasks (prometheus.prometheus 0.27.0's
// string conditional under ansible-core 2.19+), so the CLI refuses before running anything and
// names the collection and the range it supports.
func TestEnsureRefusesAnsibleCoreOutsideACollectionsSupportedRange(t *testing.T) {
	for _, tc := range []struct {
		core    string
		refused bool
	}{
		{core: "2.18.7", refused: false},
		{core: "2.12.0", refused: false},
		{core: "2.19.0", refused: true},
		{core: "2.21.4", refused: true},
		{core: "2.11.9", refused: true},
	} {
		t.Run(tc.core, func(t *testing.T) {
			dir := t.TempDir()
			requirements := filepath.Join(dir, "requirements.yml")
			if err := os.WriteFile(requirements, []byte("collections: []\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			e := &CollectionEnsurer{
				RequirementsFile: requirements,
				CacheDir:         filepath.Join(dir, "cache"),
				Binary:           fakeGalaxy(t, dir, tc.core, ">=2.12.0,<=2.18.99"),
			}
			_, err := e.Ensure(context.Background())
			if !tc.refused {
				if err != nil {
					t.Fatalf("ansible-core %s is inside the supported range but was refused: %v", tc.core, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ansible-core %s is outside prometheus.prometheus's supported range >=2.12.0,<=2.18.99 but provisioning proceeded", tc.core)
			}
			for _, want := range []string{tc.core, "prometheus.prometheus 0.27.0", ">=2.12.0,<=2.18.99"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("refusal must name %q: %v", want, err)
				}
			}

			// A cache populated earlier is checked too: upgrading ansible-core after the install
			// must not slip past on the fresh-cache path.
			if _, again := e.Ensure(context.Background()); again == nil {
				t.Fatalf("fresh cache let ansible-core %s through", tc.core)
			}
		})
	}
}

func TestRequiresAnsibleSpecParsing(t *testing.T) {
	for _, tc := range []struct {
		version, spec string
		ok            bool
	}{
		{"2.21.4", ">=2.14.0,<=2.21.99", true},
		{"2.22.0", ">=2.14.0,<=2.21.99", false},
		{"2.9.10", ">=2.9.10", true},
		{"2.17.14", ">=2.9", true},
		{"2.20.0rc1", ">=2.20.0", true},
		{"2.15.0", ">2.15.0", false},
		{"2.15.0", "<2.16", true},
		{"2.15.0", "!=2.15.0", false},
		{"2.15.0", "==2.15.0", true},
	} {
		got, err := satisfiesRequiresAnsible(tc.version, tc.spec)
		if err != nil {
			t.Fatalf("satisfiesRequiresAnsible(%q, %q): %v", tc.version, tc.spec, err)
		}
		if got != tc.ok {
			t.Errorf("satisfiesRequiresAnsible(%q, %q) = %v, want %v", tc.version, tc.spec, got, tc.ok)
		}
	}
	if _, err := satisfiesRequiresAnsible("2.15.0", "~=2.15"); err == nil {
		t.Error("an operator the CLI cannot evaluate must be an error, not a pass")
	}
}
