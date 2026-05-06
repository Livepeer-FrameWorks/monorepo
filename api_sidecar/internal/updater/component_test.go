package updater

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
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

func TestReplaceDirsAtomicallyRollsBackAfterFailure(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	oldBin := filepath.Join(root, "bin")
	oldLib := filepath.Join(root, "lib")
	newBin := filepath.Join(root, "new-bin")
	newLib := filepath.Join(root, "new-lib")
	for _, dir := range []string{oldBin, oldLib, newBin, newLib} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(oldBin, "MistController"), []byte("old-bin"), 0o644); err != nil {
		t.Fatalf("write old bin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(oldLib, "libmist.so"), []byte("old-lib"), 0o644); err != nil {
		t.Fatalf("write old lib: %v", err)
	}
	if err := os.WriteFile(filepath.Join(newBin, "MistController"), []byte("new-bin"), 0o644); err != nil {
		t.Fatalf("write new bin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(newLib, "libmist.so"), []byte("new-lib"), 0o644); err != nil {
		t.Fatalf("write new lib: %v", err)
	}

	err := replaceDirsAtomically([]dirReplacement{
		{src: newBin, dst: oldBin},
		{src: newLib, dst: oldLib},
	}, func() error {
		return errors.New("restart failed")
	})
	if err == nil {
		t.Fatal("replaceDirsAtomically succeeded despite post-replacement failure")
	}

	binBytes, err := os.ReadFile(filepath.Join(oldBin, "MistController"))
	if err != nil {
		t.Fatalf("read restored bin: %v", err)
	}
	if string(binBytes) != "old-bin" {
		t.Fatalf("bin content = %q, want old-bin", string(binBytes))
	}
	libBytes, err := os.ReadFile(filepath.Join(oldLib, "libmist.so"))
	if err != nil {
		t.Fatalf("read restored lib: %v", err)
	}
	if string(libBytes) != "old-lib" {
		t.Fatalf("lib content = %q, want old-lib", string(libBytes))
	}
}

func TestReplaceDirsAtomicallyKeepsDestinationVisible(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	root := filepath.Join(parent, "mistserver")
	staged := filepath.Join(parent, "mistserver-staged")
	for _, dir := range []string{filepath.Join(root, "bin"), filepath.Join(staged, "bin")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "bin", "MistController"), []byte("old-bin"), 0o755); err != nil {
		t.Fatalf("write old controller: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staged, "bin", "MistController"), []byte("new-bin"), 0o755); err != nil {
		t.Fatalf("write new controller: %v", err)
	}

	err := replaceDirsAtomically([]dirReplacement{{src: staged, dst: root}}, func() error {
		controller, readErr := os.ReadFile(filepath.Join(root, "bin", "MistController"))
		if readErr != nil {
			t.Fatalf("live root disappeared during replacement: %v", readErr)
		}
		if string(controller) != "new-bin" {
			t.Fatalf("controller during replacement = %q, want new-bin", string(controller))
		}
		return errors.New("force rollback")
	})
	if err == nil {
		t.Fatal("replaceDirsAtomically succeeded despite forced rollback")
	}
	controller, err := os.ReadFile(filepath.Join(root, "bin", "MistController"))
	if err != nil {
		t.Fatalf("read restored controller: %v", err)
	}
	if string(controller) != "old-bin" {
		t.Fatalf("controller after rollback = %q, want old-bin", string(controller))
	}
}

func TestMistPayloadReplacementSwapsSingleRootAndPreservesWrapper(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	root := filepath.Join(parent, "mistserver")
	staging := t.TempDir()
	for _, dir := range []string{
		filepath.Join(root, "bin"),
		filepath.Join(root, "lib"),
		filepath.Join(staging, "bin"),
		filepath.Join(staging, "lib"),
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
	wrapper, err := os.ReadFile(filepath.Join(replacement.src, "run.sh"))
	if err != nil {
		t.Fatalf("read staged wrapper: %v", err)
	}
	if string(wrapper) != "#!/bin/sh\n" {
		t.Fatalf("wrapper = %q, want preserved script", string(wrapper))
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
