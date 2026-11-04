package cmd

import (
	serviceexec "frameworks/cli/pkg/exec"
	"frameworks/cli/pkg/gitops"
	"strings"
	"testing"
)

func TestNativePreflightUsesVerifiedIsolatedTarget(t *testing.T) {
	if _, err := nativeDataPreflightScript(gitops.Artifact{URL: "http://example.test/binary", Checksum: "sha256:bad"}, serviceexec.SpecFromDetection("native", nil, "purser")); err == nil {
		t.Fatal("accepted an unverified artifact")
	}
	script, err := nativeDataPreflightScript(gitops.Artifact{URL: "https://example.test/purser.tar.gz", Checksum: "sha256:" + strings.Repeat("a", 64)}, serviceexec.SpecFromDetection("native", nil, "purser"))
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"sudo -n -u 'frameworks'", "sha256sum --check --status", "mktemp -d", "release-preflight"} {
		if !strings.Contains(script, part) {
			t.Fatalf("missing %s", part)
		}
	}
	for _, part := range []string{"systemctl restart", "/opt/frameworks/purser/purser", " bootstrap", "data-migrations"} {
		if strings.Contains(script, part) {
			t.Fatalf("preview alters installation or starts workers: %s", part)
		}
	}
}
