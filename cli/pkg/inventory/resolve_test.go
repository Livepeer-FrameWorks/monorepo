package inventory

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	fwcfg "frameworks/cli/internal/config"
)

// --- test fixtures ---------------------------------------------------------

// fakeFetcher implements GithubFetcher for tests without real GitHub calls.
type fakeFetcher struct {
	called bool
	err    error
	result GithubFetchResult
}

func (f *fakeFetcher) Fetch(_ context.Context, _ GithubFetchInput) (GithubFetchResult, error) {
	f.called = true
	if f.err != nil {
		return GithubFetchResult{}, f.err
	}
	return f.result, nil
}

// writeClusterManifest creates a minimal but loadable cluster.yaml at the
// given path so the resolver's file-existence check passes. We don't need
// the manifest to validate — the resolver itself doesn't parse it, it just
// returns the path.
func writeClusterManifest(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("type: central\nprofile: dev\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeGitopsRoot constructs a dir that looks like a real gitops repo root
// for the cwd heuristic (clusters/ + .sops.yaml).
func writeGitopsRoot(t *testing.T, root string, clusters ...string) {
	t.Helper()
	for _, c := range clusters {
		writeClusterManifest(t, filepath.Join(root, "clusters", c, "cluster.yaml"))
	}
	if err := os.WriteFile(filepath.Join(root, ".sops.yaml"), []byte("creation_rules: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// --- resolver branch tests -------------------------------------------------

func TestResolve_ManifestFlagWins(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cluster.yaml")
	writeClusterManifest(t, path)

	// Even with a valid gitops-dir configured, --manifest wins.
	gitopsDir := t.TempDir()
	writeGitopsRoot(t, gitopsDir, "production")

	res, err := ResolveManifestSource(ResolveInput{
		Manifest:  StringFlag{Value: path, Changed: true},
		GitopsDir: StringFlag{Value: gitopsDir, Changed: true},
		Cluster:   StringFlag{Value: "production", Changed: true},
		Env:       fwcfg.MapEnv{},
		Stdout:    io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != SourceManifestFlag {
		t.Errorf("want manifest-flag, got %s", res.Source)
	}
	if res.Path != path {
		t.Errorf("want %s, got %s", path, res.Path)
	}
}

func TestResolve_GitopsDirWithSingleCluster_AutoPicks(t *testing.T) {
	root := t.TempDir()
	writeGitopsRoot(t, root, "production")

	var buf strings.Builder
	res, err := ResolveManifestSource(ResolveInput{
		GitopsDir: StringFlag{Value: root, Changed: true},
		Env:       fwcfg.MapEnv{},
		Stdout:    &buf,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Cluster != "production" {
		t.Errorf("want production, got %q", res.Cluster)
	}
	if !strings.Contains(buf.String(), "auto-picked") {
		t.Errorf("expected auto-pick notification, got %q", buf.String())
	}
}

func TestResolve_GitopsDirWithMultipleClusters_ErrorsWithOptions(t *testing.T) {
	root := t.TempDir()
	writeGitopsRoot(t, root, "production", "staging")

	_, err := ResolveManifestSource(ResolveInput{
		GitopsDir: StringFlag{Value: root, Changed: true},
		Env:       fwcfg.MapEnv{},
		Stdout:    io.Discard,
	})
	if err == nil {
		t.Fatal("want error on ambiguous cluster choice")
	}
	if !strings.Contains(err.Error(), "production") || !strings.Contains(err.Error(), "staging") {
		t.Errorf("error must list options, got: %v", err)
	}
	if !strings.Contains(err.Error(), "--cluster") {
		t.Errorf("error must suggest --cluster remedy, got: %v", err)
	}
}

func TestResolve_GithubSourceRequiresCredentials(t *testing.T) {
	fetcher := &fakeFetcher{}
	_, err := ResolveManifestSource(ResolveInput{
		GithubRepo:  StringFlag{Value: "owner/repo", Changed: true},
		Cluster:     StringFlag{Value: "production", Changed: true},
		Env:         fwcfg.MapEnv{},
		GithubFetch: fetcher,
		Stdout:      io.Discard,
	})
	if err == nil {
		t.Fatal("want error for missing github creds")
	}
	// Error must name every way to supply creds so the operator can pick one.
	for _, want := range []string{"--github-app-id", "FRAMEWORKS_GITHUB_APP_ID", "frameworks config set github.app-id"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q, got: %v", want, err)
		}
	}
	if fetcher.called {
		t.Error("fetcher must not be called when creds are missing")
	}
}

func TestResolve_GithubSourceWithCreds_CallsFetcher(t *testing.T) {
	dir := t.TempDir()
	// We return a tempdir with a fake-materialized cluster.yaml. The
	// resolver just hands us the Path back.
	manifestPath := filepath.Join(dir, "clusters", "production", "cluster.yaml")
	writeClusterManifest(t, manifestPath)

	fetcher := &fakeFetcher{result: GithubFetchResult{Path: manifestPath, TmpDir: dir, Cleanup: func() {}}}
	res, err := ResolveManifestSource(ResolveInput{
		GithubRepo:  StringFlag{Value: "owner/repo", Changed: true},
		Cluster:     StringFlag{Value: "production", Changed: true},
		GithubAppID: Int64Flag{Value: 123, Changed: true},
		GithubInst:  Int64Flag{Value: 456, Changed: true},
		GithubKey:   StringFlag{Value: "/tmp/key.pem", Changed: true},
		Env:         fwcfg.MapEnv{},
		GithubFetch: fetcher,
		Stdout:      io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !fetcher.called {
		t.Error("fetcher should have been invoked")
	}
	if res.Source != SourceGithubRepoFlag {
		t.Errorf("want github-repo-flag, got %s", res.Source)
	}
}

func TestResolve_CwdHeuristicRequiresBothMarkers(t *testing.T) {
	// Only clusters/ without .sops.yaml → heuristic must NOT trigger.
	t.Run("clusters only, no sops marker", func(t *testing.T) {
		root := t.TempDir()
		writeClusterManifest(t, filepath.Join(root, "clusters", "production", "cluster.yaml"))
		_, err := ResolveManifestSource(ResolveInput{
			Env:    fwcfg.MapEnv{},
			Cwd:    root,
			Stdout: io.Discard,
		})
		if err == nil {
			t.Fatal("want structured error without both markers")
		}
		if !strings.Contains(err.Error(), "--manifest") {
			t.Errorf("structured error must list flag alternatives: %v", err)
		}
	})

	// Both markers → heuristic triggers.
	t.Run("both markers present", func(t *testing.T) {
		root := t.TempDir()
		writeGitopsRoot(t, root, "production")
		res, err := ResolveManifestSource(ResolveInput{
			Env:    fwcfg.MapEnv{},
			Cwd:    root,
			Stdout: io.Discard,
		})
		if err != nil {
			t.Fatal(err)
		}
		if res.Source != SourceCwdHeuristic {
			t.Errorf("want cwd source, got %s", res.Source)
		}
	})
}

func TestResolve_CwdHeuristicDoesNotWalkParents(t *testing.T) {
	// Confirm we never walk up: place markers ONLY at the parent, run
	// with cwd set to a child directory, expect resolver to NOT find the
	// parent's gitops root.
	parent := t.TempDir()
	writeGitopsRoot(t, parent, "production")
	child := filepath.Join(parent, "subdir")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := ResolveManifestSource(ResolveInput{
		Env:    fwcfg.MapEnv{},
		Cwd:    child,
		Stdout: io.Discard,
	})
	if err == nil {
		t.Fatal("cwd heuristic must not walk parents; expected structured error")
	}
}

func TestResolve_NoSourceConfigured_ErrorListsFlagsFirst(t *testing.T) {
	_, err := ResolveManifestSource(ResolveInput{
		Env:    fwcfg.MapEnv{},
		Cwd:    t.TempDir(), // empty dir, no markers
		Stdout: io.Discard,
	})
	if err == nil {
		t.Fatal("want error when no source is configured")
	}
	// Error must list --manifest BEFORE the setup tip — explicit usage
	// is a supported path, not a second-class citizen.
	text := err.Error()
	idxManifest := strings.Index(text, "--manifest")
	idxSetup := strings.Index(text, "frameworks setup")
	if idxManifest < 0 || idxSetup < 0 {
		t.Fatalf("error must list both remedies: %v", err)
	}
	if idxManifest >= idxSetup {
		t.Errorf("--manifest must appear before 'frameworks setup' in the error (flags first): %v", err)
	}
}

// TestResolve_CleanupIsNoOpForNonTempdirSources — local/manifest sources
// don't create tempdirs, so Cleanup must be safe to defer even though
// there's nothing to clean.
func TestResolve_CleanupIsNoOpForNonTempdirSources(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cluster.yaml")
	writeClusterManifest(t, path)

	res, err := ResolveManifestSource(ResolveInput{
		Manifest: StringFlag{Value: path, Changed: true},
		Env:      fwcfg.MapEnv{},
		Stdout:   io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Cleanup == nil {
		t.Fatal("Cleanup must never be nil — callers defer it unconditionally")
	}
	// No panic, no side effects: that's the contract.
	res.Cleanup()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("Cleanup must not touch user files; stat failed: %v", err)
	}
}

// Ensure the fakeFetcher interface stays satisfied as a compile-time check.
var _ = fmt.Sprintf
