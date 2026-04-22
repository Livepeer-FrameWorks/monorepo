package provisioner

import (
	"strings"
	"testing"

	"frameworks/cli/pkg/gitops"
)

func TestBuildYugabyteInstallScript_usesManifestURLAndChecksum(t *testing.T) {
	t.Parallel()
	amd := &gitops.Artifact{
		Arch:     "linux-amd64",
		URL:      "https://downloads.yugabyte.com/releases/2025.1.3.2/yugabyte-2025.1.3.2-b1-linux-x86_64.tar.gz",
		Checksum: "sha256:31d8b3a65f75d96a3ffe9fe61f8398018576afe8439c0b43115075296cee3a20",
	}
	arm := &gitops.Artifact{
		Arch:     "linux-arm64",
		URL:      "https://downloads.yugabyte.com/releases/2025.1.3.2/yugabyte-2025.1.3.2-b1-el8-aarch64.tar.gz",
		Checksum: "sha256:ff6f7c4170bfda693f89103c2212a538ba68e0a632386b7bd96fa5daf7579920",
	}

	script := buildYugabyteInstallScript("2025.1.3.2", 0, "master", "tserver", "munit", "tunit", amd, arm)

	mustContain := []string{
		amd.URL,
		amd.Checksum,
		arm.URL,
		arm.Checksum,
		"sha256sum -c",
		"curl --fail",
		`case "$(uname -m)"`,
		"x86_64)",
		"aarch64|arm64)",
	}
	for _, fragment := range mustContain {
		if !strings.Contains(script, fragment) {
			t.Errorf("script missing %q", fragment)
		}
	}

	mustNotContain := []string{
		`yugabyte-${VERSION}-linux-`,
		`URL="https://downloads.yugabyte.com/releases/${VERSION}`,
		"curl -sSLO",
	}
	for _, fragment := range mustNotContain {
		if strings.Contains(script, fragment) {
			t.Errorf("script must not contain string-built URL fragment %q — regressed to URL guessing", fragment)
		}
	}
}

func TestBuildYugabyteInstallScript_tolerates_missing_checksum(t *testing.T) {
	t.Parallel()
	amd := &gitops.Artifact{Arch: "linux-amd64", URL: "https://x/a.tgz"}
	arm := &gitops.Artifact{Arch: "linux-arm64", URL: "https://x/b.tgz"}
	script := buildYugabyteInstallScript("2025.1.3.2", 0, "m", "t", "mu", "tu", amd, arm)
	if !strings.Contains(script, "https://x/a.tgz") || !strings.Contains(script, "https://x/b.tgz") {
		t.Error("must still emit download URLs when checksum absent")
	}
	if !strings.Contains(script, `"")       ;;`) {
		t.Error("missing-checksum branch must be present and a silent no-op")
	}
}

func TestBuildYugabyteInstallScript_includesTimeSyncAndCurlSnippets(t *testing.T) {
	t.Parallel()
	amd := &gitops.Artifact{Arch: "linux-amd64", URL: "https://x/a.tgz", Checksum: "sha256:aa"}
	arm := &gitops.Artifact{Arch: "linux-arm64", URL: "https://x/b.tgz", Checksum: "sha256:bb"}
	script := buildYugabyteInstallScript("2025.1.3.2", 1, "m", "t", "mu", "tu", amd, arm)
	if !strings.Contains(script, "systemctl is-active --quiet chronyd") {
		t.Error("install script must splice the time-sync snippet")
	}
	if !strings.Contains(script, "command -v curl") {
		t.Error("install script must splice the curl-ensure snippet")
	}
	if strings.Contains(script, "__FRAMEWORKS_") {
		t.Error("install script still contains unsubstituted sentinel — one of the Replace calls missed")
	}
}
