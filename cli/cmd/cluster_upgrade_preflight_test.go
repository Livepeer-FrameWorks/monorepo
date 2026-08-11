package cmd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"frameworks/cli/pkg/gitops"
	"frameworks/cli/pkg/inventory"
)

// ensurePlannedArtifactsResolvable must run the SAME resolution the provisioner runs at deploy — not a bare
// entry-presence check — so a manifest entry that exists but cannot produce a deployable image (missing digest) is
// caught BEFORE mutation, and every failing service is reported in one aggregated error.
func TestEnsurePlannedArtifactsResolvable_CatchesUnresolvableAndAggregates(t *testing.T) {
	// Pin registry selection so the check is deterministic regardless of the ambient FRAMEWORKS_IMAGE_REGISTRY.
	t.Setenv("FRAMEWORKS_IMAGE_REGISTRY", "dockerhub")

	gm := &gitops.Manifest{
		Services: []gitops.ServiceEntry{
			{Name: "good", Image: "repo/good", Digest: "sha256:aaa"},
			{Name: "nodigest", Image: "repo/nodigest"}, // entry present, but no digest → not deployable
		},
	}
	// Empty inventory maps: classificationDeployName returns each id verbatim, so id == deploy name.
	inv := &inventory.Manifest{}

	err := ensurePlannedArtifactsResolvable(context.Background(), gm, inv, []string{"good", "nodigest", "absent"}, nil)
	if err == nil {
		t.Fatal("expected an aggregated resolution error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "nodigest") {
		t.Errorf("error must name the digest-less service; got: %s", msg)
	}
	if !strings.Contains(msg, "absent") {
		t.Errorf("error must name the missing service; got: %s", msg)
	}
	if strings.Contains(msg, "good") {
		t.Errorf("the resolvable service must NOT appear in the error; got: %s", msg)
	}
}

// Native services must be validated by their BINARY artifacts, not a Docker image: every declared arch needs BOTH a
// download URL and a checksum (what the deploy-time native resolver requires), and an empty binary set is a hard failure.
func TestValidateNativeBinariesResolvable(t *testing.T) {
	ok := &gitops.ServiceInfo{Name: "quartermaster", Binaries: map[string]gitops.Artifact{
		"linux-amd64": {URL: "https://example/qm-amd64", Checksum: "sha256:aaa"},
		"linux-arm64": {URL: "https://example/qm-arm64", Checksum: "sha256:bbb"},
	}}
	if err := validateNativeBinariesResolvable(ok); err != nil {
		t.Fatalf("well-formed native binary set must pass, got %v", err)
	}

	missingURL := &gitops.ServiceInfo{Name: "quartermaster", Binaries: map[string]gitops.Artifact{
		"linux-amd64": {URL: "https://example/qm-amd64", Checksum: "sha256:aaa"},
		"linux-arm64": {URL: "", Checksum: "sha256:bbb"},
	}}
	if err := validateNativeBinariesResolvable(missingURL); err == nil || !strings.Contains(err.Error(), "linux-arm64 (url)") {
		t.Fatalf("a native arch with no URL must fail and name the arch; got %v", err)
	}

	// Deploy requires BOTH url and checksum, so a checksum-less binary must NOT pass preflight.
	missingChecksum := &gitops.ServiceInfo{Name: "quartermaster", Binaries: map[string]gitops.Artifact{
		"linux-amd64": {URL: "https://example/qm-amd64", Checksum: ""},
	}}
	if err := validateNativeBinariesResolvable(missingChecksum); err == nil || !strings.Contains(err.Error(), "linux-amd64 (checksum)") {
		t.Fatalf("a native arch with no checksum must fail and name the arch; got %v", err)
	}

	empty := &gitops.ServiceInfo{Name: "quartermaster"}
	if err := validateNativeBinariesResolvable(empty); err == nil || !strings.Contains(err.Error(), "no binary artifacts") {
		t.Fatalf("a native service with no binaries must fail closed; got %v", err)
	}
}

// The per-host check must reject a release missing a binary for the target host's detected architecture (e.g. an
// arm64-only release for an amd64 host) and must fail closed when a host's architecture cannot be detected. Detection
// is cached per host IP so a shared host is probed once.
func TestNativeHostArchProblems(t *testing.T) {
	svc := &gitops.ServiceInfo{Name: "foghorn", Binaries: map[string]gitops.Artifact{
		"linux-arm64": {URL: "https://example/foghorn-arm64", Checksum: "sha256:bbb"},
	}}
	hosts := []inventory.Host{{Name: "n1", ExternalIP: "10.0.0.1"}, {Name: "n2", ExternalIP: "10.0.0.2"}}

	// Both hosts are amd64; the release has only arm64 → both must be reported.
	calls := 0
	amd64 := func(ctx context.Context, h inventory.Host) (string, string, error) {
		calls++
		return "linux", "amd64", nil
	}
	probs := nativeHostArchProblems(context.Background(), svc, "foghorn", hosts, amd64, map[string]detectedArch{})
	if len(probs) != 2 {
		t.Fatalf("expected 2 per-host problems for an amd64 host with an arm64-only release, got %v", probs)
	}
	if !strings.Contains(probs[0], "linux-amd64") || !strings.Contains(probs[0], "10.0.0.1") {
		t.Errorf("problem must name the missing arch and host, got %q", probs[0])
	}

	// A resolvable host (arm64) yields no problem, and caching means one probe per distinct IP.
	calls = 0
	arm64 := func(ctx context.Context, h inventory.Host) (string, string, error) {
		calls++
		return "linux", "arm64", nil
	}
	if p := nativeHostArchProblems(context.Background(), svc, "foghorn", hosts, arm64, map[string]detectedArch{}); len(p) != 0 {
		t.Fatalf("arm64 hosts against an arm64 binary must pass, got %v", p)
	}
	if calls != 2 {
		t.Errorf("expected one probe per distinct host, got %d", calls)
	}

	// Detection failure fails closed.
	failing := func(ctx context.Context, h inventory.Host) (string, string, error) {
		return "", "", errors.New("ssh unreachable")
	}
	if p := nativeHostArchProblems(context.Background(), svc, "foghorn", hosts[:1], failing, map[string]detectedArch{}); len(p) != 1 || !strings.Contains(p[0], "cannot detect architecture") {
		t.Fatalf("a detection failure must be reported as a problem, got %v", p)
	}

	// A nil resolver skips the per-host check entirely.
	if p := nativeHostArchProblems(context.Background(), svc, "foghorn", hosts, nil, map[string]detectedArch{}); p != nil {
		t.Fatalf("nil resolver must skip per-host checks, got %v", p)
	}
}

// A binaries-only release entry with NO declared mode must FAIL preflight: buildTaskConfig defaults an omitted mode to
// docker, so deployment would look for an image and find none. Preflight must match that effective mode, not guess
// "native" from the presence of binaries.
func TestEnsurePlannedArtifactsResolvable_OmittedModeDefaultsToDocker(t *testing.T) {
	t.Setenv("FRAMEWORKS_IMAGE_REGISTRY", "dockerhub")
	gm := &gitops.Manifest{
		NativeBinaries: []gitops.NativeBinary{
			{Name: "binonly", Artifacts: []gitops.Artifact{{Arch: "linux-amd64", URL: "https://x/b", Checksum: "sha256:aaa"}}},
		},
	}
	err := ensurePlannedArtifactsResolvable(context.Background(), gm, &inventory.Manifest{}, []string{"binonly"}, nil)
	if err == nil || !strings.Contains(err.Error(), "binonly") {
		t.Fatalf("a binaries-only entry with omitted mode must fail preflight (deploy defaults to docker), got %v", err)
	}
}

func TestEnsurePlannedArtifactsResolvable_AllResolvable(t *testing.T) {
	t.Setenv("FRAMEWORKS_IMAGE_REGISTRY", "dockerhub")

	gm := &gitops.Manifest{
		Services: []gitops.ServiceEntry{
			{Name: "a", Image: "repo/a", Digest: "sha256:aaa"},
			{Name: "b", Image: "repo/b", Digest: "sha256:bbb"},
		},
	}
	if err := ensurePlannedArtifactsResolvable(context.Background(), gm, &inventory.Manifest{}, []string{"a", "b"}, nil); err != nil {
		t.Fatalf("all services resolve; expected nil, got %v", err)
	}
}
