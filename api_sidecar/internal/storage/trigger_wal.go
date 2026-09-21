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

	"frameworks/api_sidecar/internal/appconfig"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
)

// TriggerWAL durably stores MistTrigger payloads for non-blocking final
// triggers (USER_END, STREAM_END, PUSH_END, PUSH_INPUT_CLOSE,
// RECORDING_END, RECORDING_SEGMENT, LIVEPEER_SEGMENT_COMPLETE,
// PROCESS_AV_VIRTUAL_SEGMENT_COMPLETE — the mist.IsDurableTriggerType
// set). Entries persist until Foghorn
// returns a positive MistTriggerAck. On disconnect or restart the
// forwarder replays all entries; identical re-deliveries from Mist are
// idempotent because the natural key is the source_event_id which is
// derived deterministically from (node_id, trigger_type, payload_raw).
type TriggerWAL struct {
	dir string
	mu  sync.Mutex

	// pending is an oldest-first index of WAL paths. Payloads stay on disk and
	// are decoded in bounded batches; a large backlog must not become a second,
	// in-memory copy of the WAL. Acked paths are removed from active and lazily
	// compacted from pending so draining the head stays O(1).
	pending   []string
	head      int
	active    map[string]struct{}
	pathsByID map[string][]string
}

// NewTriggerWAL creates (or opens) the WAL directory.
func NewTriggerWAL(dir string) (*TriggerWAL, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("trigger wal mkdir: %w", err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.pb"))
	if err != nil {
		return nil, fmt.Errorf("trigger wal index glob: %w", err)
	}
	sort.Strings(files)
	w := &TriggerWAL{
		dir:       dir,
		pending:   files,
		active:    make(map[string]struct{}, len(files)),
		pathsByID: make(map[string][]string, len(files)),
	}
	for _, path := range files {
		w.active[path] = struct{}{}
		if id, ok := sourceEventIDFromPath(path); ok {
			w.pathsByID[id] = append(w.pathsByID[id], path)
		}
	}
	return w, nil
}

// DefaultTriggerWALDir resolves the on-disk directory used by the WAL.
// Honors FRAMEWORKS_TRIGGER_WAL_DIR, falling back only to Helmsman's explicit
// durable state directory. Media storage and /tmp are not durability bounds.
func DefaultTriggerWALDir() string {
	if dir := strings.TrimSpace(appconfig.Runtime().TriggerWALDir); dir != "" {
		return dir
	}
	if stateDir := strings.TrimSpace(appconfig.StateDir()); stateDir != "" {
		return filepath.Join(stateDir, "trigger-wal")
	}
	return ""
}

// ComputeSourceEventID derives the stable id for a trigger. Retries from Mist for the
// same logical event collide on this key.
//
// naturalKey is an optional distinguishing identity folded into the hash when non-empty
// — MistServer's X-Trigger-UUID for triggers that carry one. Without it a PUSH_INPUT_CLOSE
// dedups on (node, type, body) alone, and two legitimately-distinct closes with identical
// bodies (same stream + an OS-reused connector PID) would collapse to one key: the second,
// real close would be dropped as a duplicate. The trigger UUID is stable across a single
// trigger's retries (so retry-dedup still works) but distinct per logical trigger (so the
// two real closes stay distinct). Empty preserves the pre-existing (node, type, body) key.
func ComputeSourceEventID(nodeID, triggerType string, payload []byte, naturalKey string) string {
	h := sha256.New()
	h.Write([]byte(nodeID))
	h.Write([]byte{0})
	h.Write([]byte(triggerType))
	h.Write([]byte{0})
	h.Write(payload)
	if naturalKey != "" {
		h.Write([]byte{0})
		h.Write([]byte(naturalKey))
	}
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
func (w *TriggerWAL) Append(trigger *ipcpb.MistTrigger) (bool, error) {
	if trigger == nil {
		return false, errors.New("trigger wal: nil trigger")
	}
	id := trigger.GetRequestId()
	if id == "" {
		return false, errors.New("trigger wal: trigger missing request_id (source_event_id)")
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if len(w.pathsByID[id]) > 0 {
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
	w.addPathLocked(id, path)
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
	files := append([]string(nil), w.pathsByID[sourceEventID]...)
	for _, f := range files {
		if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("trigger wal ack remove: %w", err)
		}
		delete(w.active, f)
	}
	delete(w.pathsByID, sourceEventID)
	w.advanceHeadLocked()
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
	files := append([]string(nil), w.pathsByID[sourceEventID]...)
	for _, f := range files {
		deadPath := f + ".dead"
		if err := os.Rename(f, deadPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("trigger wal dead-letter rename: %w", err)
		}
		delete(w.active, f)
	}
	delete(w.pathsByID, sourceEventID)
	w.advanceHeadLocked()
	if len(files) > 0 {
		if err := syncDir(w.dir); err != nil {
			return fmt.Errorf("trigger wal dead-letter sync dir: %w", err)
		}
	}
	return nil
}

// Pending returns every persisted trigger in oldest-first order. It is kept
// for tests and offline callers; online draining and inspection must use
// PendingBatch so a large WAL is never decoded in full.
func (w *TriggerWAL) Pending() ([]*ipcpb.MistTrigger, error) {
	return w.PendingBatch(0)
}

// PendingBatch returns at most limit persisted triggers in oldest-first order.
// A non-positive limit means all entries. Only the selected protobuf payloads
// are read; the in-memory index contains paths, not payloads.
func (w *TriggerWAL) PendingBatch(limit int) ([]*ipcpb.MistTrigger, error) {
	w.mu.Lock()
	files := make([]string, 0)
	for i := w.head; i < len(w.pending); i++ {
		path := w.pending[i]
		if _, ok := w.active[path]; !ok {
			continue
		}
		files = append(files, path)
		if limit > 0 && len(files) >= limit {
			break
		}
	}
	w.mu.Unlock()

	out := make([]*ipcpb.MistTrigger, 0, len(files))
	for _, path := range files {
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("trigger wal read %s: %w", filepath.Base(path), err)
		}
		var t ipcpb.MistTrigger
		if err := proto.Unmarshal(data, &t); err != nil {
			return nil, fmt.Errorf("trigger wal unmarshal %s: %w", filepath.Base(path), err)
		}
		out = append(out, &t)
	}
	return out, nil
}

// PendingDepth returns the count without unmarshaling — for metrics.
func (w *TriggerWAL) PendingDepth() (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.active), nil
}

func (w *TriggerWAL) addPathLocked(sourceEventID, path string) {
	w.active[path] = struct{}{}
	w.pathsByID[sourceEventID] = append(w.pathsByID[sourceEventID], path)
	if len(w.pending) == 0 || path > w.pending[len(w.pending)-1] {
		w.pending = append(w.pending, path)
		return
	}
	index := sort.SearchStrings(w.pending, path)
	w.pending = append(w.pending, "")
	copy(w.pending[index+1:], w.pending[index:])
	w.pending[index] = path
	if index < w.head {
		w.head = index
	}
}

func (w *TriggerWAL) advanceHeadLocked() {
	for w.head < len(w.pending) {
		if _, ok := w.active[w.pending[w.head]]; ok {
			break
		}
		w.head++
	}
	if w.head > 4096 && w.head*2 > len(w.pending) {
		w.pending = append([]string(nil), w.pending[w.head:]...)
		w.head = 0
	}
}

func sourceEventIDFromPath(path string) (string, bool) {
	name := strings.TrimSuffix(filepath.Base(path), ".pb")
	separator := strings.IndexByte(name, '-')
	if separator <= 0 || separator == len(name)-1 {
		return "", false
	}
	return name[separator+1:], true
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

func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return f.Sync()
}
