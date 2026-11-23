package telemetryspool

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type retryState struct {
	After    time.Time `json:"after"`
	Attempts int       `json:"attempts"`
}

type persistentState struct {
	Dropped     uint64                `json:"dropped"`
	Quarantined uint64                `json:"quarantined"`
	Sequence    uint64                `json:"sequence"`
	LastUpload  time.Time             `json:"last_upload"`
	Retries     map[string]retryState `json:"retries,omitempty"`
}

func (q *Queue) persistLocked() error {
	q.state.Dropped = q.dropped.Load()
	snapshot := q.state
	snapshot.Retries = nil
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	return q.writeMetadata(".state.json", ".state-", raw)
}

func (q *Queue) writeMetadata(target, prefix string, raw []byte) error {
	f, err := os.CreateTemp(q.cfg.Directory, prefix)
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, filepath.Join(q.cfg.Directory, target)); err != nil {
		return err
	}
	return syncDirectory(q.cfg.Directory)
}

// Retry schedules are bounded per record, so one failed delivery never rewrites
// the retry state of every other pending record.
func (q *Queue) saveRetry(name string, state retryState) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return q.writeMetadata(name+".retry", ".retry-", raw)
}
func (q *Queue) loadRetry(name string) error {
	raw, err := os.ReadFile(filepath.Join(q.cfg.Directory, name+".retry"))
	if errors.Is(err, os.ErrNotExist) {
		if legacy, ok := q.state.Retries[name]; ok {
			return q.saveRetry(name, legacy)
		}
		return nil
	}
	if err != nil {
		return err
	}
	var state retryState
	if err = json.Unmarshal(raw, &state); err != nil {
		return fmt.Errorf("invalid retry state for %s: %w", name, err)
	}
	q.state.Retries[name] = state
	return nil
}
func (q *Queue) removeRetry(name string) error {
	err := os.Remove(filepath.Join(q.cfg.Directory, name+".retry"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (q *Queue) loadState() error {
	raw, err := os.ReadFile(filepath.Join(q.cfg.Directory, ".state.json"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil {
		if err = json.Unmarshal(raw, &q.state); err != nil {
			return fmt.Errorf("invalid spool state: %w", err)
		}
	}
	if q.state.Retries == nil {
		q.state.Retries = map[string]retryState{}
	}
	q.dropped.Store(q.state.Dropped)
	return nil
}

func (q *Queue) NextSequence() (uint64, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.state.Sequence++
	return q.state.Sequence, q.persistLocked()
}

func (q *Queue) DeliveryState() (time.Time, time.Time, uint64, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	var oldest time.Time
	entries, readErr := os.ReadDir(q.cfg.Directory)
	if readErr != nil {
		return q.state.LastUpload, time.Time{}, q.state.Quarantined, readErr
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".event" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return q.state.LastUpload, oldest, q.state.Quarantined, err
		}
		if oldest.IsZero() || info.ModTime().Before(oldest) {
			oldest = info.ModTime()
		}
	}
	return q.state.LastUpload, oldest, q.state.Quarantined, nil
}

func (q *Queue) deferRecord(record Record) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	r := q.state.Retries[record.Name]
	r.Attempts++
	delay := time.Second * time.Duration(1<<min(r.Attempts, 5))
	// Stable per-record jitter spreads retries without changing delivery identity.
	if len(record.Name) > 0 {
		delay += time.Duration(record.Name[0]) * time.Millisecond
	}
	r.After = time.Now().Add(delay)
	q.state.Retries[record.Name] = r
	return q.saveRetry(record.Name, r)
}

type Reservation struct {
	q     *Queue
	bytes int64
	done  bool
}

func (r *Reservation) Capacity() int64 { return r.bytes }

func (q *Queue) CanReserve(bytes int64) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return bytes > 0 && bytes <= q.cfg.MaxRecordBytes && q.records+q.reservedRecords < q.cfg.MaxRecords && q.bytes+q.reservedBytes+bytes <= q.cfg.MaxBytes
}

func (q *Queue) NoteDropped() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.dropped.Add(1)
	return q.persistLocked()
}

func (q *Queue) Reserve(bytes int64) (*Reservation, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if bytes <= 0 || bytes > q.cfg.MaxRecordBytes {
		return nil, errors.New("invalid spool reservation")
	}
	if q.records+q.reservedRecords >= q.cfg.MaxRecords || q.bytes+q.reservedBytes+bytes > q.cfg.MaxBytes {
		return nil, ErrFull
	}
	q.reservedRecords++
	q.reservedBytes += bytes
	return &Reservation{q: q, bytes: bytes}, nil
}

func (r *Reservation) Cancel() { r.q.mu.Lock(); defer r.q.mu.Unlock(); r.release() }
func (r *Reservation) release() {
	if !r.done {
		r.done = true
		r.q.reservedRecords--
		r.q.reservedBytes -= r.bytes
	}
}
func (r *Reservation) Commit(id string, payload []byte) error {
	r.q.mu.Lock()
	defer r.q.mu.Unlock()
	if r.done {
		return errors.New("spool reservation already released")
	}
	r.release()
	if int64(len(payload)) > r.bytes {
		return errors.New("record exceeds reserved capacity")
	}
	err := r.q.enqueueLocked(id, payload)
	if err != nil {
		r.done = false
		r.q.reservedRecords++
		r.q.reservedBytes += r.bytes
	}
	return err
}

type DeliveryOutcome struct {
	RetryAfter     time.Duration
	Delivered      bool
	QuarantineCode string
	Err            error
}

func (q *Queue) quarantine(record Record, code string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	dir := filepath.Join(q.cfg.Directory, "quarantine")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var bytes int64
	for _, e := range entries {
		if info, statErr := e.Info(); statErr == nil {
			bytes += info.Size()
		}
	}
	if bytes+int64(len(record.Payload)) > q.cfg.MaxBytes {
		return errors.New("quarantine capacity reached")
	}
	meta := filepath.Join(dir, record.Name+".reason")
	if writeErr := os.WriteFile(meta, []byte(code), 0600); writeErr != nil {
		return writeErr
	}
	f, err := os.OpenFile(meta, os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	err = errors.Join(f.Sync(), f.Close())
	if err != nil {
		return err
	}
	if removeErr := q.removeRetry(record.Name); removeErr != nil {
		return removeErr
	}
	if err := os.Rename(filepath.Join(q.cfg.Directory, record.Name), filepath.Join(dir, record.Name)); err != nil {
		return err
	}
	if err := syncDirectory(dir); err != nil {
		return err
	}
	q.records--
	q.bytes -= int64(len(record.Payload))
	delete(q.state.Retries, record.Name)
	q.state.Quarantined++
	return q.persistLocked()
}
