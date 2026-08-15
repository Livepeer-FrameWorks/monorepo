package control

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// A DVR recording started through StartRecording durably persists its source
// internal_name (distinct from the stream_id path component of
// /storage/dvr/{stream_id}/{dvr_hash}/), and a fresh manager restores it on
// recovery so LookupActiveDVRByInternalName resolves the rolling-DVR playback
// token dvr+<internal_name>.
func TestStartRecordingPersistsInternalNameThenRecoveryResolves(t *testing.T) {
	clearConn()
	useFastInitialPushRetry(t)

	const (
		dvrHash      = "hash-active-XYZ"
		streamID     = "stream-id-123"  // path component under /storage/dvr/
		internalName = "internal-abc-9" // real Mist internal_name — DISTINCT
	)
	storagePath := t.TempDir()
	outputDir := filepath.Join(storagePath, "dvr", streamID, dvrHash)
	segmentsDir := filepath.Join(outputDir, "segments")
	manifestPath := filepath.Join(outputDir, dvrHash+".m3u8")

	// Start through the REAL StartRecording path. In production this runs FIRST,
	// before Mist has produced anything: StartRecording creates the directory
	// fresh and writes the job.json identity sidecar. startAwareFakeMist makes
	// the started push visible to the warmup PushList so StartRecording completes.
	dm1 := &DVRManager{
		logger:      logging.NewLogger(),
		jobs:        make(map[string]*DVRJob),
		storagePath: storagePath,
		mistClient:  &startAwareFakeMist{pushIDToReturn: 7},
		diskCheck:   func(string, uint64) error { return nil },
	}
	if err := dm1.StartRecording(dvrHash, streamID, internalName, "live+"+internalName, "", &ipcpb.DVRConfig{SegmentDuration: 6}, nil); err != nil {
		t.Fatalf("StartRecording: %v", err)
	}
	// The identity sidecar must be durable on disk before Mist produces any
	// segments — recovery keys dvr+<internal_name> off it, and stream_id is not
	// a substitute.
	if name, ok := readDVRJobInternalName(outputDir); !ok || name != internalName {
		t.Fatalf("StartRecording must persist internal_name durably; got (%q, ok=%v)", name, ok)
	}

	// Mist now produces the recording surface (segments + manifest) into the
	// directory StartRecording created, so recovery can find and measure it.
	if err := os.MkdirAll(segmentsDir, 0755); err != nil {
		t.Fatalf("mkdir segments: %v", err)
	}
	if err := os.WriteFile(manifestPath, []byte("#EXTM3U\n#EXTINF:6.000,\nsegments/seg0.ts\n"), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(segmentsDir, "seg0.ts"), []byte("seg"), 0644); err != nil {
		t.Fatalf("write segment: %v", err)
	}

	// Restart: a fresh manager recovers the job from disk + the still-active push.
	dm2 := &DVRManager{
		logger:      logging.NewLogger(),
		jobs:        make(map[string]*DVRJob),
		storagePath: storagePath,
		mistClient: &fakeMistClient{pushListItems: []mist.PushInfo{{
			ID:         7,
			StreamName: "live+" + internalName,
			TargetURI:  filepath.Join(segmentsDir, "$segmentCounter.ts") + "#m3u8=../" + dvrHash + ".m3u8",
		}}},
	}
	withTestDVRManager(t, dm2)
	if err := dm2.recoverActiveDVRJobsFromMist(storagePath, logging.NewLogger()); err != nil {
		t.Fatalf("recoverActiveDVRJobsFromMist: %v", err)
	}

	hash, manifest, ok := LookupActiveDVRByInternalName(internalName)
	if !ok {
		t.Fatalf("LookupActiveDVRByInternalName(%q) missed after recovery through StartRecording", internalName)
	}
	if hash != dvrHash || manifest != manifestPath {
		t.Fatalf("resolved (%q, %q), want (%q, %q)", hash, manifest, dvrHash, manifestPath)
	}
	// The stream_id must NOT resolve — recovery never substitutes it.
	if _, _, ok := LookupActiveDVRByInternalName(streamID); ok {
		t.Fatalf("stream_id %q must not resolve; recovery must not substitute it for internal_name", streamID)
	}
}

// StartRecording rejects an empty source internal_name before creating anything:
// a DVR recording keyed by dvr+<internal_name> cannot be recovered without it.
func TestStartRecordingRejectsEmptyInternalName(t *testing.T) {
	clearConn()
	storagePath := t.TempDir()
	fake := &fakeMistClient{}
	dm := &DVRManager{
		logger:      logging.NewLogger(),
		jobs:        make(map[string]*DVRJob),
		storagePath: storagePath,
		mistClient:  fake,
	}
	if err := dm.StartRecording("hash-empty", "stream-empty", "   ", "live+x", "", &ipcpb.DVRConfig{}, nil); err == nil {
		t.Fatal("StartRecording must reject an empty/whitespace internal_name")
	}
	if _, ok := dm.jobs["hash-empty"]; ok {
		t.Fatal("no job must be stored for an empty internal_name")
	}
	if atomic.LoadInt64(&fake.pushStartCalls) != 0 {
		t.Fatal("no Mist push may start for an empty internal_name")
	}
	if _, err := os.Stat(filepath.Join(storagePath, "dvr", "stream-empty")); !os.IsNotExist(err) {
		t.Fatal("no on-disk directory may be created for an empty internal_name")
	}
}

var errInjectedMetaWrite = errors.New("injected metadata persist failure")

// mkdirAllDurable must create every missing ancestor (not just the leaf's
// parent) and report exactly the directories it created, so a failed start can
// roll back precisely its own contribution and nothing pre-existing.
func TestMkdirAllDurableCreatesHierarchyAndRollsBack(t *testing.T) {
	base := t.TempDir()

	// Nothing under base/dvr exists yet: creating base/dvr/s/h creates 3 levels.
	target := filepath.Join(base, "dvr", "s", "h")
	created, err := mkdirAllDurable(target)
	if err != nil {
		t.Fatalf("mkdirAllDurable: %v", err)
	}
	want := []string{filepath.Join(base, "dvr"), filepath.Join(base, "dvr", "s"), target}
	if len(created) != len(want) {
		t.Fatalf("created = %v, want the 3 new levels %v", created, want)
	}
	for i, p := range want {
		if created[i] != p {
			t.Fatalf("created[%d] = %q, want %q (shallowest first)", i, created[i], p)
		}
		if info, statErr := os.Stat(p); statErr != nil || !info.IsDir() {
			t.Fatalf("expected %q to be a created directory, stat err=%v", p, statErr)
		}
	}

	// Rolling back the created dirs removes the whole subtree but keeps base.
	removeCreatedDirs(created, func(p string, rmErr error) { t.Fatalf("unexpected preserve of %q: %v", p, rmErr) })
	if _, statErr := os.Stat(filepath.Join(base, "dvr")); !os.IsNotExist(statErr) {
		t.Fatalf("created subtree must be gone after rollback, stat err=%v", statErr)
	}
	if _, statErr := os.Stat(base); statErr != nil {
		t.Fatalf("pre-existing base must survive rollback, got %v", statErr)
	}

	// A directory holding foreign content is PRESERVED by the non-recursive
	// rollback (never RemoveAll), so a live push's segments are never wiped.
	target2 := filepath.Join(base, "dvr2", "s", "h")
	created2, err := mkdirAllDurable(target2)
	if err != nil {
		t.Fatalf("mkdirAllDurable (2): %v", err)
	}
	if wErr := os.WriteFile(filepath.Join(target2, "seg0.ts"), []byte("live"), 0644); wErr != nil {
		t.Fatalf("write foreign content: %v", wErr)
	}
	preserved := ""
	removeCreatedDirs(created2, func(p string, _ error) { preserved = p })
	if preserved != target2 {
		t.Fatalf("non-empty leaf must be preserved, preserved=%q want %q", preserved, target2)
	}
	if _, statErr := os.Stat(filepath.Join(target2, "seg0.ts")); statErr != nil {
		t.Fatalf("foreign content must survive rollback, got %v", statErr)
	}

	// With an existing parent, only the leaf is created and reported.
	existingParent := filepath.Join(base, "dvr3", "s")
	if mkErr := os.MkdirAll(existingParent, 0755); mkErr != nil {
		t.Fatalf("seed parent: %v", mkErr)
	}
	leaf := filepath.Join(existingParent, "h2")
	created3, err := mkdirAllDurable(leaf)
	if err != nil {
		t.Fatalf("mkdirAllDurable (existing parent): %v", err)
	}
	if len(created3) != 1 || created3[0] != leaf {
		t.Fatalf("created = %v, want just the leaf %q", created3, leaf)
	}
	if _, statErr := os.Stat(existingParent); statErr != nil {
		t.Fatalf("existing parent must be untouched, got %v", statErr)
	}
}

// seedExistingDVRDir lays down an on-disk recording (manifest + segment, and
// optionally an identity sidecar) as recovery would find it, returning the
// output dir and a sentinel segment path to assert preservation.
func seedExistingDVRDir(t *testing.T, storagePath, streamID, dvrHash, persistedIdentity string, corruptMeta bool) (outputDir, sentinelSeg string) {
	t.Helper()
	outputDir = filepath.Join(storagePath, "dvr", streamID, dvrHash)
	segmentsDir := filepath.Join(outputDir, "segments")
	if err := os.MkdirAll(segmentsDir, 0755); err != nil {
		t.Fatalf("mkdir segments: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outputDir, dvrHash+".m3u8"), []byte("#EXTM3U\n"), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	sentinelSeg = filepath.Join(segmentsDir, "seg0.ts")
	if err := os.WriteFile(sentinelSeg, []byte("existing-data"), 0644); err != nil {
		t.Fatalf("write sentinel segment: %v", err)
	}
	switch {
	case corruptMeta:
		if err := os.WriteFile(filepath.Join(outputDir, dvrJobMetaFile), []byte("{not-json"), 0644); err != nil {
			t.Fatalf("write corrupt metadata: %v", err)
		}
	case persistedIdentity != "":
		// Write the full versioned descriptor (writeDVRJobMeta stamps the version);
		// a partial internal-name-only sidecar is intentionally rejected now.
		if err := writeDVRJobMeta(outputDir, dvrJobMeta{
			InternalName:      persistedIdentity,
			SourceRuntimeName: "live+" + persistedIdentity,
		}); err != nil {
			t.Fatalf("seed identity sidecar: %v", err)
		}
	}
	return outputDir, sentinelSeg
}

func assertSentinelSurvives(t *testing.T, outputDir, sentinelSeg string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(outputDir, filepath.Base(outputDir)+".m3u8")); err != nil {
		// manifest is named <hash>.m3u8; hash == base(outputDir).
		t.Fatalf("pre-existing manifest must survive, got %v", err)
	}
	if data, err := os.ReadFile(sentinelSeg); err != nil || string(data) != "existing-data" {
		t.Fatalf("pre-existing segment must survive intact, got data=%q err=%v", string(data), err)
	}
}

// Exact-identity retry with a LIVE push adopts the existing recording
// idempotently: no duplicate push, no metadata rewrite, job registered.
func TestStartRecordingAdoptsExactRetryWithLivePush(t *testing.T) {
	clearConn()
	const (
		dvrHash      = "hash-adopt"
		streamID     = "stream-adopt"
		internalName = "internal-adopt"
	)
	storagePath := t.TempDir()
	outputDir, sentinelSeg := seedExistingDVRDir(t, storagePath, streamID, dvrHash, internalName, false)

	fake := &fakeMistClient{pushListItems: []mist.PushInfo{{
		ID:         11,
		StreamName: "live+" + internalName,
		TargetURI:  filepath.Join(outputDir, "segments", "$segmentCounter.ts") + "#m3u8=../" + dvrHash + ".m3u8",
	}}}
	const sourceURL = "dtsc://remote-node/live+" + internalName
	ClearDVRSourceOverride("live+" + internalName)
	dm := &DVRManager{logger: logging.NewLogger(), jobs: make(map[string]*DVRJob), storagePath: storagePath, mistClient: fake}
	if err := dm.StartRecording(dvrHash, streamID, internalName, "live+"+internalName, sourceURL, &ipcpb.DVRConfig{}, nil); err != nil {
		t.Fatalf("exact retry with a live push must adopt idempotently, got %v", err)
	}
	if _, ok := dm.jobs[dvrHash]; !ok {
		t.Fatal("adopted recording must be registered in the job map")
	}
	if atomic.LoadInt64(&fake.pushStartCalls) != 0 {
		t.Fatal("adoption must NOT start a duplicate push")
	}
	// The re-dispatched descriptor must re-register the source override so a later
	// push recreation keeps the remote DVR source.
	if got, ok := GetDVRSourceOverride("live+" + internalName); !ok || got != sourceURL {
		t.Fatalf("adoption must re-register the DVR source override, got (%q, ok=%v)", got, ok)
	}
	assertSentinelSurvives(t, outputDir, sentinelSeg)
}

// An exact-identity directory that already contains recorded media but has NO
// live push is a completed/interrupted recording. Restarting a push over it
// would open a second append writer and corrupt the rolling manifest, so
// StartRecording must fail closed and preserve the media (never restart).
func TestStartRecordingFailsOnExistingMediaWithoutPush(t *testing.T) {
	clearConn()
	const (
		dvrHash      = "hash-media"
		streamID     = "stream-media"
		internalName = "internal-media"
	)
	storagePath := t.TempDir()
	// seedExistingDVRDir writes a matching identity sidecar + a .ts segment (media).
	outputDir, sentinelSeg := seedExistingDVRDir(t, storagePath, streamID, dvrHash, internalName, false)

	fake := &fakeMistClient{} // empty PushList → no live push to adopt
	dm := &DVRManager{logger: logging.NewLogger(), jobs: make(map[string]*DVRJob), storagePath: storagePath, mistClient: fake}
	if err := dm.StartRecording(dvrHash, streamID, internalName, "live+"+internalName, "", &ipcpb.DVRConfig{}, nil); err == nil {
		t.Fatal("StartRecording must fail closed on existing media with no live push")
	}
	if _, ok := dm.jobs[dvrHash]; ok {
		t.Fatal("no job may be registered for a completed recording")
	}
	if atomic.LoadInt64(&fake.pushStartCalls) != 0 {
		t.Fatal("no push may start over existing media")
	}
	assertSentinelSurvives(t, outputDir, sentinelSeg)
}

// A pre-existing directory whose persisted identity differs fails closed and
// preserves the existing recording.
func TestStartRecordingFailsOnIdentityMismatch(t *testing.T) {
	clearConn()
	const (
		dvrHash  = "hash-mismatch"
		streamID = "stream-mismatch"
	)
	storagePath := t.TempDir()
	outputDir, sentinelSeg := seedExistingDVRDir(t, storagePath, streamID, dvrHash, "internal-OTHER", false)

	fake := &fakeMistClient{}
	dm := &DVRManager{logger: logging.NewLogger(), jobs: make(map[string]*DVRJob), storagePath: storagePath, mistClient: fake}
	if err := dm.StartRecording(dvrHash, streamID, "internal-REQUESTED", "live+x", "", &ipcpb.DVRConfig{}, nil); err == nil {
		t.Fatal("StartRecording must fail closed on an identity mismatch")
	}
	if _, ok := dm.jobs[dvrHash]; ok {
		t.Fatal("no job must be stored on identity mismatch")
	}
	if atomic.LoadInt64(&fake.pushStartCalls) != 0 {
		t.Fatal("no push may start on identity mismatch")
	}
	assertSentinelSurvives(t, outputDir, sentinelSeg)
}

// A pre-existing directory with missing OR corrupt identity fails closed and
// preserves the existing recording.
func TestStartRecordingFailsOnMissingOrCorruptMetadata(t *testing.T) {
	for _, tc := range []struct {
		name    string
		corrupt bool
	}{{"missing", false}, {"corrupt", true}} {
		t.Run(tc.name, func(t *testing.T) {
			clearConn()
			dvrHash, streamID := "hash-"+tc.name, "stream-"+tc.name
			storagePath := t.TempDir()
			// For "missing", persistedIdentity="" and corrupt=false → no sidecar.
			outputDir, sentinelSeg := seedExistingDVRDir(t, storagePath, streamID, dvrHash, "", tc.corrupt)

			fake := &fakeMistClient{}
			dm := &DVRManager{logger: logging.NewLogger(), jobs: make(map[string]*DVRJob), storagePath: storagePath, mistClient: fake}
			if err := dm.StartRecording(dvrHash, streamID, "internal-x", "live+x", "", &ipcpb.DVRConfig{}, nil); err == nil {
				t.Fatalf("StartRecording must fail closed on %s metadata", tc.name)
			}
			if _, ok := dm.jobs[dvrHash]; ok {
				t.Fatal("no job must be stored")
			}
			if atomic.LoadInt64(&fake.pushStartCalls) != 0 {
				t.Fatal("no push may start")
			}
			assertSentinelSurvives(t, outputDir, sentinelSeg)
		})
	}
}

// A genuinely NEW recording whose metadata persist fails removes only the
// directory THIS call created (deterministic fault injection via metaWriter).
func TestStartRecordingRemovesOwnDirOnMetadataFailure(t *testing.T) {
	clearConn()
	const (
		dvrHash      = "hash-fresh"
		streamID     = "stream-fresh"
		internalName = "internal-fresh"
	)
	storagePath := t.TempDir()
	fake := &fakeMistClient{}
	dm := &DVRManager{
		logger:      logging.NewLogger(),
		jobs:        make(map[string]*DVRJob),
		storagePath: storagePath,
		mistClient:  fake,
		metaWriter:  func(string, dvrJobMeta) error { return errInjectedMetaWrite },
	}
	if err := dm.StartRecording(dvrHash, streamID, internalName, "live+"+internalName, "", &ipcpb.DVRConfig{}, nil); err == nil {
		t.Fatal("StartRecording must fail when metadata cannot be persisted")
	}
	if _, err := os.Stat(filepath.Join(storagePath, "dvr", streamID, dvrHash)); !os.IsNotExist(err) {
		t.Fatalf("a freshly created dir must be removed on metadata failure, stat err = %v", err)
	}
	if atomic.LoadInt64(&fake.pushStartCalls) != 0 {
		t.Fatal("no push may start when metadata persistence fails")
	}
}

// A genuinely NEW recording whose push fails AFTER metadata persist removes the
// directory this call created.
func TestStartRecordingRemovesOwnDirOnPushFailure(t *testing.T) {
	clearConn()
	useFastInitialPushRetry(t)
	const (
		dvrHash      = "hash-pushfail"
		streamID     = "stream-pushfail"
		internalName = "internal-pushfail"
	)
	storagePath := t.TempDir()
	dm := &DVRManager{
		logger:      logging.NewLogger(),
		jobs:        make(map[string]*DVRJob),
		storagePath: storagePath,
		mistClient:  &startAwareFakeMist{pushStartErr: errors.New("stream ended before DVR start")},
	}
	if err := dm.StartRecording(dvrHash, streamID, internalName, "live+"+internalName, "", &ipcpb.DVRConfig{}, nil); err == nil {
		t.Fatal("StartRecording must fail when the push cannot start")
	}
	if _, ok := dm.jobs[dvrHash]; ok {
		t.Fatal("no job must be stored on push failure")
	}
	if _, err := os.Stat(filepath.Join(storagePath, "dvr", streamID, dvrHash)); !os.IsNotExist(err) {
		t.Fatalf("the created dir must be removed on push failure, stat err = %v", err)
	}
}

// Recovery with missing or corrupt metadata fails closed: the active job is
// still tracked for its lifecycle, but it is resolvable by NEITHER the real
// internal_name nor the stream_id — so the poller installs degraded cleanup
// protection instead of a false identity.
func TestRecoverActiveDVRJobsFromMist_MissingOrCorruptMetadataIsNotRecovered(t *testing.T) {
	clearConn()

	const (
		dvrHash  = "hash-nometa"
		streamID = "stream-nometa"
		realName = "internal-nometa"
	)
	storagePath := t.TempDir()
	outputDir := filepath.Join(storagePath, "dvr", streamID, dvrHash)
	segmentsDir := filepath.Join(outputDir, "segments")
	if err := os.MkdirAll(segmentsDir, 0755); err != nil {
		t.Fatalf("mkdir segments: %v", err)
	}
	manifestPath := filepath.Join(outputDir, dvrHash+".m3u8")
	if err := os.WriteFile(manifestPath, []byte("#EXTM3U\n#EXTINF:6.000,\nsegments/seg0.ts\n"), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(segmentsDir, "seg0.ts"), []byte("seg"), 0644); err != nil {
		t.Fatalf("write segment: %v", err)
	}
	// No job.json is written — metadata is missing.

	dm := &DVRManager{
		logger:      logging.NewLogger(),
		jobs:        make(map[string]*DVRJob),
		storagePath: storagePath,
		mistClient: &fakeMistClient{pushListItems: []mist.PushInfo{{
			ID:         9,
			StreamName: "live+" + realName,
			TargetURI:  filepath.Join(segmentsDir, "$segmentCounter.ts") + "#m3u8=../" + dvrHash + ".m3u8",
		}}},
	}
	withTestDVRManager(t, dm)
	if err := dm.recoverActiveDVRJobsFromMist(storagePath, logging.NewLogger()); err != nil {
		t.Fatalf("recoverActiveDVRJobsFromMist: %v", err)
	}

	// Without a valid descriptor recovery CANNOT match the push to an identity, so
	// it must NOT adopt it (a foreign push could share the hash). The job is not
	// tracked; the poller's degraded protection is what pauses cleanup for the dir.
	if _, ok := dm.jobs[dvrHash]; ok {
		t.Fatal("recovery must not adopt a push for a directory with no valid descriptor")
	}
	if _, _, ok := LookupActiveDVRByInternalName(realName); ok {
		t.Fatal("missing metadata must not resolve to the real internal_name")
	}
	if _, _, ok := LookupActiveDVRByInternalName(streamID); ok {
		t.Fatal("missing metadata must not resolve to the stream_id either")
	}

	// Corrupt metadata is rejected the same way (readDVRJobInternalName reports
	// not-ok, so the caller leaves InternalName empty → unresolved).
	if err := os.WriteFile(filepath.Join(outputDir, dvrJobMetaFile), []byte("{not-json"), 0644); err != nil {
		t.Fatalf("write corrupt metadata: %v", err)
	}
	if name, ok := readDVRJobInternalName(outputDir); ok {
		t.Fatalf("corrupt metadata must be rejected, got %q", name)
	}
}

// An established recording (start → progress-to-recording) must survive a
// Helmsman restart with its full descriptor intact — recovery restores
// SourceURL/config and RE-PINS the source override, because Foghorn never
// re-dispatches a `recording` row.
func TestDVRRestartRecoversDescriptorAndRePinsOverride(t *testing.T) {
	clearConn()
	useFastInitialPushRetry(t)
	const (
		dvrHash      = "hash-desc"
		streamID     = "stream-desc"
		internalName = "internal-desc"
	)
	runtimeName := "live+" + internalName
	sourceURL := "dtsc://remote-node/" + runtimeName
	ClearDVRSourceOverride(runtimeName)
	storagePath := t.TempDir()
	outputDir := filepath.Join(storagePath, "dvr", streamID, dvrHash)
	segmentsDir := filepath.Join(outputDir, "segments")
	manifestPath := filepath.Join(outputDir, dvrHash+".m3u8")

	// Start: persists the full descriptor sidecar and goes to recording.
	dm1 := &DVRManager{
		logger:      logging.NewLogger(),
		jobs:        make(map[string]*DVRJob),
		storagePath: storagePath,
		mistClient:  &startAwareFakeMist{pushIDToReturn: 7},
		diskCheck:   func(string, uint64) error { return nil },
	}
	cfg := &ipcpb.DVRConfig{SegmentDuration: 6, DvrWindowSeconds: 3600, MaxEntries: 600}
	if err := dm1.StartRecording(dvrHash, streamID, internalName, runtimeName, sourceURL, cfg, nil); err != nil {
		t.Fatalf("StartRecording: %v", err)
	}
	if meta, ok := readDVRJobMeta(outputDir); !ok || meta.SourceURL != sourceURL || meta.SourceRuntimeName != runtimeName || meta.SegmentDuration != 6 {
		t.Fatalf("descriptor must be durably persisted, got %+v ok=%v", meta, ok)
	}

	// Restart: the override map is empty (process restarted) and Mist has produced
	// the on-disk recording surface.
	ClearDVRSourceOverride(runtimeName)
	if err := os.MkdirAll(segmentsDir, 0755); err != nil {
		t.Fatalf("mkdir segments: %v", err)
	}
	if err := os.WriteFile(manifestPath, []byte("#EXTM3U\n#EXTINF:6.000,\nsegments/seg0.ts\n"), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(segmentsDir, "seg0.ts"), []byte("seg"), 0644); err != nil {
		t.Fatalf("write segment: %v", err)
	}

	dm2 := &DVRManager{
		logger:      logging.NewLogger(),
		jobs:        make(map[string]*DVRJob),
		storagePath: storagePath,
		mistClient: &fakeMistClient{pushListItems: []mist.PushInfo{{
			ID:         7,
			StreamName: runtimeName,
			TargetURI:  filepath.Join(segmentsDir, "$segmentCounter.ts") + "#m3u8=../" + dvrHash + ".m3u8",
		}}},
	}
	withTestDVRManager(t, dm2)
	if err := dm2.recoverActiveDVRJobsFromMist(storagePath, logging.NewLogger()); err != nil {
		t.Fatalf("recoverActiveDVRJobsFromMist: %v", err)
	}

	// The override is re-pinned so a later push recreation keeps the remote source.
	if got, ok := GetDVRSourceOverride(runtimeName); !ok || got != sourceURL {
		t.Fatalf("recovery must re-pin the source override, got (%q, ok=%v)", got, ok)
	}
	// Descriptor restored onto the recovered job.
	dm2.mutex.RLock()
	job := dm2.jobs[dvrHash]
	dm2.mutex.RUnlock()
	if job == nil {
		t.Fatal("recovered job must be registered")
	}
	if job.SourceURL != sourceURL || job.Config.GetSegmentDuration() != 6 {
		t.Fatalf("recovered job descriptor = {url:%q seg:%d}, want {%q 6}", job.SourceURL, job.Config.GetSegmentDuration(), sourceURL)
	}
}

// ambiguousPushFakeMist accepts PushStart (the push goes live) but reports it
// under a StreamName that the warmup's dvrPushMatches will NOT match, so
// startDVRPush fails to CONFIRM the push even though it is live. findDVRPush
// (by dvr_hash) still finds it — the ambiguous post-PushStart failure.
// foreignPushFakeMist accepts PushStart (a push goes live) but PushList only
// ever reports a push under a DIFFERENT runtime — so the accepted push is never
// confirmed AND no exact-identity push exists to adopt.
type foreignPushFakeMist struct {
	dvrHash        string
	started        bool
	pushStartCalls int64
}

func (a *foreignPushFakeMist) PushStart(streamName, targetURI string) error {
	atomic.AddInt64(&a.pushStartCalls, 1)
	a.started = true
	return nil
}
func (a *foreignPushFakeMist) PushStop(int) error { return nil }
func (a *foreignPushFakeMist) PushList() ([]mist.PushInfo, error) {
	if !a.started {
		return nil, nil
	}
	return []mist.PushInfo{{
		ID:         99,
		StreamName: "live+SOMEONE-ELSE",
		TargetURI:  "/storage/dvr/s/" + a.dvrHash + "/segments/$c.ts#m3u8=../" + a.dvrHash + ".m3u8",
	}}, nil
}

// An accepted-but-unconfirmed start must NOT adopt a foreign push that merely
// shares the hash — that would map our recording to someone else's runtime. The
// job registers as "starting" with an unknown PushID (the monitor reconciles it
// by exact identity), the directory is preserved (a live push may be writing),
// and no PushStart is retried after acceptance.
func TestStartRecordingDoesNotAdoptForeignPushOnAmbiguousStart(t *testing.T) {
	clearConn()
	useFastInitialPushRetry(t)
	const (
		dvrHash      = "hash-ambig"
		streamID     = "stream-ambig"
		internalName = "internal-ambig"
	)
	runtimeName := "live+" + internalName
	sourceURL := "dtsc://remote/" + runtimeName
	ClearDVRSourceOverride(runtimeName)
	storagePath := t.TempDir()
	outputDir := filepath.Join(storagePath, "dvr", streamID, dvrHash)

	fake := &foreignPushFakeMist{dvrHash: dvrHash}
	dm := &DVRManager{
		logger:      logging.NewLogger(),
		jobs:        make(map[string]*DVRJob),
		storagePath: storagePath,
		mistClient:  fake,
		diskCheck:   func(string, uint64) error { return nil },
	}
	if err := dm.StartRecording(dvrHash, streamID, internalName, runtimeName, sourceURL, &ipcpb.DVRConfig{}, nil); err != nil {
		t.Fatalf("accepted-but-unconfirmed start must not error, got %v", err)
	}
	dm.mutex.RLock()
	job := dm.jobs[dvrHash]
	dm.mutex.RUnlock()
	if job == nil {
		t.Fatal("job must be registered (a push may be live)")
	}
	if job.PushID == 99 {
		t.Fatal("must NOT adopt the foreign push (id 99) — that mixes identities")
	}
	if job.PushID != 0 || job.Status != "starting" {
		t.Fatalf("unconfirmed job must be {PushID:0, Status:starting}, got {%d,%q}", job.PushID, job.Status)
	}
	// PushStart must have been issued exactly ONCE (non-idempotent; no retry after
	// acceptance).
	if n := atomic.LoadInt64(&fake.pushStartCalls); n != 1 {
		t.Fatalf("PushStart must be issued exactly once after acceptance, got %d", n)
	}
	// The directory must NOT have been rolled back — a live push may be writing.
	if _, err := os.Stat(filepath.Join(outputDir, dvrJobMetaFile)); err != nil {
		t.Fatalf("output dir must be preserved on an accepted-but-unconfirmed start, stat err=%v", err)
	}
	// Our own override (our runtime → our URL) stays pinned; it is never mixed
	// with the foreign push.
	if got, ok := GetDVRSourceOverride(runtimeName); !ok || got != sourceURL {
		t.Fatalf("our own override must remain, got (%q, ok=%v)", got, ok)
	}
}

// When the accepted push becomes visible under the EXACT identity within the
// window, ensureInitialPush confirms it (no retry, no double-start) and the job
// goes to recording with the real PushID.
func TestStartRecordingConfirmsAcceptedPushByExactIdentity(t *testing.T) {
	clearConn()
	useFastInitialPushRetry(t)
	const (
		dvrHash      = "hash-confirm"
		streamID     = "stream-confirm"
		internalName = "internal-confirm"
	)
	runtimeName := "live+" + internalName
	storagePath := t.TempDir()

	// listEmptyAfterStart delays visibility so PushStart is accepted first, then
	// the push appears under the exact runtime+target and is confirmed.
	fake := &startAwareFakeMist{pushIDToReturn: 42, listEmptyAfterStart: 2}
	dm := &DVRManager{
		logger:      logging.NewLogger(),
		jobs:        make(map[string]*DVRJob),
		storagePath: storagePath,
		mistClient:  fake,
		diskCheck:   func(string, uint64) error { return nil },
	}
	if err := dm.StartRecording(dvrHash, streamID, internalName, runtimeName, "", &ipcpb.DVRConfig{}, nil); err != nil {
		t.Fatalf("StartRecording: %v", err)
	}
	dm.mutex.RLock()
	job := dm.jobs[dvrHash]
	dm.mutex.RUnlock()
	if job == nil || job.PushID != 42 || job.Status != "recording" {
		t.Fatalf("confirmed job must be {PushID:42, Status:recording}, got %+v", job)
	}
	if fake.startCalls != 1 {
		t.Fatalf("PushStart must be issued exactly once, got %d", fake.startCalls)
	}
}

// A descriptor applies only to its exact live runtime. A mismatch must fail closed without
// registering a source override for either name.
func TestReconcileDescriptorFailsClosedOnRuntimeMismatch(t *testing.T) {
	const (
		liveRuntime = "live+new-takeover-source"
		descRuntime = "live+old-source"
	)
	descURL := "dtsc://old-node/" + descRuntime
	ClearDVRSourceOverride(liveRuntime)
	ClearDVRSourceOverride(descRuntime)

	dm := &DVRManager{logger: logging.NewLogger(), jobs: make(map[string]*DVRJob)}
	job := &DVRJob{DVRHash: "hash-mismatch", StreamName: liveRuntime}
	cfg := &ipcpb.DVRConfig{SegmentDuration: 6}

	dm.mutex.Lock()
	dm.reconcileDVRJobDescriptorLocked(job, descRuntime, descURL, cfg)
	dm.mutex.Unlock()

	if got, ok := GetDVRSourceOverride(liveRuntime); ok {
		t.Fatalf("mismatched descriptor must NOT map the live runtime to a stale URL, got %q", got)
	}
	if got, ok := GetDVRSourceOverride(descRuntime); ok {
		t.Fatalf("no override may be registered under the descriptor runtime either, got %q", got)
	}
	// config is source-identity-independent and is still restored.
	if job.Config.GetSegmentDuration() != 6 {
		t.Fatal("config must still be restored on a runtime mismatch")
	}
	// The live push identity must be left untouched.
	if job.SourceURL != "" || job.SourceRuntimeName != "" {
		t.Fatalf("mismatched descriptor must not be copied onto the job, got url=%q runtime=%q", job.SourceURL, job.SourceRuntimeName)
	}
}

// Metadata without its format version and source runtime is not sufficient to resolve a job's
// source identity.
func TestReadDVRJobMetaRejectsPartialSidecar(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, dvrJobMetaFile), []byte(`{"internal_name":"x"}`), 0644); err != nil {
		t.Fatalf("write incomplete sidecar: %v", err)
	}
	if _, ok := readDVRJobMeta(dir); ok {
		t.Fatal("an incomplete sidecar without version/runtime must be rejected")
	}
	// A descriptor missing only the runtime is also rejected.
	if err := os.WriteFile(filepath.Join(dir, dvrJobMetaFile), []byte(`{"version":1,"internal_name":"x"}`), 0644); err != nil {
		t.Fatalf("write runtimeless sidecar: %v", err)
	}
	if _, ok := readDVRJobMeta(dir); ok {
		t.Fatal("a descriptor without source_runtime_name must be rejected")
	}
	// The full versioned descriptor is accepted (writeDVRJobMeta stamps version).
	if err := writeDVRJobMeta(dir, dvrJobMeta{InternalName: "x", SourceRuntimeName: "live+x"}); err != nil {
		t.Fatalf("write full descriptor: %v", err)
	}
	if meta, ok := readDVRJobMeta(dir); !ok || meta.Version != dvrJobMetaVersion || meta.SourceRuntimeName != "live+x" {
		t.Fatalf("full descriptor must be accepted, got %+v ok=%v", meta, ok)
	}
}

// A job stuck in "starting" (an accepted-but-unconfirmed push whose exact
// identity never appears) MUST be stopped by the monitor's one-shot startup
// deadline — the previous time.After-in-select never fired, so such a job could
// live forever holding its job/override/leases.
func TestMonitorStopsUnconfirmedStartingJobAfterTimeout(t *testing.T) {
	clearConn()
	const dvrHash = "hash-stuck"
	storagePath := t.TempDir()
	outputDir := filepath.Join(storagePath, "dvr", "s", dvrHash)
	if err := os.MkdirAll(filepath.Join(outputDir, "segments"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	dm := &DVRManager{
		logger: logging.NewLogger(), jobs: make(map[string]*DVRJob), storagePath: storagePath,
		mistClient: &fakeMistClient{}, diskCheck: func(string, uint64) error { return nil },
		// Per-manager (no global mutation → no race with other monitors). The push
		// ticker fires faster than the startup deadline, so the deadline must still
		// elapse and stop the unconfirmed job.
		startupTimeout:      200 * time.Millisecond,
		pushMonitorInterval: 10 * time.Millisecond,
	}
	// The startup-deadline terminalization is gated on bounded-absence convergence (no push +
	// no segment/byte progress over a real grace). This recording is empty (0 segments, 0
	// bytes) and stays so, so advancing the injected clock by the grace on each observation
	// lets convergence happen within the test window instead of after the wall-clock grace.
	var absObs int64
	dm.nowFn = func() time.Time {
		n := atomic.AddInt64(&absObs, 1)
		return time.Unix(1_000_000, 0).Add(time.Duration(n) * dvrAbsenceGrace)
	}
	job := &DVRJob{
		DVRHash: dvrHash, InternalName: "x", StreamName: "live+x", OutputDir: outputDir,
		Status: "starting", PushID: 0, MaxRetries: MaxDVRRetries,
		Logger: logging.NewLogger(), SendFunc: func(*ipcpb.ControlMessage) {}, SyncedSegments: make(map[string]bool),
	}
	dm.mutex.Lock()
	dm.jobs[dvrHash] = job
	dm.mutex.Unlock()
	go dm.monitorJob(job)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		dm.mutex.RLock()
		_, stillTracked := dm.jobs[dvrHash]
		dm.mutex.RUnlock()
		if !stillTracked {
			return // success: the monitor stopped the stuck job
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("monitor must stop a job stuck in 'starting' past the startup deadline")
}

// ambiguousAcceptFakeMist's PushStart returns an AMBIGUOUS error (as if the
// response was lost after Mist accepted it); the push becomes visible a few polls
// later. A correct state machine must treat the ambiguous error as accepted and
// confirm via PushList — never re-issue PushStart (which would duplicate).
type ambiguousAcceptFakeMist struct {
	streamName      string
	targetURI       string
	started         bool
	emptyAfterStart int
	startCalls      int64
}

func (a *ambiguousAcceptFakeMist) PushStart(streamName, targetURI string) error {
	atomic.AddInt64(&a.startCalls, 1)
	a.streamName = streamName
	a.targetURI = targetURI
	a.started = true
	return fmt.Errorf("read response failed: %w", mist.ErrMistAmbiguous)
}
func (a *ambiguousAcceptFakeMist) PushStop(int) error { return nil }
func (a *ambiguousAcceptFakeMist) PushList() ([]mist.PushInfo, error) {
	if !a.started {
		return nil, nil
	}
	if a.emptyAfterStart > 0 {
		a.emptyAfterStart--
		return []mist.PushInfo{}, nil
	}
	return []mist.PushInfo{{ID: 71, StreamName: a.streamName, TargetURI: a.targetURI}}, nil
}

func TestStartRecordingTreatsAmbiguousPushStartAsAccepted(t *testing.T) {
	clearConn()
	useFastInitialPushRetry(t)
	const (
		dvrHash      = "hash-ambig-accept"
		streamID     = "stream-ambig-accept"
		internalName = "internal-ambig-accept"
	)
	storagePath := t.TempDir()
	fake := &ambiguousAcceptFakeMist{emptyAfterStart: 2}
	dm := &DVRManager{
		logger: logging.NewLogger(), jobs: make(map[string]*DVRJob), storagePath: storagePath,
		mistClient: fake, diskCheck: func(string, uint64) error { return nil },
	}
	if err := dm.StartRecording(dvrHash, streamID, internalName, "live+"+internalName, "", &ipcpb.DVRConfig{}, nil); err != nil {
		t.Fatalf("ambiguous-but-accepted start must confirm, got %v", err)
	}
	dm.mutex.RLock()
	job := dm.jobs[dvrHash]
	dm.mutex.RUnlock()
	if job == nil || job.PushID != 71 {
		t.Fatalf("must confirm the accepted push (id 71), got %+v", job)
	}
	// The non-idempotent PushStart must have been issued EXACTLY once despite the
	// ambiguous error and the delayed visibility.
	if n := atomic.LoadInt64(&fake.startCalls); n != 1 {
		t.Fatalf("PushStart must be issued exactly once on an ambiguous error, got %d", n)
	}
}

// When the startup deadline is reached but Mist is UNAVAILABLE, absence was never
// established — a push accepted during the ambiguous start may still be writing.
// The monitor must RETAIN the job (quarantined), never StopRecording it with an
// unknown PushID.
func TestMonitorRetainsStartingJobWhenMistUnavailableAtTimeout(t *testing.T) {
	clearConn()
	const dvrHash = "hash-quarantine"
	storagePath := t.TempDir()
	outputDir := filepath.Join(storagePath, "dvr", "s", dvrHash)
	if err := os.MkdirAll(filepath.Join(outputDir, "segments"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	dm := &DVRManager{
		logger: logging.NewLogger(), jobs: make(map[string]*DVRJob), storagePath: storagePath,
		mistClient: &fakeMistClient{pushListErr: fmt.Errorf("mist unavailable")},
		diskCheck:  func(string, uint64) error { return nil },
		// Deadline fires quickly; Mist stays unavailable.
		startupTimeout:      30 * time.Millisecond,
		pushMonitorInterval: 10 * time.Millisecond,
	}
	job := &DVRJob{
		DVRHash: dvrHash, InternalName: "x", StreamName: "live+x", OutputDir: outputDir,
		Status: "starting", PushID: 0, MaxRetries: MaxDVRRetries,
		Logger: logging.NewLogger(), SendFunc: func(*ipcpb.ControlMessage) {}, SyncedSegments: make(map[string]bool),
	}
	dm.mutex.Lock()
	dm.jobs[dvrHash] = job
	dm.mutex.Unlock()
	go dm.monitorJob(job)

	// The deadline elapses several times while Mist is down; the job must remain.
	time.Sleep(300 * time.Millisecond)
	dm.mutex.RLock()
	_, tracked := dm.jobs[dvrHash]
	dm.mutex.RUnlock()
	if !tracked {
		t.Fatal("job must be retained (quarantined) when Mist is unavailable at the startup deadline")
	}
}
