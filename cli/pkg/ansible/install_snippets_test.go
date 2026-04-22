package ansible

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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
		`sed -n 's/.*"\([0-9._]*\)".*/\1/p'`,
		"java_major",
		"-ge 11",
		"command -v archlinux-java",
		"for prov_dir in /usr/lib/jvm/*",
		"prov_major",
		"archlinux-java set $compatible_provider",
		"pacman -Syu --noconfirm --needed jre-openjdk-headless",
	}
	for _, fragment := range want {
		if !strings.Contains(EnsureJavaRuntimeInstallSnippet, fragment) {
			t.Errorf("EnsureJavaRuntimeInstallSnippet missing %q", fragment)
		}
	}
	if strings.Contains(EnsureJavaRuntimeInstallSnippet, `sed -n 's/.*"\([0-9._]*\).*/\1/p'`) {
		t.Error("EnsureJavaRuntimeInstallSnippet contains the broken Java version regex (missing closing quote)")
	}
}

func TestEnsureJavaRuntimeInstallSnippet_rejectsTextParsing(t *testing.T) {
	t.Parallel()
	forbidden := []string{
		`archlinux-java status`,
		`awk '/^ /`,
	}
	for _, fragment := range forbidden {
		if strings.Contains(EnsureJavaRuntimeInstallSnippet, fragment) {
			t.Errorf("EnsureJavaRuntimeInstallSnippet must not parse text output; still contains %q", fragment)
		}
	}
}

func TestEnsureJavaRuntimeInstallSnippet_doesNotAbortOnIncompatibleProvider(t *testing.T) {
	t.Parallel()
	if !strings.Contains(EnsureJavaRuntimeInstallSnippet, `if [ -n "$compatible_provider" ]`) {
		t.Error("EnsureJavaRuntimeInstallSnippet must only abort when a compatible provider was found")
	}
	if !strings.Contains(EnsureJavaRuntimeInstallSnippet, `[ ! -L "$prov_dir" ]`) {
		t.Error("EnsureJavaRuntimeInstallSnippet must skip convenience symlinks like default/default-runtime")
	}
}

func TestEnsureJavaRuntimeInstallSnippet_doesNotInstallCurl(t *testing.T) {
	t.Parallel()
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

func TestSafeTarballExtractSnippet_hasSafetyChecks(t *testing.T) {
	t.Parallel()
	want := []string{
		"extract_tarball_to()",
		`tmpdir="$(mktemp -d)"`,
		`tar -xf "$archive" -C "$tmpdir"`,
		`find "$tmpdir" -mindepth 1 -maxdepth 1`,
		`if [ "$count" != "1" ]`,
		`if [ ! -d "$inner" ]`,
		"expected 1 top-level entry",
		"top-level entry is not a directory",
	}
	for _, fragment := range want {
		if !strings.Contains(SafeTarballExtractSnippet, fragment) {
			t.Errorf("SafeTarballExtractSnippet missing %q", fragment)
		}
	}
}

const javaDetectionProbe = `
printf "HCJ=%s\n" "$have_compatible_java"
printf "CP=%s\n" "${compatible_provider:-}"
exit 0
`

func runJavaDetection(t *testing.T, pathDir, jvmRoot string) (stdout, stderr string, exitCode int) {
	t.Helper()
	snippet := EnsureJavaRuntimeInstallSnippet
	if jvmRoot != "" {
		snippet = strings.ReplaceAll(snippet, "/usr/lib/jvm", jvmRoot)
	}
	installLadder := "  if command -v apt-get >/dev/null 2>&1; then"
	if !strings.Contains(snippet, installLadder) {
		t.Fatalf("cannot locate install-ladder splice point in snippet")
	}
	// Splice a probe before the install ladder (HCJ=0 paths) and after the
	// outer if/else (HCJ=1 paths) so the pacman/apt-get branches never run.
	snippet = strings.Replace(snippet, installLadder, javaDetectionProbe+installLadder, 1)
	snippet += "\n" + javaDetectionProbe

	cmd := exec.CommandContext(t.Context(), "/bin/bash", "-c", snippet)
	cmd.Env = []string{
		"PATH=" + pathDir + ":" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"LANG=C",
	}
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		exitCode = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("bash invocation failed: %v", err)
	}
	return out.String(), errBuf.String(), exitCode
}

func writeShim(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureJavaRuntimeInstallSnippet_activeCompatible(t *testing.T) {
	t.Parallel()
	bin := t.TempDir()
	writeShim(t, bin, "java", `printf 'openjdk version "17.0.1" 2024-01-16\nOpenJDK Runtime Environment\n' >&2`)
	stdout, stderr, exitCode := runJavaDetection(t, bin, "")
	if exitCode != 0 {
		t.Fatalf("want exit 0, got %d; stderr=%q", exitCode, stderr)
	}
	if !strings.Contains(stdout, "HCJ=1") {
		t.Errorf("want HCJ=1, got stdout=%q", stdout)
	}
}

func TestEnsureJavaRuntimeInstallSnippet_activeIncompatible(t *testing.T) {
	t.Parallel()
	bin := t.TempDir()
	writeShim(t, bin, "java", `printf 'java version "1.8.0_281"\nJava(TM) SE Runtime Environment\n' >&2`)
	stdout, _, exitCode := runJavaDetection(t, bin, "")
	if exitCode != 0 {
		t.Fatalf("want exit 0 (no archlinux-java on PATH so no remediation); got %d", exitCode)
	}
	if !strings.Contains(stdout, "HCJ=0") {
		t.Errorf("Java 8 must not count as compatible; stdout=%q", stdout)
	}
}

func TestEnsureJavaRuntimeInstallSnippet_dormantCompatibleProvider(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "bin")
	jvmRoot := filepath.Join(tmp, "usr", "lib", "jvm")
	writeShim(t, bin, "java", `printf 'openjdk version "1.8.0_281"\n' >&2`)
	writeShim(t, bin, "archlinux-java", "exit 0")
	writeShim(t, filepath.Join(jvmRoot, "java-17-openjdk", "bin"), "java",
		`printf 'openjdk version "17.0.2" 2024-07-16\n' >&2`)
	// default-runtime symlink: the detection must skip it, not advertise it.
	if err := os.Symlink("java-17-openjdk", filepath.Join(jvmRoot, "default-runtime")); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, exitCode := runJavaDetection(t, bin, jvmRoot)
	if exitCode != 1 {
		t.Fatalf("want exit 1 (remediation), got %d; stdout=%q stderr=%q", exitCode, stdout, stderr)
	}
	if !strings.Contains(stderr, "archlinux-java set java-17-openjdk") {
		t.Errorf("stderr must name the compatible provider; got %q", stderr)
	}
	if strings.Contains(stderr, "default-runtime") {
		t.Errorf("must not advertise the symlink as a provider name; got %q", stderr)
	}
}

func TestEnsureJavaRuntimeInstallSnippet_dormantIncompatibleProvider(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "bin")
	jvmRoot := filepath.Join(tmp, "usr", "lib", "jvm")
	writeShim(t, bin, "java", `printf 'openjdk version "1.8.0_281"\n' >&2`)
	writeShim(t, bin, "archlinux-java", "exit 0")
	writeShim(t, filepath.Join(jvmRoot, "jre8-openjdk", "bin"), "java",
		`printf 'openjdk version "1.8.0_281"\n' >&2`)
	stdout, stderr, exitCode := runJavaDetection(t, bin, jvmRoot)
	if exitCode != 0 {
		t.Fatalf("want exit 0 (no compatible provider, fall through to install); got %d; stderr=%q", exitCode, stderr)
	}
	if !strings.Contains(stdout, "CP=") || strings.Contains(stdout, "CP=jre8-openjdk") {
		t.Errorf("compatible_provider must be empty; got stdout=%q", stdout)
	}
	if strings.Contains(stderr, "archlinux-java set") {
		t.Errorf("must not emit remediation for incompatible provider; got stderr=%q", stderr)
	}
}

// --- SafeTarballExtractSnippet functional tests ---

// runExtractHelper runs `extract_tarball_to <archive> <dest>` in a fresh bash
// and returns the results. The helper body is spliced in from
// SafeTarballExtractSnippet.
func runExtractHelper(t *testing.T, archive, dest string) (stderr string, exitCode int) {
	t.Helper()
	script := SafeTarballExtractSnippet + "\nextract_tarball_to " +
		shellQuote(archive) + " " + shellQuote(dest) + "\n"
	cmd := exec.CommandContext(t.Context(), "/bin/bash", "-c", script)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		exitCode = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("bash invocation failed: %v", err)
	}
	return errBuf.String(), exitCode
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func buildTarball(t *testing.T, out string, layout map[string]string) {
	t.Helper()
	src := t.TempDir()
	for rel, body := range layout {
		full := filepath.Join(src, rel)
		if strings.HasSuffix(rel, "/") {
			if err := os.MkdirAll(full, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"-czf", out, "-C", src}
	for _, e := range entries {
		args = append(args, e.Name())
	}
	if err := exec.CommandContext(t.Context(), "tar", args...).Run(); err != nil {
		t.Fatalf("tar -czf %s: %v", out, err)
	}
}

func TestSafeTarballExtractSnippet_happyPath(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	archive := filepath.Join(tmp, "pkg.tgz")
	buildTarball(t, archive, map[string]string{
		"pkg-1.0/bin/thing": "hello",
		"pkg-1.0/README":    "readme",
	})
	dest := filepath.Join(tmp, "out")
	stderr, exitCode := runExtractHelper(t, archive, dest)
	if exitCode != 0 {
		t.Fatalf("want exit 0, got %d; stderr=%q", exitCode, stderr)
	}
	if body, err := os.ReadFile(filepath.Join(dest, "bin", "thing")); err != nil || string(body) != "hello" {
		t.Errorf("dest contents missing or wrong: err=%v body=%q", err, body)
	}
}

func TestSafeTarballExtractSnippet_rejectsMultipleTopLevel(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	archive := filepath.Join(tmp, "bad.tgz")
	buildTarball(t, archive, map[string]string{
		"first/file.txt":  "a",
		"second/file.txt": "b",
	})
	dest := filepath.Join(tmp, "out")
	stderr, exitCode := runExtractHelper(t, archive, dest)
	if exitCode == 0 {
		t.Fatalf("want non-zero exit, got 0")
	}
	if !strings.Contains(stderr, "expected 1 top-level entry") {
		t.Errorf("missing diagnostic; stderr=%q", stderr)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("dest must not exist after failed extract: err=%v", err)
	}
}

func TestSafeTarballExtractSnippet_rejectsFileAtTop(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	archive := filepath.Join(tmp, "flat.tgz")
	buildTarball(t, archive, map[string]string{
		"loose.txt": "not a directory",
	})
	dest := filepath.Join(tmp, "out")
	stderr, exitCode := runExtractHelper(t, archive, dest)
	if exitCode == 0 {
		t.Fatalf("want non-zero exit for single-file tarball, got 0")
	}
	if !strings.Contains(stderr, "top-level entry is not a directory") {
		t.Errorf("missing directory-type diagnostic; stderr=%q", stderr)
	}
}
