package runtimeassets

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
)

func monorepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// cli/internal/runtimeassets/runtimeassets_test.go -> repo root
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

func testResolver(t *testing.T, ansible, skipper fs.FS, ver, cwd string) *resolver {
	t.Helper()
	cache := t.TempDir()
	return &resolver{
		ansible:   ansible,
		skipper:   skipper,
		version:   ver,
		cacheRoot: func() (string, error) { return cache, nil },
		getwd:     func() (string, error) { return cwd, nil },
		getenv:    func(string) string { return "" },
	}
}

func miniAnsible() fstest.MapFS {
	return fstest.MapFS{
		"ansible.cfg":      {Data: []byte("[defaults]\n")},
		"requirements.yml": {Data: []byte("---\ncollections: []\n")},
		"playbooks/hello.yml": {
			Data: []byte("- hosts: all\n"),
		},
		"collections/ansible_collections/frameworks/infra/galaxy.yml": {Data: []byte("namespace: frameworks\n")},
	}
}

func TestReleaseVersionClassification(t *testing.T) {
	cases := map[string]bool{
		"v0.3.10":                   true,
		"v0.3.11-rc3":               true,
		"dev":                       false,
		"0.0.0-dev":                 false,
		"v0.3.11-rc3-5-gabc1234":    false,
		"v0.3.10-2-gdeadbee-dirty":  false,
		"v0.3.10-dirty":             false,
		"b5d2efdce":                 false,
		"v0.3.11-rc3-dirty-garbage": false,
	}
	for v, want := range cases {
		if got := (&resolver{version: v}).isRelease(); got != want {
			t.Errorf("isRelease(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestExtractIsVersionedAndReused(t *testing.T) {
	r := testResolver(t, miniAnsible(), nil, "v1.2.3", t.TempDir())
	first, err := r.ansibleRoot()
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	if !strings.HasPrefix(filepath.Base(first), "v1.2.3-") {
		t.Fatalf("cache dir %q not keyed by version", first)
	}
	cfg, err := os.ReadFile(filepath.Join(first, "ansible.cfg"))
	if err != nil || string(cfg) != "[defaults]\n" {
		t.Fatalf("ansible.cfg = %q, %v", cfg, err)
	}
	if _, statErr := os.Stat(filepath.Join(first, "collections/ansible_collections/frameworks/infra/galaxy.yml")); statErr != nil {
		t.Fatalf("collection not extracted: %v", statErr)
	}

	// A marker file proves the second call reuses the directory rather than
	// re-extracting over it.
	marker := filepath.Join(first, "reuse-marker")
	if writeErr := os.WriteFile(marker, nil, 0o644); writeErr != nil {
		t.Fatal(writeErr)
	}
	second, err := r.ansibleRoot()
	if err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	if second != first {
		t.Fatalf("second resolve = %q, want reuse of %q", second, first)
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatalf("cache dir was rewritten: %v", statErr)
	}
	entries, err := os.ReadDir(filepath.Dir(first))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected only the final dir under cache parent, got %d entries", len(entries))
	}
}

func TestExtractKeyChangesWithContent(t *testing.T) {
	cache := t.TempDir()
	mk := func(fsys fs.FS) string {
		r := &resolver{
			ansible: fsys, version: "v1.2.3",
			cacheRoot: func() (string, error) { return cache, nil },
			getwd:     os.Getwd, getenv: func(string) string { return "" },
		}
		root, err := r.ansibleRoot()
		if err != nil {
			t.Fatal(err)
		}
		return root
	}
	a := mk(miniAnsible())
	changed := miniAnsible()
	changed["playbooks/hello.yml"] = &fstest.MapFile{Data: []byte("- hosts: none\n")}
	b := mk(changed)
	if a == b {
		t.Fatalf("different embedded content resolved to the same cache dir %q", a)
	}
}

func TestReleaseBuildIgnoresCheckoutInWorkingDirectory(t *testing.T) {
	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, "ansible"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "ansible", "ansible.cfg"), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	release := testResolver(t, miniAnsible(), nil, "v1.2.3", cwd)
	root, err := release.ansibleRoot()
	if err != nil {
		t.Fatal(err)
	}
	if root == filepath.Join(cwd, "ansible") {
		t.Fatal("release build resolved the working-directory checkout instead of its embedded tree")
	}

	dev := testResolver(t, miniAnsible(), nil, "dev", cwd)
	root, err = dev.ansibleRoot()
	if err != nil {
		t.Fatal(err)
	}
	if root != filepath.Join(cwd, "ansible") {
		t.Fatalf("dev build resolved %q, want working-directory checkout", root)
	}
}

func TestEnvOverrideWins(t *testing.T) {
	override := t.TempDir()
	if err := os.WriteFile(filepath.Join(override, "ansible.cfg"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	r := testResolver(t, miniAnsible(), nil, "v1.2.3", t.TempDir())
	r.getenv = func(k string) string {
		if k == AnsibleRootEnv {
			return override
		}
		return ""
	}
	root, err := r.ansibleRoot()
	if err != nil {
		t.Fatal(err)
	}
	if root != override {
		t.Fatalf("root = %q, want override %q", root, override)
	}
}

func TestMissingEmbeddedContentFailsClearly(t *testing.T) {
	r := testResolver(t, fstest.MapFS{}, fstest.MapFS{}, "v1.2.3", t.TempDir())
	if _, err := r.ansibleRoot(); err == nil || !strings.Contains(err.Error(), "no embedded Ansible content") {
		t.Fatalf("ansibleRoot err = %v", err)
	}
	if _, err := r.skipperSources(); err == nil || !strings.Contains(err.Error(), "no embedded Skipper sources") {
		t.Fatalf("skipperSources err = %v", err)
	}
}

// TestResolvesFromEmptyWorkingDirectory runs a release build from a directory
// with no monorepo above it, backed by the real ansible/ and config/skipper
// trees, and checks the extracted root carries everything the provisioners
// load.
func TestResolvesFromEmptyWorkingDirectory(t *testing.T) {
	repo := monorepoRoot(t)
	empty := t.TempDir()
	t.Chdir(empty)

	r := testResolver(t, stagedAnsibleFS(os.DirFS(filepath.Join(repo, "ansible"))), os.DirFS(filepath.Join(repo, "config", "skipper")), "v9.9.9", empty)
	r.getwd = os.Getwd
	assertRuntimeContent(t, r)
}

// TestEmbeddedBundle checks the tree actually compiled into this binary once
// scripts/cli-embed-assets.sh has staged it (make test-cli always does).
func TestEmbeddedBundle(t *testing.T) {
	base := defaultRes()
	if !embeddedHas(base.ansible, "ansible.cfg") {
		t.Skip("bundle not staged; run scripts/cli-embed-assets.sh (make test-cli does)")
	}
	empty := t.TempDir()
	t.Chdir(empty)
	r := testResolver(t, base.ansible, base.skipper, "v9.9.9", empty)
	r.getwd = os.Getwd
	assertRuntimeContent(t, r)
	if _, err := fs.Stat(base.ansible, "collections/ansible_collections/frameworks/infra/molecule"); err == nil {
		t.Fatal("molecule scenarios should not be embedded")
	}
}

func assertRuntimeContent(t *testing.T, r *resolver) {
	t.Helper()
	root, err := r.ansibleRoot()
	if err != nil {
		t.Fatalf("ansibleRoot: %v", err)
	}
	for _, rel := range []string{
		"ansible.cfg",
		"requirements.yml",
		"playbooks/go_service.yml",
		"playbooks/edge.yml",
		"collections/ansible_collections/frameworks/infra/galaxy.yml",
		"collections/ansible_collections/frameworks/infra/roles/go_service/tasks/main.yml",
	} {
		if _, statErr := os.Stat(filepath.Join(root, rel)); statErr != nil {
			t.Errorf("extracted root missing %s: %v", rel, statErr)
		}
	}

	skipper, err := r.skipperSources()
	if err != nil {
		t.Fatalf("skipperSources: %v", err)
	}
	files, err := readSkipperTree(skipper)
	if err != nil {
		t.Fatalf("readSkipperTree: %v", err)
	}
	seen := map[string]bool{}
	for _, f := range files {
		seen[f.Path] = true
	}
	for _, want := range []string{"sitemaps/frameworks.txt", "faq/protocol-selection.md"} {
		if !seen[want] {
			t.Errorf("skipper sources missing %s (have %d files)", want, len(files))
		}
	}
}

// stagedAnsibleFS mirrors what scripts/cli-embed-assets.sh stages from
// ansible/: everything the source tree has minus molecule scenarios and the
// local .cache.
func stagedAnsibleFS(src fs.FS) fs.FS {
	out := fstest.MapFS{}
	_ = fs.WalkDir(src, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch {
			case d.Name() == "molecule", p == ".cache", p == ".ansible", p == "inventory":
				return fs.SkipDir
			}
			return nil
		}
		keep := p == "ansible.cfg" || p == "requirements.yml" || strings.HasPrefix(p, "playbooks/") ||
			strings.HasPrefix(p, "collections/ansible_collections/frameworks/")
		if !keep {
			return nil
		}
		data, readErr := fs.ReadFile(src, p)
		if readErr != nil {
			return readErr
		}
		out[p] = &fstest.MapFile{Data: data}
		return nil
	})
	return out
}
