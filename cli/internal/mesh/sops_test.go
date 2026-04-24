package mesh

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStagedFileCommitAndDiscard(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "hosts.yaml")
	original := []byte("hosts:\n  a: {}\n")
	if err := os.WriteFile(target, original, 0o644); err != nil {
		t.Fatal(err)
	}

	staged, err := StageEncryptedYAML(context.Background(), target, "", func(p []byte) ([]byte, error) {
		return append(p, []byte("  b: {}\n")...), nil
	})
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	if staged.NoChange {
		t.Fatal("expected NoChange=false after a real edit")
	}
	if staged.TempPath == "" {
		t.Fatal("expected TempPath to be set")
	}
	// Target unchanged before commit.
	if got, _ := os.ReadFile(target); string(got) != string(original) {
		t.Fatalf("target modified before commit: %q", got)
	}
	if err := staged.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	got, _ := os.ReadFile(target)
	if !strings.Contains(string(got), "b: {}") {
		t.Fatalf("target did not receive staged content: %q", got)
	}
	// Discard after commit is safe.
	staged.Discard()
}

func TestStagedFileNoChangeSkipsWrites(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "hosts.yaml")
	original := []byte("hosts:\n  a: {}\n")
	if err := os.WriteFile(target, original, 0o644); err != nil {
		t.Fatal(err)
	}

	staged, err := StageEncryptedYAML(context.Background(), target, "", func(p []byte) ([]byte, error) {
		return p, nil
	})
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	if !staged.NoChange {
		t.Fatal("expected NoChange=true when edit returns identical bytes")
	}
	if staged.TempPath != "" {
		t.Fatalf("expected empty TempPath on NoChange, got %q", staged.TempPath)
	}
	if err := staged.Commit(); err != nil {
		t.Fatalf("no-op commit: %v", err)
	}
}

func TestCommitManifestAndHostsRollsBackOnSecondFailure(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "cluster.yaml")
	hostsPath := filepath.Join(dir, "hosts.enc.yaml")

	originalManifest := []byte("version: v1\nhosts:\n  a: {}\n")
	originalHosts := []byte("hosts:\n  a:\n    external_ip: 1.2.3.4\n")
	if err := os.WriteFile(manifestPath, originalManifest, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hostsPath, originalHosts, 0o644); err != nil {
		t.Fatal(err)
	}

	// Stage a new manifest via a sibling tempfile.
	newManifest := []byte("version: v1\nhosts:\n  a:\n    wireguard_ip: 10.88.0.2\n")
	manifestTmp, err := os.CreateTemp(dir, ".cluster-*.yaml.tmp")
	if err != nil {
		t.Fatal(err)
	}
	if _, writeErr := manifestTmp.Write(newManifest); writeErr != nil {
		t.Fatal(writeErr)
	}
	manifestTmp.Close()

	// Stage a change to the hosts file.
	stagedHosts, err := StageEncryptedYAML(context.Background(), hostsPath, "", func(p []byte) ([]byte, error) {
		return append(p, []byte("    wireguard_private_key: fake\n")...), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Sabotage the staged tempfile so stagedHosts.Commit fails (ENOENT on Rename).
	if rmErr := os.Remove(stagedHosts.TempPath); rmErr != nil {
		t.Fatal(rmErr)
	}

	err = CommitManifestAndHosts(manifestPath, manifestTmp.Name(), originalManifest, stagedHosts)
	if err == nil {
		t.Fatal("expected CommitManifestAndHosts to return an error when hosts commit fails")
	}
	if !strings.Contains(err.Error(), "manifest rolled back") {
		t.Fatalf("expected rollback message in error, got: %v", err)
	}

	// Manifest must be reverted to its pre-call bytes.
	got, readErr := os.ReadFile(manifestPath)
	if readErr != nil {
		t.Fatalf("read manifest post-rollback: %v", readErr)
	}
	if string(got) != string(originalManifest) {
		t.Fatalf("manifest not rolled back. got:\n%s\nwant:\n%s", got, originalManifest)
	}

	// Hosts file must be untouched.
	gotHosts, _ := os.ReadFile(hostsPath)
	if string(gotHosts) != string(originalHosts) {
		t.Fatalf("hosts file modified despite failed commit. got:\n%s", gotHosts)
	}
}

func TestCommitManifestAndHostsHappyPath(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "cluster.yaml")
	hostsPath := filepath.Join(dir, "hosts.enc.yaml")

	if err := os.WriteFile(manifestPath, []byte("old manifest\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hostsPath, []byte("hosts:\n  a: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	newManifest := []byte("new manifest\n")
	manifestTmp, _ := os.CreateTemp(dir, ".cluster-*.yaml.tmp")
	manifestTmp.Write(newManifest)
	manifestTmp.Close()

	stagedHosts, err := StageEncryptedYAML(context.Background(), hostsPath, "", func(p []byte) ([]byte, error) {
		return append(p, []byte("  b: {}\n")...), nil
	})
	if err != nil {
		t.Fatal(err)
	}

	backup, _ := os.ReadFile(manifestPath)
	if err := CommitManifestAndHosts(manifestPath, manifestTmp.Name(), backup, stagedHosts); err != nil {
		t.Fatalf("happy-path commit: %v", err)
	}

	gotManifest, _ := os.ReadFile(manifestPath)
	if string(gotManifest) != string(newManifest) {
		t.Fatalf("manifest not committed: %q", gotManifest)
	}
	gotHosts, _ := os.ReadFile(hostsPath)
	if !strings.Contains(string(gotHosts), "b: {}") {
		t.Fatalf("hosts not committed: %q", gotHosts)
	}
}
