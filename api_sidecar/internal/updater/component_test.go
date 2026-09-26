package updater

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func TestWriteComponentVersionRejectsUnsafeVersion(t *testing.T) {
	t.Parallel()

	if err := WriteComponentVersion("helmsman", "v1.2.3\nEXTRA=1"); err == nil {
		t.Fatal("WriteComponentVersion accepted multiline version")
	}
}

func TestWriteComponentVersionRejectsUnknownComponent(t *testing.T) {
	t.Parallel()

	if err := WriteComponentVersion("surprise", "v1.2.3"); err == nil {
		t.Fatal("WriteComponentVersion accepted unsupported component")
	}
}

func TestMistPayloadReplacementStagesReleaseOwnedPayload(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	root := filepath.Join(parent, "mistserver")
	staging := t.TempDir()
	for _, dir := range []string{
		filepath.Join(root, "bin"),
		filepath.Join(root, "lib"),
		filepath.Join(root, "share"),
		filepath.Join(root, "opt", "mist-onnx", "lib"),
		filepath.Join(staging, "bin"),
		filepath.Join(staging, "lib"),
		filepath.Join(staging, "share"),
		filepath.Join(staging, "opt", "mist-onnx", "lib"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "run.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write wrapper: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "bin", "MistController"), []byte("old-bin"), 0o755); err != nil {
		t.Fatalf("write old controller: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "lib", "libmist.so"), []byte("old-lib"), 0o644); err != nil {
		t.Fatalf("write old lib: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staging, "bin", "MistController"), []byte("new-bin"), 0o755); err != nil {
		t.Fatalf("write new controller: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staging, "lib", "libmist.so"), []byte("new-lib"), 0o644); err != nil {
		t.Fatalf("write new lib: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "share", "contract.json"), []byte("old-share"), 0o644); err != nil {
		t.Fatalf("write old share: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staging, "share", "contract.json"), []byte("new-share"), 0o644); err != nil {
		t.Fatalf("write new share: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "opt", "mist-onnx", "lib", "provider.so"), []byte("old-provider"), 0o644); err != nil {
		t.Fatalf("write old provider: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staging, "opt", "mist-onnx", "lib", "provider.so"), []byte("new-provider"), 0o644); err != nil {
		t.Fatalf("write new provider: %v", err)
	}

	replacement, err := mistPayloadReplacement(staging, root)
	if err != nil {
		t.Fatalf("mistPayloadReplacement: %v", err)
	}
	defer os.RemoveAll(replacement.src)
	if replacement.dst != root {
		t.Fatalf("replacement dst = %q, want %q", replacement.dst, root)
	}
	if filepath.Dir(replacement.src) != parent {
		t.Fatalf("replacement src parent = %q, want %q", filepath.Dir(replacement.src), parent)
	}
	if _, statErr := os.Stat(filepath.Join(replacement.src, "run.sh")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("unmanaged wrapper was copied into release staging: %v", statErr)
	}
	controller, err := os.ReadFile(filepath.Join(replacement.src, "bin", "MistController"))
	if err != nil {
		t.Fatalf("read staged controller: %v", err)
	}
	if string(controller) != "new-bin" {
		t.Fatalf("controller = %q, want new-bin", string(controller))
	}
	lib, err := os.ReadFile(filepath.Join(replacement.src, "lib", "libmist.so"))
	if err != nil {
		t.Fatalf("read staged lib: %v", err)
	}
	if string(lib) != "new-lib" {
		t.Fatalf("lib = %q, want new-lib", string(lib))
	}
	provider, err := os.ReadFile(filepath.Join(replacement.src, "opt", "mist-onnx", "lib", "provider.so"))
	if err != nil {
		t.Fatalf("read staged provider: %v", err)
	}
	if string(provider) != "new-provider" {
		t.Fatalf("provider = %q, want new-provider", string(provider))
	}
	share, err := os.ReadFile(filepath.Join(replacement.src, "share", "contract.json"))
	if err != nil {
		t.Fatalf("read staged share: %v", err)
	}
	if string(share) != "new-share" {
		t.Fatalf("share = %q, want new-share", string(share))
	}
}

func TestMistPayloadReplacementRemovesAbsentOptionalPayload(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	root := filepath.Join(parent, "mistserver")
	staging := t.TempDir()
	for _, dir := range []string{filepath.Join(root, "bin"), filepath.Join(root, "opt", "mist-onnx"), filepath.Join(staging, "bin")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{filepath.Join(root, "bin", "MistController"), filepath.Join(staging, "bin", "MistController")} {
		if err := os.WriteFile(path, []byte("bin"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "opt", "mist-onnx", "provider.so"), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	replacement, err := mistPayloadReplacement(staging, root)
	if err != nil {
		t.Fatalf("mistPayloadReplacement: %v", err)
	}
	defer os.RemoveAll(replacement.src)
	if _, err := os.Stat(filepath.Join(replacement.src, "opt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale optional provider payload survived CPU replacement: %v", err)
	}
}

func TestReplaceMistPayloadInPlaceKeepsCanonicalDirectories(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	root := filepath.Join(parent, "mistserver")
	staged := filepath.Join(parent, "staged")
	for _, dir := range []string{filepath.Join(root, "bin"), filepath.Join(root, "lib"), filepath.Join(staged, "bin"), filepath.Join(staged, "lib")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for path, contents := range map[string]string{
		filepath.Join(root, "bin", "MistController"):   "old-controller",
		filepath.Join(root, "bin", "MistOutHTTP"):      "old-output",
		filepath.Join(root, "lib", "libmist.so"):       "old-lib",
		filepath.Join(staged, "bin", "MistController"): "new-controller",
		filepath.Join(staged, "bin", "MistOutHTTP"):    "new-output",
		filepath.Join(staged, "lib", "libmist.so"):     "new-lib",
	} {
		if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "run.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	binBefore, err := os.Stat(filepath.Join(root, "bin"))
	if err != nil {
		t.Fatal(err)
	}
	callbackRan := false
	if replaceErr := replaceMistPayloadInPlace(staged, root, func() error {
		callbackRan = true
		binDuring, statErr := os.Stat(filepath.Join(root, "bin"))
		if statErr != nil {
			return statErr
		}
		if !os.SameFile(binBefore, binDuring) {
			return errors.New("canonical bin directory was replaced")
		}
		controller, readErr := os.ReadFile(filepath.Join(root, "bin", "MistController"))
		if readErr != nil {
			return readErr
		}
		if string(controller) != "new-controller" {
			return errors.New("new controller was not visible before signal")
		}
		return nil
	}); replaceErr != nil {
		t.Fatalf("replaceMistPayloadInPlace: %v", replaceErr)
	}
	if !callbackRan {
		t.Fatal("post-install callback did not run")
	}
	wrapper, err := os.ReadFile(filepath.Join(root, "run.sh"))
	if err != nil || string(wrapper) != "#!/bin/sh\n" {
		t.Fatalf("unmanaged wrapper was not preserved: content=%q err=%v", wrapper, err)
	}
}

func TestReplaceMistPayloadInPlaceRollsBackSignalFailure(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	root := filepath.Join(parent, "mistserver")
	staged := filepath.Join(parent, "staged")
	for _, dir := range []string{filepath.Join(root, "bin"), filepath.Join(staged, "bin")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "bin", "MistController"), []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staged, "bin", "MistController"), []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staged, "bin", "MistOutHTTP"), []byte("new-only"), 0o755); err != nil {
		t.Fatal(err)
	}

	err := replaceMistPayloadInPlace(staged, root, func() error { return errors.New("signal failed") })
	if err == nil || !strings.Contains(err.Error(), "signal failed") {
		t.Fatalf("expected signal failure, got %v", err)
	}
	controller, err := os.ReadFile(filepath.Join(root, "bin", "MistController"))
	if err != nil || string(controller) != "old" {
		t.Fatalf("controller was not rolled back: content=%q err=%v", controller, err)
	}
	if _, err := os.Stat(filepath.Join(root, "bin", "MistOutHTTP")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new-only binary survived rollback: %v", err)
	}
}

func TestWriteMistManagedMetadataStampsProvisionSentinel(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	component := &ipcpb.DesiredComponent{
		Component:   "mist",
		Version:     "v1.2.3",
		ArtifactUrl: "https://example.test/mistserver-linux-amd64.tar.gz",
		Checksum:    "sha256:" + strings.Repeat("a", 64),
		OnnxProfile: "cuda",
	}

	if err := writeMistManagedMetadata(root, component); err != nil {
		t.Fatalf("writeMistManagedMetadata: %v", err)
	}
	if _, err := os.Stat(componentInstallSentinelPath(root, component.GetChecksum())); err != nil {
		t.Fatalf("expected install sentinel: %v", err)
	}
	manifest, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	for _, want := range []string{
		`"component": "mistserver"`,
		`"version": "v1.2.3"`,
		`"artifact_url": "https://example.test/mistserver-linux-amd64.tar.gz"`,
		`"artifact_checksum": "sha256:` + strings.Repeat("a", 64) + `"`,
		`"onnx_profile": "cuda"`,
	} {
		if !strings.Contains(string(manifest), want) {
			t.Fatalf("manifest missing %s:\n%s", want, manifest)
		}
	}
}

func TestWriteComponentInstallSentinelFallsBackToArtifactURL(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	component := &ipcpb.DesiredComponent{
		Component:   "helmsman",
		Version:     "v1.2.3",
		ArtifactUrl: "https://example.test/helmsman.tar.gz",
	}

	if err := writeComponentInstallSentinel(root, component); err != nil {
		t.Fatalf("writeComponentInstallSentinel: %v", err)
	}
	if _, err := os.Stat(componentInstallSentinelPath(root, component.GetArtifactUrl())); err != nil {
		t.Fatalf("expected URL-derived install sentinel: %v", err)
	}
}

func TestLinuxMistControllerSignalTargetsMainProcess(t *testing.T) {
	t.Parallel()

	command, args := linuxMistControllerSignalCommand()
	if command != "systemctl" {
		t.Fatalf("command = %q, want systemctl", command)
	}
	if !slices.Contains(args, "--kill-whom=main") {
		t.Fatalf("args = %#v, want --kill-whom=main", args)
	}
}

type restartCountingController struct {
	fakeServiceController
	restarts   *int
	restartErr error
}

func (c restartCountingController) RestartMist(context.Context) error {
	*c.restarts++
	return c.restartErr
}

func TestCompleteMistReloadRestartsWhenReloadDoesNotServe(t *testing.T) {
	reloadStuck := errors.New("MistController PID 9 still runs the previous executable")
	cases := []struct {
		name         string
		verify       []error
		restartErr   error
		wantRestarts int
		wantErr      bool
		wantDetail   string
	}{
		{name: "reload serves", verify: []error{nil}, wantRestarts: 0},
		{name: "restart recovers", verify: []error{reloadStuck, nil}, wantRestarts: 1, wantDetail: "MistServer restarted because the rolling reload failed: " + reloadStuck.Error()},
		{name: "restart fails", verify: []error{reloadStuck}, restartErr: errors.New("no MistController process found"), wantRestarts: 1, wantErr: true},
		{name: "still down after restart", verify: []error{reloadStuck, errors.New("MistServer API did not answer")}, wantRestarts: 1, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			restarts := 0
			restore := SetServiceControllerForTest(restartCountingController{restarts: &restarts, restartErr: tc.restartErr})
			defer restore()
			calls := 0
			originalVerify := verifyMistReload
			verifyMistReload = func(context.Context, string) error {
				if calls >= len(tc.verify) {
					t.Fatalf("unexpected verification call %d", calls+1)
				}
				err := tc.verify[calls]
				calls++
				return err
			}
			defer func() { verifyMistReload = originalVerify }()

			detail, err := completeMistReload(context.Background(), "/opt/frameworks/mistserver/bin/MistController")
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr && !strings.Contains(err.Error(), reloadStuck.Error()) {
				t.Fatalf("error %q must name the reload failure", err)
			}
			if restarts != tc.wantRestarts {
				t.Fatalf("restarts = %d, want %d", restarts, tc.wantRestarts)
			}
			if !strings.Contains(detail, tc.wantDetail) || (tc.wantDetail == "" && detail != "") {
				t.Fatalf("detail = %q, want %q", detail, tc.wantDetail)
			}
		})
	}
}
