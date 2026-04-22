package compare

import (
	"context"
	"errors"
	"slices"
	"testing"

	"frameworks/cli/pkg/artifacts"
)

type stubRunner struct {
	content map[string][]byte
	missing map[string]bool
	errs    map[string]error
}

func (s *stubRunner) Fetch(_ context.Context, path string) ([]byte, bool, error) {
	if err, ok := s.errs[path]; ok {
		return nil, false, err
	}
	if s.missing[path] {
		return nil, true, nil
	}
	return s.content[path], false, nil
}

func TestCompareTarget_fileHashMatchAndDiffer(t *testing.T) {
	t.Parallel()
	runner := &stubRunner{
		content: map[string][]byte{
			"/a": []byte("same"),
			"/b": []byte("observed-differs"),
		},
	}
	desired := []artifacts.DesiredArtifact{
		{Path: "/a", Kind: artifacts.KindFileHash, Content: []byte("same")},
		{Path: "/b", Kind: artifacts.KindFileHash, Content: []byte("desired-differs")},
	}
	got := CompareTarget(context.Background(), artifacts.TargetID{Host: "h"}, desired, runner)
	if len(got.Entries) != 2 {
		t.Fatalf("want 2 entries, got %d", len(got.Entries))
	}
	if got.Entries[0].Status != artifacts.StatusMatch {
		t.Errorf("/a: want match, got %v", got.Entries[0].Status)
	}
	if got.Entries[1].Status != artifacts.StatusDiffer {
		t.Errorf("/b: want differ, got %v", got.Entries[1].Status)
	}
	if got.Divergences() != 1 {
		t.Errorf("want 1 divergence, got %d", got.Divergences())
	}
}

func TestCompareTarget_missingOnHost(t *testing.T) {
	t.Parallel()
	runner := &stubRunner{missing: map[string]bool{"/x": true}}
	desired := []artifacts.DesiredArtifact{{Path: "/x", Kind: artifacts.KindFileHash, Content: []byte("anything")}}
	got := CompareTarget(context.Background(), artifacts.TargetID{}, desired, runner)
	if got.Entries[0].Status != artifacts.StatusMissingOnHost {
		t.Errorf("want missing_on_host, got %v", got.Entries[0].Status)
	}
}

func TestCompareTarget_probeErrorDistinctFromMissing(t *testing.T) {
	t.Parallel()
	runner := &stubRunner{errs: map[string]error{"/y": errors.New("permission denied")}}
	desired := []artifacts.DesiredArtifact{{Path: "/y", Kind: artifacts.KindFileHash, Content: []byte("x")}}
	got := CompareTarget(context.Background(), artifacts.TargetID{}, desired, runner)
	if got.Entries[0].Status != artifacts.StatusProbeError {
		t.Errorf("want probe_error, got %v", got.Entries[0].Status)
	}
	if got.Entries[0].Detail != "permission denied" {
		t.Errorf("detail: want 'permission denied', got %q", got.Entries[0].Detail)
	}
}

func TestCompareTarget_envKindAddedRemovedChanged(t *testing.T) {
	t.Parallel()
	runner := &stubRunner{
		content: map[string][]byte{
			"/e": []byte("SAME=1\nCHANGED=old\nEXTRA=x\n"),
		},
	}
	desired := []artifacts.DesiredArtifact{{
		Path:    "/e",
		Kind:    artifacts.KindEnv,
		Content: []byte("SAME=1\nCHANGED=new\nNEW_KEY=y\n"),
	}}
	got := CompareTarget(context.Background(), artifacts.TargetID{}, desired, runner)
	entry := got.Entries[0]
	if entry.Status != artifacts.StatusDiffer {
		t.Fatalf("want differ, got %v", entry.Status)
	}
	if entry.Env == nil {
		t.Fatalf("want EnvDiff populated")
	}
	if !slices.Equal(entry.Env.Added, []string{"NEW_KEY"}) {
		t.Errorf("Added: want [NEW_KEY], got %v", entry.Env.Added)
	}
	if !slices.Equal(entry.Env.Removed, []string{"EXTRA"}) {
		t.Errorf("Removed: want [EXTRA], got %v", entry.Env.Removed)
	}
	if !slices.Equal(entry.Env.Changed, []string{"CHANGED"}) {
		t.Errorf("Changed: want [CHANGED], got %v", entry.Env.Changed)
	}
}

func TestCompareTarget_envMatchWhenKeysAndValuesAlign(t *testing.T) {
	t.Parallel()
	runner := &stubRunner{content: map[string][]byte{"/e": []byte("A=1\nB=2\n")}}
	desired := []artifacts.DesiredArtifact{{
		Path: "/e", Kind: artifacts.KindEnv,
		// Intentionally a different serialization (with comment header
		// and different order) — parser reconciles.
		Content: []byte("# Environment for foo\nSERVICE_NAME=foo\nB=2\nA=1\n"),
	}}
	got := CompareTarget(context.Background(), artifacts.TargetID{}, desired, runner)
	// The SERVICE_NAME key is added-in-desired since host doesn't have it.
	if got.Entries[0].Env == nil || len(got.Entries[0].Env.Added) != 1 || got.Entries[0].Env.Added[0] != "SERVICE_NAME" {
		t.Errorf("expected SERVICE_NAME as Added; got %+v", got.Entries[0].Env)
	}
}

func TestCompareTarget_envIgnoreKeysSuppressesRuntimeInjectedDrift(t *testing.T) {
	t.Parallel()
	// The host has three runtime-injected keys the manifest doesn't know
	// about (ENROLLMENT_TOKEN, UPSTREAM_DNS, SERVICE_TOKEN). Declaring the
	// first two as IgnoreKeys must leave only manifest-backed keys in the
	// compared set. SERVICE_TOKEN stays — it's manifest-backed for this
	// service and not in IgnoreKeys.
	observed := []byte("MANIFEST_KEY=a\nENROLLMENT_TOKEN=abc\nUPSTREAM_DNS=1.1.1.1\nSERVICE_TOKEN=tok\n")
	desired := []byte("MANIFEST_KEY=a\nSERVICE_TOKEN=tok\n")
	runner := &stubRunner{content: map[string][]byte{"/e": observed}}
	arts := []artifacts.DesiredArtifact{{
		Path:       "/e",
		Kind:       artifacts.KindEnv,
		Content:    desired,
		IgnoreKeys: []string{"ENROLLMENT_TOKEN", "UPSTREAM_DNS"},
	}}
	got := CompareTarget(context.Background(), artifacts.TargetID{}, arts, runner)
	if got.Entries[0].Status != artifacts.StatusMatch {
		t.Errorf("want match after IgnoreKeys filtering, got %v (env=%+v)",
			got.Entries[0].Status, got.Entries[0].Env)
	}
}

func TestCompareTarget_ignoreKeysDoesNotMaskRealDrift(t *testing.T) {
	t.Parallel()
	// If an ignored key is the only difference, match. If a non-ignored
	// key also drifts, report it.
	observed := []byte("MANIFEST_KEY=old\nENROLLMENT_TOKEN=abc\n")
	desired := []byte("MANIFEST_KEY=new\n")
	runner := &stubRunner{content: map[string][]byte{"/e": observed}}
	arts := []artifacts.DesiredArtifact{{
		Path:       "/e",
		Kind:       artifacts.KindEnv,
		Content:    desired,
		IgnoreKeys: []string{"ENROLLMENT_TOKEN"},
	}}
	got := CompareTarget(context.Background(), artifacts.TargetID{}, arts, runner)
	if got.Entries[0].Status != artifacts.StatusDiffer {
		t.Fatalf("want differ on MANIFEST_KEY, got %v", got.Entries[0].Status)
	}
	if got.Entries[0].Env == nil || !slices.Equal(got.Entries[0].Env.Changed, []string{"MANIFEST_KEY"}) {
		t.Errorf("Changed: want [MANIFEST_KEY], got %+v", got.Entries[0].Env)
	}
}

func TestCompareTarget_managedInvariantMatchAndMissing(t *testing.T) {
	t.Parallel()
	runner := &stubRunner{
		content: map[string][]byte{
			"/caddyfile":         []byte("# system caddy\nsome config\nimport /etc/caddy/frameworks.d/*\n"),
			"/caddyfile-missing": []byte("# system caddy\nno import here\n"),
		},
	}
	desired := []artifacts.DesiredArtifact{
		{
			Path: "/caddyfile", Kind: artifacts.KindManagedInvariant,
			Invariant: &artifacts.Invariant{
				MustContain: [][]byte{[]byte("import /etc/caddy/frameworks.d/*")},
			},
		},
		{
			Path: "/caddyfile-missing", Kind: artifacts.KindManagedInvariant,
			Invariant: &artifacts.Invariant{
				MustContain: [][]byte{[]byte("import /etc/caddy/frameworks.d/*")},
			},
		},
	}
	got := CompareTarget(context.Background(), artifacts.TargetID{}, desired, runner)
	if got.Entries[0].Status != artifacts.StatusMatch {
		t.Errorf("caddyfile present: want match, got %v", got.Entries[0].Status)
	}
	if got.Entries[1].Status != artifacts.StatusDiffer {
		t.Errorf("caddyfile absent invariant: want differ, got %v", got.Entries[1].Status)
	}
	if got.Entries[1].Detail == "" {
		t.Errorf("caddyfile absent invariant: want Detail populated")
	}
}

func TestCompareTarget_onePathFailureDoesNotAffectOthers(t *testing.T) {
	t.Parallel()
	runner := &stubRunner{
		content: map[string][]byte{"/good": []byte("x")},
		errs:    map[string]error{"/bad": errors.New("transport")},
	}
	desired := []artifacts.DesiredArtifact{
		{Path: "/good", Kind: artifacts.KindFileHash, Content: []byte("x")},
		{Path: "/bad", Kind: artifacts.KindFileHash, Content: []byte("x")},
	}
	got := CompareTarget(context.Background(), artifacts.TargetID{}, desired, runner)
	if got.Entries[0].Status != artifacts.StatusMatch {
		t.Errorf("/good: want match despite /bad failure, got %v", got.Entries[0].Status)
	}
	if got.Entries[1].Status != artifacts.StatusProbeError {
		t.Errorf("/bad: want probe_error, got %v", got.Entries[1].Status)
	}
}
