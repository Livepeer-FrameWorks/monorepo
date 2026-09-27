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
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
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

	// lanes are oldest-first indexes of WAL paths, one per TriggerLane. Payloads
	// stay on disk and are decoded in bounded batches; a large backlog must not
	// become a second, in-memory copy of the WAL. Acked paths are removed from
	// active and lazily compacted from their lane so draining the head stays O(1).
	lanes     [triggerLaneCount]walLane
	active    map[string]TriggerLane
	pathsByID map[string][]string

	// runtimeByID and idsByRuntime index the pending entries that end an ingest runtime's publisher
	// (PUSH_INPUT_CLOSE, STREAM_END) by Mist runtime name, so a new admission for that runtime can be
	// ordered after them. changed is closed and replaced whenever an entry leaves the WAL.
	runtimeByID  map[string]string
	idsByRuntime map[string]map[string]struct{}
	changed      chan struct{}
}

// TriggerLane separates the entries the forwarder drains independently.
type TriggerLane int

const (
	// LaneLifecycle holds session, stream, push and recording end triggers.
	LaneLifecycle TriggerLane = iota
	// LaneSample holds the high-rate transcode billing samples, which must never delay a
	// lifecycle trigger.
	LaneSample
	triggerLaneCount
)

// TriggerLaneOf classifies a trigger by type, so a parse-failure envelope drains in the lane of
// the trigger it wraps.
func TriggerLaneOf(trigger *ipcpb.MistTrigger) TriggerLane {
	switch mist.TriggerType(trigger.GetTriggerType()) {
	case mist.TriggerProcessAVSegmentComplete, mist.TriggerLivepeerSegmentComplete:
		return LaneSample
	default:
		return LaneLifecycle
	}
}

type walLane struct {
	pending []string
	head    int
	// reorders counts insertions that landed before the newest path of the lane. A reader walking
	// the lane with a path cursor restarts from the head when it changes, so no entry is skipped.
	reorders uint64
}

// PendingEntry is one undelivered WAL entry with the path that orders it.
type PendingEntry struct {
	Path    string
	Trigger *ipcpb.MistTrigger
}

// IngestRuntimeOrderKey returns the Mist runtime whose publisher the trigger reports ended, or ""
// when the trigger does not end an ingest publisher. Processing inputs are sidecar-local jobs, not
// admitted publishers.
func IngestRuntimeOrderKey(trigger *ipcpb.MistTrigger) string {
	var runtime string
	switch {
	case trigger.GetPushInputClose() != nil:
		runtime = trigger.GetPushInputClose().GetStreamName()
	case trigger.GetStreamEnd() != nil:
		runtime = trigger.GetStreamEnd().GetStreamName()
	}
	runtime = strings.TrimSpace(runtime)
	if strings.HasPrefix(runtime, "processing+") {
		return ""
	}
	return runtime
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
		dir:          dir,
		active:       make(map[string]TriggerLane, len(files)),
		pathsByID:    make(map[string][]string, len(files)),
		runtimeByID:  make(map[string]string),
		idsByRuntime: make(map[string]map[string]struct{}),
		changed:      make(chan struct{}),
	}
	for _, path := range files {
		// Entries that survive a restart keep their lane and ordering role; an unreadable entry
		// is still forwarded in the lifecycle lane, it just cannot hold back an admission.
		trigger, readErr := readTriggerFile(path)
		lane := LaneLifecycle
		if readErr == nil {
			lane = TriggerLaneOf(trigger)
		}
		w.active[path] = lane
		w.lanes[lane].pending = append(w.lanes[lane].pending, path)
		id, ok := sourceEventIDFromPath(path)
		if !ok {
			continue
		}
		w.pathsByID[id] = append(w.pathsByID[id], path)
		if readErr == nil {
			w.indexRuntimeLocked(id, IngestRuntimeOrderKey(trigger))
		}
	}
	return w, nil
}

func readTriggerFile(path string) (*ipcpb.MistTrigger, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var t ipcpb.MistTrigger
	if err := proto.Unmarshal(data, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

func (w *TriggerWAL) indexRuntimeLocked(sourceEventID, runtime string) {
	if runtime == "" {
		return
	}
	w.runtimeByID[sourceEventID] = runtime
	ids := w.idsByRuntime[runtime]
	if ids == nil {
		ids = make(map[string]struct{})
		w.idsByRuntime[runtime] = ids
	}
	ids[sourceEventID] = struct{}{}
}

// removedLocked drops an entry that left the WAL from the runtime index and wakes every waiter.
func (w *TriggerWAL) removedLocked(sourceEventID string) {
	if runtime, ok := w.runtimeByID[sourceEventID]; ok {
		delete(w.runtimeByID, sourceEventID)
		if ids := w.idsByRuntime[runtime]; ids != nil {
			delete(ids, sourceEventID)
			if len(ids) == 0 {
				delete(w.idsByRuntime, runtime)
			}
		}
	}
	close(w.changed)
	w.changed = make(chan struct{})
}

// PendingForRuntime reports how many entries ending runtime's publisher are still undelivered, and
// a channel that is closed at the next removal of any entry.
func (w *TriggerWAL) PendingForRuntime(runtime string) (int, <-chan struct{}) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.idsByRuntime[strings.TrimSpace(runtime)]), w.changed
}

// PendingRuntimeBatch returns the undelivered entries that end runtime's publisher, oldest first.
func (w *TriggerWAL) PendingRuntimeBatch(runtime string) ([]*ipcpb.MistTrigger, error) {
	w.mu.Lock()
	ids := w.idsByRuntime[strings.TrimSpace(runtime)]
	files := make([]string, 0, len(ids))
	for id := range ids {
		for _, path := range w.pathsByID[id] {
			if _, ok := w.active[path]; ok {
				files = append(files, path)
			}
		}
	}
	w.mu.Unlock()
	sort.Strings(files)
	out := make([]*ipcpb.MistTrigger, 0, len(files))
	for _, path := range files {
		t, err := readTriggerFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("trigger wal read %s: %w", filepath.Base(path), err)
		}
		out = append(out, t)
	}
	return out, nil
}

// IsPending reports whether sourceEventID is still awaiting delivery.
func (w *TriggerWAL) IsPending(sourceEventID string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.pathsByID[sourceEventID]) > 0
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
	w.addPathLocked(id, path, TriggerLaneOf(trigger))
	w.indexRuntimeLocked(id, IngestRuntimeOrderKey(trigger))
	if err := syncDir(w.dir); err != nil {
		return false, fmt.Errorf("trigger wal sync dir: %w", err)
	}
	return true, nil
}

// Ack removes the durable entry for source_event_id. Idempotent — calling
// Ack on an already-acked id is a no-op. The removal is not fsynced: a crash
// that brings the file back only resends an entry Foghorn already committed,
// under the same source_event_id every downstream consumer deduplicates on,
// and a directory fsync per ack would serialize the forwarder on the disk.
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
	w.advanceHeadsLocked()
	if len(files) > 0 {
		w.removedLocked(sourceEventID)
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
	w.advanceHeadsLocked()
	if len(files) > 0 {
		w.removedLocked(sourceEventID)
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

// PendingBatch returns at most limit persisted triggers in oldest-first order across lanes.
// A non-positive limit means all entries. Only the selected protobuf payloads
// are read; the in-memory index contains paths, not payloads.
func (w *TriggerWAL) PendingBatch(limit int) ([]*ipcpb.MistTrigger, error) {
	w.mu.Lock()
	files := make([]string, 0)
	for lane := range w.lanes {
		files = append(files, w.activePathsLocked(TriggerLane(lane), "", limit)...)
	}
	w.mu.Unlock()
	sort.Strings(files)
	if limit > 0 && len(files) > limit {
		files = files[:limit]
	}

	out := make([]*ipcpb.MistTrigger, 0, len(files))
	for _, path := range files {
		t, err := readTriggerFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("trigger wal read %s: %w", filepath.Base(path), err)
		}
		out = append(out, t)
	}
	return out, nil
}

// PendingAfter returns at most limit undelivered entries of lane whose path sorts after the given
// path, oldest first. An empty after starts at the oldest entry.
func (w *TriggerWAL) PendingAfter(lane TriggerLane, after string, limit int) ([]PendingEntry, error) {
	w.mu.Lock()
	files := w.activePathsLocked(lane, after, limit)
	w.mu.Unlock()

	out := make([]PendingEntry, 0, len(files))
	for _, path := range files {
		t, err := readTriggerFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("trigger wal read %s: %w", filepath.Base(path), err)
		}
		out = append(out, PendingEntry{Path: path, Trigger: t})
	}
	return out, nil
}

func (w *TriggerWAL) activePathsLocked(lane TriggerLane, after string, limit int) []string {
	l := &w.lanes[lane]
	start := l.head
	if after != "" {
		start = l.head + sort.SearchStrings(l.pending[l.head:], after)
	}
	files := make([]string, 0)
	for i := start; i < len(l.pending); i++ {
		path := l.pending[i]
		if path <= after {
			continue
		}
		if _, ok := w.active[path]; !ok {
			continue
		}
		files = append(files, path)
		if limit > 0 && len(files) >= limit {
			break
		}
	}
	return files
}

// Reorders reports how many entries have been inserted behind the newest path of lane.
func (w *TriggerWAL) Reorders(lane TriggerLane) uint64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lanes[lane].reorders
}

// PendingDepth returns the count without unmarshaling — for metrics.
func (w *TriggerWAL) PendingDepth() (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.active), nil
}

func (w *TriggerWAL) addPathLocked(sourceEventID, path string, lane TriggerLane) {
	w.active[path] = lane
	w.pathsByID[sourceEventID] = append(w.pathsByID[sourceEventID], path)
	l := &w.lanes[lane]
	if len(l.pending) == 0 || path > l.pending[len(l.pending)-1] {
		l.pending = append(l.pending, path)
		return
	}
	l.reorders++
	index := sort.SearchStrings(l.pending, path)
	l.pending = append(l.pending, "")
	copy(l.pending[index+1:], l.pending[index:])
	l.pending[index] = path
	if index < l.head {
		l.head = index
	}
}

func (w *TriggerWAL) advanceHeadsLocked() {
	for i := range w.lanes {
		l := &w.lanes[i]
		for l.head < len(l.pending) {
			if _, ok := w.active[l.pending[l.head]]; ok {
				break
			}
			l.head++
		}
		if l.head > 4096 && l.head*2 > len(l.pending) {
			l.pending = append([]string(nil), l.pending[l.head:]...)
			l.head = 0
		}
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
