package ansible

import (
	"strings"
	"testing"
)

func TestEnsureCurlInstallSnippet_idempotentCheck(t *testing.T) {
	t.Parallel()
	want := []string{
		"command -v curl",
		"apt-get",
		"dnf install -y curl",
		"yum install -y curl",
		"pacman -Syu --noconfirm --needed curl",
	}
	for _, fragment := range want {
		if !strings.Contains(EnsureCurlInstallSnippet, fragment) {
			t.Errorf("EnsureCurlInstallSnippet missing %q", fragment)
		}
	}
	if strings.Contains(EnsureCurlInstallSnippet, "pacman -Sy ") {
		t.Error("EnsureCurlInstallSnippet must not use partial-upgrade `pacman -Sy`")
	}
}

func TestEnsureJavaRuntimeInstallSnippet_versionAwareDetection(t *testing.T) {
	t.Parallel()
	want := []string{
		"java -version",
		"java_major",
		"-ge 11",
		"archlinux-java status",
		"prov_major",
		"archlinux-java set $compatible_provider",
		"pacman -Syu --noconfirm --needed jre-openjdk-headless",
	}
	for _, fragment := range want {
		if !strings.Contains(EnsureJavaRuntimeInstallSnippet, fragment) {
			t.Errorf("EnsureJavaRuntimeInstallSnippet missing %q", fragment)
		}
	}
}

func TestEnsureJavaRuntimeInstallSnippet_doesNotAbortOnIncompatibleProvider(t *testing.T) {
	t.Parallel()
	// The snippet must iterate providers and check each one's actual major
	// version. If we ever revert to pattern-match-and-abort, we'd see a
	// `pacman -Q ... | grep` followed by an unconditional exit — that's a
	// regression. Look for the per-provider loop instead.
	if !strings.Contains(EnsureJavaRuntimeInstallSnippet, `for prov in $(archlinux-java status`) {
		t.Error("EnsureJavaRuntimeInstallSnippet must iterate archlinux-java providers, not pattern-abort")
	}
	if !strings.Contains(EnsureJavaRuntimeInstallSnippet, `if [ -n "$compatible_provider" ]`) {
		t.Error("EnsureJavaRuntimeInstallSnippet must only abort when a compatible provider was found")
	}
}

func TestEnsureJavaRuntimeInstallSnippet_doesNotInstallCurl(t *testing.T) {
	t.Parallel()
	// Curl must come from EnsureCurlInstallSnippet; folding it in here was
	// the regression we're fixing.
	if strings.Contains(EnsureJavaRuntimeInstallSnippet, "install -y curl") ||
		strings.Contains(EnsureJavaRuntimeInstallSnippet, "--needed curl") {
		t.Error("EnsureJavaRuntimeInstallSnippet must not install curl; that belongs to EnsureCurlInstallSnippet")
	}
}

func TestEnsureJavaRuntimeInstallSnippet_refusesPartialUpgrade(t *testing.T) {
	t.Parallel()
	if strings.Contains(EnsureJavaRuntimeInstallSnippet, "pacman -Sy ") {
		t.Error("EnsureJavaRuntimeInstallSnippet must not use partial-upgrade `pacman -Sy`")
	}
}

func TestTimeSyncInstallSnippet_hasDaemonChecks(t *testing.T) {
	t.Parallel()
	want := []string{
		"systemctl is-active --quiet chronyd",
		"systemctl is-active --quiet chrony",
		"systemctl is-active --quiet ntpd",
		"systemctl is-active --quiet ntp",
		"systemctl is-active --quiet systemd-timesyncd",
		"pacman -Syu --noconfirm --needed",
	}
	for _, fragment := range want {
		if !strings.Contains(TimeSyncInstallSnippet, fragment) {
			t.Errorf("TimeSyncInstallSnippet missing %q", fragment)
		}
	}
	if strings.Contains(TimeSyncInstallSnippet, "pacman -Sy ") {
		t.Error("TimeSyncInstallSnippet must not use partial-upgrade `pacman -Sy`")
	}
}
