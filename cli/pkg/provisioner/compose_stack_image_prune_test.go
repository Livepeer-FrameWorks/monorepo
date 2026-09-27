package provisioner

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeImagePruneDocker answers the docker calls compose-image-prune.sh makes
// for a stack whose container runs svc:v3 and records every removal.
const fakeImagePruneDocker = `#!/bin/sh
case "$*" in
  *" images --quiet")
    echo "sha256:3333333333333333"
    ;;
  *" config --images")
    echo "ghcr.io/org/svc:v3@sha256:d3"
    echo "registry.example:5000/org/sidecar:1"
    ;;
  "image ls --no-trunc --digests --format {{.ID}} {{.Tag}} {{.Digest}} ghcr.io/org/svc")
    echo "sha256:4444444444444444 v4 sha256:d4"
    echo "sha256:3333333333333333 v3 sha256:d3"
    echo "sha256:2222222222222222 v2 sha256:d2"
    echo "sha256:1111111111111111 v1 sha256:d1"
    echo "sha256:1111111111111111 v1-alias sha256:d1"
    echo "sha256:0000000000000000 <none> sha256:d0"
    ;;
  "image ls --no-trunc --digests --format {{.ID}} {{.Tag}} {{.Digest}} registry.example:5000/org/sidecar")
    echo "sha256:5555555555555555 1 <none>"
    ;;
  "image rm "*)
    echo "${3}" >> "$REMOVED_LOG"
    ;;
  *)
    echo "unexpected docker call: $*" >&2
    exit 64
    ;;
esac
`

// A stack keeps the image it runs and the previous release per repository;
// every older image of those repositories is removed.
func TestComposeImagePruneKeepsRunningAndPreviousImage(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(fakeImagePruneDocker), 0o755); err != nil {
		t.Fatal(err)
	}
	removedLog := filepath.Join(dir, "removed")
	cmd := exec.CommandContext(t.Context(), "sh", composeStackRolePath(t, "files", "compose-image-prune.sh"), "/opt/frameworks/svc")
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "REMOVED_LOG="+removedLog)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("compose-image-prune.sh: %v\n%s", err, stderr.String())
	}
	raw, _ := os.ReadFile(removedLog)
	removed := strings.Fields(string(raw))
	want := []string{"ghcr.io/org/svc:v2", "ghcr.io/org/svc:v1", "ghcr.io/org/svc:v1-alias", "ghcr.io/org/svc@sha256:d0"}
	if !slices.Equal(removed, want) {
		t.Fatalf("removed %v, want %v", removed, want)
	}
	for _, ref := range want {
		if !strings.Contains(stdout.String(), "removed "+ref+"\n") {
			t.Fatalf("stdout missing removal of %s:\n%s", ref, stdout.String())
		}
	}
}

// Pruning runs only after the stack applied successfully, never in check mode.
func TestComposeStackPrunesImagesAfterApply(t *testing.T) {
	data, err := os.ReadFile(composeStackRolePath(t, "tasks", "install.yml"))
	if err != nil {
		t.Fatal(err)
	}
	tasks := string(data)
	apply := strings.Index(tasks, "community.docker.docker_compose_v2:")
	rescue := strings.Index(tasks, "Fail compose stack apply with logs")
	prune := strings.Index(tasks, "compose-image-prune.sh")
	if apply < 0 || rescue < 0 || prune < 0 {
		t.Fatalf("install.yml must apply (%d), rescue (%d) and prune (%d)", apply, rescue, prune)
	}
	if prune < rescue {
		t.Fatal("images must be pruned only after the apply block, whose rescue fails the play")
	}
	block := tasks[prune:]
	if !strings.Contains(block, "not ansible_check_mode") || !strings.Contains(block, "compose_stack_render_only") {
		t.Fatal("image pruning must skip check mode and render-only runs")
	}
}
