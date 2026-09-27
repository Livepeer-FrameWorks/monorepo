package storage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/proto"
)

// A staged entry is a durable trigger that is still being accumulated: each rewrite replaces it
// atomically, and the forwarder cannot see it until it is sealed into the WAL. Staged entries live
// in their own directory, so a restart finds every one that was never sealed and seals it: the
// accumulator that owned it is gone and nothing will add to it again.

const stagingDirName = "staging"

func (w *TriggerWAL) stagingDir() string { return filepath.Join(w.dir, stagingDirName) }

func (w *TriggerWAL) stagedPath(sourceEventID string) string {
	return filepath.Join(w.stagingDir(), sourceEventID+".pb")
}

// Stage durably writes trigger as the current content of its staged entry, replacing any earlier
// content under the same source_event_id. It returns only after the content is on disk.
func (w *TriggerWAL) Stage(trigger *ipcpb.MistTrigger) error {
	if trigger == nil {
		return errors.New("trigger wal: nil trigger")
	}
	id := trigger.GetRequestId()
	if id == "" || strings.ContainsAny(id, `/\`) {
		return fmt.Errorf("trigger wal: invalid staged source_event_id %q", id)
	}
	payload, err := proto.Marshal(trigger)
	if err != nil {
		return fmt.Errorf("trigger wal marshal: %w", err)
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.pathsByID[id]) > 0 {
		return fmt.Errorf("trigger wal: %s is already sealed", id)
	}
	if err := os.MkdirAll(w.stagingDir(), 0o700); err != nil {
		return fmt.Errorf("trigger wal staging mkdir: %w", err)
	}
	path := w.stagedPath(id)
	if err := writeFileDurably(path, payload); err != nil {
		return err
	}
	if err := syncDir(w.stagingDir()); err != nil {
		return fmt.Errorf("trigger wal staging sync dir: %w", err)
	}
	return nil
}

// Seal moves a staged entry into the WAL, where the forwarder delivers it in the lane of its
// trigger type. The entry's position is the seal time, so it lands at the tail of its lane.
func (w *TriggerWAL) Seal(sourceEventID string, sealedAt time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.sealLocked(sourceEventID, sealedAt)
}

func (w *TriggerWAL) sealLocked(sourceEventID string, sealedAt time.Time) error {
	staged := w.stagedPath(sourceEventID)
	trigger, err := readTriggerFile(staged)
	if err != nil {
		return fmt.Errorf("trigger wal read staged %s: %w", sourceEventID, err)
	}
	path := w.path(sourceEventID, sealedAt.UnixMilli(), triggerClass(trigger))
	if err := os.Rename(staged, path); err != nil {
		return fmt.Errorf("trigger wal seal rename: %w", err)
	}
	w.addPathLocked(sourceEventID, path, TriggerLaneOf(trigger))
	w.indexRuntimeLocked(sourceEventID, IngestRuntimeOrderKey(trigger))
	if err := syncDir(w.dir); err != nil {
		return fmt.Errorf("trigger wal seal sync dir: %w", err)
	}
	return nil
}

func (w *TriggerWAL) sealLeftoverStaged() error {
	staged, err := filepath.Glob(filepath.Join(w.stagingDir(), "*.pb"))
	if err != nil {
		return fmt.Errorf("trigger wal staging glob: %w", err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	now := time.Now()
	for _, path := range staged {
		id := strings.TrimSuffix(filepath.Base(path), ".pb")
		if sealErr := w.sealLocked(id, now); sealErr != nil {
			return sealErr
		}
	}
	return nil
}

func writeFileDurably(path string, payload []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("trigger wal open: %w", err)
	}
	if _, err := f.Write(payload); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("trigger wal write: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("trigger wal fsync: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("trigger wal close: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("trigger wal rename: %w", err)
	}
	return nil
}
