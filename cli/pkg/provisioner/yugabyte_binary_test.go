package provisioner

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestYugabyteBinaryResolverUsesOnlySelectedRelease(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, "bin", "ysqlsh")
	next := filepath.Join(root, "releases", "new", "bin", "ysqlsh")
	for _, path := range []string{old, next} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	script := strings.ReplaceAll(YugabyteBinaryResolverShell, "/opt/yugabyte", root) + "\nfw_yb_bin ysqlsh"
	check := func(want string, fail bool) {
		t.Helper()
		out, err := exec.CommandContext(t.Context(), "sh", "-c", script).CombinedOutput()
		if fail {
			if err == nil {
				t.Fatalf("unexpected fallback: %s", out)
			}
			return
		}
		if err != nil || strings.TrimSpace(string(out)) != want {
			t.Fatalf("resolved %q (%v), want %q", out, err, want)
		}
	}
	check(old, false)
	link := filepath.Join(root, "current")
	if err := os.Symlink(filepath.Dir(filepath.Dir(next)), link); err != nil {
		t.Fatal(err)
	}
	// Resolve t.TempDir's own /var symlink on macOS as readlink -f does.
	realNext, err := filepath.EvalSymlinks(next)
	if err != nil {
		t.Fatal(err)
	}
	check(realNext, false)
	if err := os.Remove(next); err != nil {
		t.Fatal(err)
	}
	check("", true)
	if err := os.RemoveAll(filepath.Join(root, "releases")); err != nil {
		t.Fatal(err)
	}
	check("", true)
}

func TestYugabyteSQLExecutorResolvesSelectedRelease(t *testing.T) {
	for _, peer := range []bool{true, false} {
		s := &SSHExecutor{UseYugabyteTools: true, UsePeerAuth: peer}
		command := s.buildCommand(ConnParams{User: "yugabyte", Port: 5433, Database: "test"}, "/tmp/test.sql", "")
		if !strings.Contains(command, `yb_client="$(fw_yb_bin ysqlsh)" || exit 1`) || !strings.Contains(command, `"$yb_client" -X`) {
			t.Fatal(command)
		}
		if out, err := exec.CommandContext(t.Context(), "sh", "-n", "-c", command).CombinedOutput(); err != nil {
			t.Fatalf("invalid shell: %s: %v", out, err)
		}
	}
}
