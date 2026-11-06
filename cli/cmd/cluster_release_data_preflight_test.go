package cmd

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	serviceexec "frameworks/cli/pkg/exec"
	"frameworks/cli/pkg/gitops"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativePreflightExecutesReleaseArchiveMember(t *testing.T) {
	for _, arch := range []string{"linux-amd64", "linux-arm64"} {
		t.Run(arch, func(t *testing.T) {
			dir := t.TempDir()
			archivePath := filepath.Join(dir, "release.tar.gz")
			archive, err := os.Create(archivePath)
			if err != nil {
				t.Fatal(err)
			}
			gzipWriter := gzip.NewWriter(archive)
			tarWriter := tar.NewWriter(gzipWriter)
			body := []byte("#!/bin/sh\n[ \"$1\" = release-preflight ] && [ \"$PREFLIGHT_ENV\" = loaded ] || exit 71\nprintf '{\"checks\":[]}\\n'\n")
			if err = tarWriter.WriteHeader(&tar.Header{Name: "frameworks-purser-" + arch, Mode: 0700, Size: int64(len(body))}); err != nil {
				t.Fatal(err)
			}
			if _, err = tarWriter.Write(body); err != nil {
				t.Fatal(err)
			}
			for _, closeFn := range []func() error{tarWriter.Close, gzipWriter.Close, archive.Close} {
				if err = closeFn(); err != nil {
					t.Fatal(err)
				}
			}
			raw, err := os.ReadFile(archivePath)
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(raw)
			checksum := hex.EncodeToString(digest[:])
			envFile := filepath.Join(dir, "purser.env")
			if err = os.WriteFile(envFile, []byte("PREFLIGHT_ENV=loaded\n"), 0600); err != nil {
				t.Fatal(err)
			}
			for name, body := range map[string]string{
				"sudo":      "#!/bin/sh\n[ \"$1\" = -n ] && [ \"$2\" = -u ] || exit 72\nshift 3\nexec \"$@\"\n",
				"curl":      "#!/bin/sh\nwhile [ \"$#\" -gt 0 ]; do\n if [ \"$1\" = -o ]; then shift; cp \"$TEST_ARCHIVE\" \"$1\"; exit; fi\n shift\ndone\nexit 73\n",
				"sha256sum": "#!/bin/sh\nread -r checksum filename\n[ \"$checksum\" = \"$TEST_CHECKSUM\" ] && [ -s \"$filename\" ]\n",
			} {
				if err = os.WriteFile(filepath.Join(dir, name), []byte(body), 0700); err != nil {
					t.Fatal(err)
				}
			}
			script, err := nativeDataPreflightScript(gitops.Artifact{Arch: arch, URL: "https://example.test/release.tar.gz", Checksum: "sha256:" + checksum}, serviceexec.Spec{Mode: serviceexec.ModeNative, User: "frameworks", EnvFile: envFile, WorkingDirectory: dir})
			if err != nil {
				t.Fatal(err)
			}
			command := exec.CommandContext(t.Context(), "bash", "-c", script)
			command.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "TEST_ARCHIVE="+archivePath, "TEST_CHECKSUM="+checksum)
			out, err := command.CombinedOutput()
			if err != nil || strings.TrimSpace(string(out)) != `{"checks":[]}` {
				t.Fatalf("release preflight did not execute the %s binary: %v\n%s", arch, err, out)
			}
		})
	}
}

func TestNativePreflightUsesVerifiedIsolatedTarget(t *testing.T) {
	if _, err := nativeDataPreflightScript(gitops.Artifact{URL: "http://example.test/binary", Checksum: "sha256:bad"}, serviceexec.SpecFromDetection("native", nil, "purser")); err == nil {
		t.Fatal("accepted an unverified artifact")
	}
	script, err := nativeDataPreflightScript(gitops.Artifact{Arch: "linux-amd64", URL: "https://example.test/purser.tar.gz", Checksum: "sha256:" + strings.Repeat("a", 64)}, serviceexec.SpecFromDetection("native", nil, "purser"))
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

func TestDockerPreflightUsesSupportedDockerAccess(t *testing.T) {
	for _, tc := range []struct {
		name, direct, wantCalls string
	}{
		{"direct", "1", "0:inspect\n0:run\n"},
		{"sudo", "0", "1:inspect\n1:run\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, body := range map[string]string{
				"docker": `#!/bin/sh
if [ "$1" = version ]; then [ "$DIRECT_DOCKER" = 1 ]; exit $?; fi
if [ "$DIRECT_DOCKER" != 1 ] && [ "${SUDO_DOCKER:-0}" != 1 ]; then
  echo 'Docker socket permission denied' >&2
  exit 77
fi
printf '%s:%s\n' "${SUDO_DOCKER:-0}" "$1" >> "$CALLS_FILE"
case "$1" in
inspect) printf '%s\n' 'DATABASE_URL=postgres://purser@example.test/purser' 'UNRELATED_SECRET=must-not-copy' ;;
run)
  while [ "$#" -gt 0 ] && [ "$1" != --env-file ]; do shift; done
  [ "$#" -ge 2 ] || exit 78
  shift
  cmp "$1" "$EXPECTED_ENV" || exit 79
  printf '{"release":"v0.3.14"}\n'
  ;;
*) exit 80 ;;
esac
`,
				"sudo": `#!/bin/sh
[ "$1" = -n ] || exit 81
shift
export SUDO_DOCKER=1
exec "$@"
`,
			} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			expected := filepath.Join(dir, "expected.env")
			if err := os.WriteFile(expected, []byte("DATABASE_URL=postgres://purser@example.test/purser\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			calls := filepath.Join(dir, "calls")
			cmd := exec.CommandContext(t.Context(), "bash", "-c", dockerDataPreflightScript("frameworks-purser", "registry.example/purser@sha256:"+strings.Repeat("a", 64)))
			cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "DIRECT_DOCKER="+tc.direct, "SUDO_DOCKER=0", "CALLS_FILE="+calls, "EXPECTED_ENV="+expected)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("preflight failed with %s Docker access: %v\n%s", tc.name, err, out)
			}
			if strings.TrimSpace(string(out)) != `{"release":"v0.3.14"}` {
				t.Fatalf("unexpected preflight report: %s", out)
			}
			gotCalls, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			if string(gotCalls) != tc.wantCalls {
				t.Fatalf("Docker calls = %q, want %q", gotCalls, tc.wantCalls)
			}
		})
	}
}
