package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
)

// TriggerWAL durably stores MistTrigger payloads for non-blocking final
// triggers (USER_END, STREAM_END, PUSH_END, RECORDING_END,
// RECORDING_SEGMENT, LIVEPEER_SEGMENT_COMPLETE,
// PROCESS_AV_VIRTUAL_SEGMENT_COMPLETE). Entries persist until Foghorn
// returns a positive MistTriggerAck. On disconnect or restart the
// forwarder replays all entries; identical re-deliveries from Mist are
// idempotent because the natural key is the source_event_id which is
// derived deterministically from (node_id, trigger_type, payload_raw).
type TriggerWAL struct {
	dir string
	mu  sync.Mutex
}

// NewTriggerWAL creates (or opens) the WAL directory.
func NewTriggerWAL(dir string) (*TriggerWAL, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("trigger wal mkdir: %w", err)
	}
	return &TriggerWAL{dir: dir}, nil
}

// DefaultTriggerWALDir resolves the on-disk directory used by the WAL.
// Honors FRAMEWORKS_TRIGGER_WAL_DIR, falling back to the edge storage path
// and finally the user cache dir or /tmp.
func DefaultTriggerWALDir() string {
	if dir := strings.TrimSpace(os.Getenv("FRAMEWORKS_TRIGGER_WAL_DIR")); dir != "" {
		return dir
	}
	if storagePath := strings.TrimSpace(os.Getenv("HELMSMAN_STORAGE_LOCAL_PATH")); storagePath != "" {
		return filepath.Join(storagePath, "trigger-wal")
	}
	if cacheDir, err := os.UserCacheDir(); err == nil {
		return filepath.Join(cacheDir, "frameworks", "trigger-wal")
	}
	return filepath.Join(os.TempDir(), "frameworks-trigger-wal")
}

// ComputeSourceEventID derives the stable id for a trigger. Retries from
// Mist for the same logical event collide on this key.
func ComputeSourceEventID(nodeID, triggerType string, payload []byte) string {
	h := sha256.New()
	h.Write([]byte(nodeID))
	h.Write([]byte{0})
	h.Write([]byte(triggerType))
	h.Write([]byte{0})
	h.Write(payload)
	return hex.EncodeToString(h.Sum(nil))
}

func ComputeTypedEventID(sourceEventID string) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("frameworks:mist-trigger:"+sourceEventID)).String()
}

// Append persists a trigger durably. Returns true when this is the first
// time the source_event_id has been written; false when a prior entry
// already exists (idempotent re-delivery). The on-disk write and directory
// entry are fsynced so a crash between Mist's 200 OK and the next forwarder
// tick still surfaces the trigger on restart.
func (w *TriggerWAL) Append(trigger *pb.MistTrigger) (bool, error) {
	if trigger == nil {
		return false, errors.New("trigger wal: nil trigger")
	}
	id := trigger.GetRequestId()
	if id == "" {
		return false, errors.New("trigger wal: trigger missing request_id (source_event_id)")
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	existing, err := filepath.Glob(w.globForID(id))
	if err != nil {
		return false, fmt.Errorf("trigger wal duplicate glob: %w", err)
	}
	if len(existing) > 0 {
		return false, nil
	}

	path := w.path(id, trigger.GetTimestamp())

	payload, err := proto.Marshal(trigger)
	if err != nil {
		return false, fmt.Errorf("trigger wal marshal: %w", err)
	}

	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return false, fmt.Errorf("trigger wal open: %w", err)
	}
	if _, err := f.Write(payload); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return false, fmt.Errorf("trigger wal write: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return false, fmt.Errorf("trigger wal fsync: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return false, fmt.Errorf("trigger wal close: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return false, fmt.Errorf("trigger wal rename: %w", err)
	}
	if err := syncDir(w.dir); err != nil {
		return false, fmt.Errorf("trigger wal sync dir: %w", err)
	}
	return true, nil
}

// Ack removes the durable entry for source_event_id. Idempotent — calling
// Ack on an already-acked id is a no-op.
func (w *TriggerWAL) Ack(sourceEventID string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	files, err := filepath.Glob(w.globForID(sourceEventID))
	if err != nil {
		return fmt.Errorf("trigger wal ack glob: %w", err)
	}
	for _, f := range files {
		if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("trigger wal ack remove: %w", err)
		}
	}
	if len(files) > 0 {
		if err := syncDir(w.dir); err != nil {
			return fmt.Errorf("trigger wal ack sync dir: %w", err)
		}
	}
	return nil
}

// DeadLetter removes a non-retryable entry from the pending WAL without
// deleting it outright. The forwarder no longer retries it, but operators can
// still inspect the .dead file on disk.
func (w *TriggerWAL) DeadLetter(sourceEventID string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	files, err := filepath.Glob(w.globForID(sourceEventID))
	if err != nil {
		return fmt.Errorf("trigger wal dead-letter glob: %w", err)
	}
	for _, f := range files {
		deadPath := f + ".dead"
		if err := os.Rename(f, deadPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("trigger wal dead-letter rename: %w", err)
		}
	}
	if len(files) > 0 {
		if err := syncDir(w.dir); err != nil {
			return fmt.Errorf("trigger wal dead-letter sync dir: %w", err)
		}
	}
	return nil
}

// Pending returns every persisted trigger in oldest-first order (by the
// received_at_ms prefix embedded in the filename). Used by the forwarder
// to drain in order and on restart to replay.
func (w *TriggerWAL) Pending() ([]*pb.MistTrigger, error) {
	w.mu.Lock()
	files, err := filepath.Glob(filepath.Join(w.dir, "*.pb"))
	w.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("trigger wal glob: %w", err)
	}
	sort.Strings(files)

	out := make([]*pb.MistTrigger, 0, len(files))
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("trigger wal read %s: %w", filepath.Base(path), err)
		}
		var t pb.MistTrigger
		if err := proto.Unmarshal(data, &t); err != nil {
			return nil, fmt.Errorf("trigger wal unmarshal %s: %w", filepath.Base(path), err)
		}
		out = append(out, &t)
	}
	return out, nil
}

// PendingDepth returns the count without unmarshaling — for metrics.
func (w *TriggerWAL) PendingDepth() (int, error) {
	files, err := filepath.Glob(filepath.Join(w.dir, "*.pb"))
	if err != nil {
		return 0, err
	}
	return len(files), nil
}

func (w *TriggerWAL) path(sourceEventID string, receivedAt int64) string {
	if receivedAt <= 0 {
		receivedAt = time.Now().UnixMilli()
	} else if receivedAt < 1_000_000_000_000 {
		receivedAt *= 1000
	}
	name := strconv.FormatInt(receivedAt, 10) + "-" + sourceEventID + ".pb"
	return filepath.Join(w.dir, name)
}

func (w *TriggerWAL) globForID(sourceEventID string) string {
	return filepath.Join(w.dir, "*-"+sourceEventID+".pb")
}

func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return f.Sync()
}
