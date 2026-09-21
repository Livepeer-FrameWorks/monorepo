package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestReleaseWorkflowKeepsCandidatesOutOfLatest(t *testing.T) {
	data, err := os.ReadFile("../../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Uses string            `yaml:"uses"`
				With map[string]string `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	for _, job := range workflow.Jobs {
		for _, step := range job.Steps {
			if !strings.HasPrefix(step.Uses, "softprops/action-gh-release@") {
				continue
			}
			if step.With["prerelease"] != "${{ contains(github.ref_name, '-rc') }}" || step.With["make_latest"] != "${{ contains(github.ref_name, '-rc') && 'false' || 'legacy' }}" {
				t.Fatalf("RCs must be prereleases and never latest: %+v", step.With)
			}
			return
		}
	}
	t.Fatal("release publication step not found")
}

func TestUpdateReleaseChannelsStableAdvancesCandidateAndStable(t *testing.T) {
	dir := releaseChannelsFixture(t, "v1.2.2", "v1.2.3")
	updates, err := UpdateReleaseChannels(dir, "v1.2.3", time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 2 || updates[0].Channel != "candidate" || updates[1].Channel != "stable" {
		t.Fatalf("updates = %+v", updates)
	}
	assertChannelVersion(t, dir, "candidate", "v1.2.3")
	assertChannelVersion(t, dir, "stable", "v1.2.3")
}

func TestUpdateReleaseChannelsRCLeavesStableAlone(t *testing.T) {
	dir := releaseChannelsFixture(t, "v1.2.2", "v1.2.3-rc1")
	writeChannel(t, dir, "stable", "v1.2.2")
	if _, err := UpdateReleaseChannels(dir, "v1.2.3-rc1", time.Now()); err != nil {
		t.Fatal(err)
	}
	assertChannelVersion(t, dir, "candidate", "v1.2.3-rc1")
	assertChannelVersion(t, dir, "rc", "v1.2.3-rc1")
	assertChannelVersion(t, dir, "stable", "v1.2.2")
}

func TestUpdateReleaseChannelsNeverRegressesPointers(t *testing.T) {
	dir := releaseChannelsFixture(t, "v1.2.2", "v1.2.3-rc1")
	writeChannel(t, dir, "candidate", "v1.2.3")
	writeChannel(t, dir, "rc", "v1.2.3-rc2")
	updates, err := UpdateReleaseChannels(dir, "v1.2.3-rc1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, update := range updates {
		if update.Updated {
			t.Fatalf("unexpected regression: %+v", updates)
		}
	}
	assertChannelVersion(t, dir, "candidate", "v1.2.3")
	assertChannelVersion(t, dir, "rc", "v1.2.3-rc2")
}

func releaseChannelsFixture(t *testing.T, manifests ...string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "releases"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tag := range manifests {
		if err := os.WriteFile(filepath.Join(dir, "releases", tag+".yaml"), []byte("platform_version: "+tag+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func writeChannel(t *testing.T, dir, channel, tag string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "channels"), 0o755); err != nil {
		t.Fatal(err)
	}
	contents := "platform_version: " + tag + "\nmanifest: releases/" + tag + ".yaml\nupdated_at: 2026-01-01T00:00:00Z\n"
	if err := os.WriteFile(filepath.Join(dir, "channels", channel+".yaml"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertChannelVersion(t *testing.T, dir, channel, tag string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "channels", channel+".yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "platform_version: "+tag+"\n") {
		t.Fatalf("%s = %q, want platform_version %s", channel, data, tag)
	}
}
