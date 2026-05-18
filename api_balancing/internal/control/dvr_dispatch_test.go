package control

import (
	"path/filepath"
	"testing"

	"frameworks/api_balancing/internal/state"
)

// TestIsActiveDVRStatus enforces the lifecycle status set that gates
// active DVR routing: any of these → the rolling DVR surface fed by
// the recording origin's local artefacts; anything else → the stopped
// DVR resolver falls back to the most-recent playable chapter's VOD
// playback ID. The set must stay in sync with foghorn.artifacts.status
// semantics (see schema/foghorn.sql).
func TestIsActiveDVRStatus(t *testing.T) {
	active := []string{"requested", "starting", "recording"}
	for _, s := range active {
		if !IsActiveDVRStatus(s) {
			t.Errorf("IsActiveDVRStatus(%q) = false, want true", s)
		}
	}
	// 'finalizing' is excluded: FinalizeDVR has claimed the stop, the
	// rolling manifest is closing, and the stopped-DVR resolver should
	// fall back to the latest playable chapter.
	notActive := []string{"", "finalizing", "completed", "completed_partial", "failed", "deleted", "ready", "anything"}
	for _, s := range notActive {
		if IsActiveDVRStatus(s) {
			t.Errorf("IsActiveDVRStatus(%q) = true, want false", s)
		}
	}
}

// TestLocalRollingDVRManifestPath verifies the on-disk layout
// constructed for the recording origin's rolling DVR manifest. The
// path shape must match what the Mist push writer produces (see
// dvr_manager.go: targetURI uses `<outputDir>/<dvr_hash>.m3u8` with
// outputDir = storage/dvr/<stream_internal_name>/<dvr_hash>/).
func TestLocalRollingDVRManifestPath(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	defer sm.Shutdown()
	const nodeID = "node-recording-1"
	const storageRoot = "/srv/frameworks/storage"
	sm.SetNodeStoragePaths(nodeID, storageRoot, "", "")

	cases := []struct {
		name       string
		streamName string
		dvrHash    string
		node       string
		want       string
	}{
		{
			name:       "happy path",
			streamName: "stream_abc",
			dvrHash:    "fedcba98",
			node:       nodeID,
			want:       filepath.Join(storageRoot, "dvr", "stream_abc", "fedcba98", "fedcba98.m3u8"),
		},
		{
			name:       "unknown node falls back to defaultStorageBase",
			streamName: "stream_abc",
			dvrHash:    "fedcba98",
			node:       "node-does-not-exist",
			want:       filepath.Join(defaultStorageBase, "dvr", "stream_abc", "fedcba98", "fedcba98.m3u8"),
		},
		{
			name:       "missing stream name returns empty",
			streamName: "",
			dvrHash:    "fedcba98",
			node:       nodeID,
			want:       "",
		},
		{
			name:       "missing dvr hash returns empty",
			streamName: "stream_abc",
			dvrHash:    "",
			node:       nodeID,
			want:       "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := LocalRollingDVRManifestPath(tc.streamName, tc.dvrHash, tc.node)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
