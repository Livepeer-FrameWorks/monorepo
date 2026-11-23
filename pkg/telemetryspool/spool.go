// Package telemetryspool persists bounded producer records until delivery acknowledgement.
package telemetryspool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var ErrFull = errors.New("telemetry spool capacity exceeded")

type Config struct {
	Directory      string
	MaxBytes       int64
	MaxRecords     int
	MaxRecordBytes int64
	MaxAge         time.Duration
}
type Record struct {
	Name    string
	Payload []byte
}
type Queue struct {
	mu              sync.Mutex
	cfg             Config
	bytes           int64
	records         int
	wake            chan struct{}
	dropped         atomic.Uint64
	state           persistentState
	reservedRecords int
	reservedBytes   int64
}

func Open(cfg Config) (*Queue, error) {
	if cfg.Directory == "" {
		return nil, errors.New("spool directory is required")
	}
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = 64 << 20
	}
	if cfg.MaxRecords <= 0 {
		cfg.MaxRecords = 10000
	}
	if cfg.MaxRecordBytes <= 0 {
		cfg.MaxRecordBytes = 8 << 20
	}
	if cfg.MaxAge <= 0 {
		cfg.MaxAge = 30 * 24 * time.Hour
	}
	if err := os.MkdirAll(cfg.Directory, 0700); err != nil {
		return nil, err
	}
	q := &Queue{cfg: cfg, wake: make(chan struct{}, 1)}
	if err := q.loadState(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(cfg.Directory)
	if err != nil {
		return nil, err
	}
	active := map[string]bool{}
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".event" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return nil, err
		}
		if time.Since(info.ModTime()) > cfg.MaxAge {
			if removeErr := q.removeRetry(e.Name()); removeErr != nil {
				return nil, removeErr
			}
			if err := os.Remove(filepath.Join(cfg.Directory, e.Name())); err != nil {
				return nil, err
			}
			q.dropped.Add(1)
			continue
		}
		if info.Size() > cfg.MaxRecordBytes {
			return nil, errors.New("spool record exceeds configured size bound")
		}
		q.records++
		q.bytes += info.Size()
		active[e.Name()] = true
		if retryErr := q.loadRetry(e.Name()); retryErr != nil {
			return nil, retryErr
		}
	}
	for name := range q.state.Retries {
		if !active[name] {
			delete(q.state.Retries, name)
		}
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".event.retry") && !active[strings.TrimSuffix(entry.Name(), ".retry")] {
			if removeErr := os.Remove(filepath.Join(cfg.Directory, entry.Name())); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				return nil, removeErr
			}
		}
	}
	if err := q.persistLocked(); err != nil {
		return nil, err
	}
	return q, nil
}
func filename(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:]) + ".event"
}
func (q *Queue) Enqueue(id string, payload []byte) error {
	if id == "" || len(payload) == 0 {
		return errors.New("record ID and payload are required")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.enqueueLocked(id, payload)
}
func (q *Queue) enqueueLocked(id string, payload []byte) error {
	if id == "" || len(payload) == 0 {
		return errors.New("record ID and payload are required")
	}
	path := filepath.Join(q.cfg.Directory, filename(id))
	if old, err := os.ReadFile(path); err == nil {
		if string(old) != string(payload) {
			return errors.New("producer ID reused with another payload")
		}
		return syncDirectory(q.cfg.Directory)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if int64(len(payload)) > q.cfg.MaxRecordBytes || q.records+q.reservedRecords >= q.cfg.MaxRecords || q.bytes+q.reservedBytes+int64(len(payload)) > q.cfg.MaxBytes {
		q.dropped.Add(1)
		if err := q.persistLocked(); err != nil {
			return errors.Join(ErrFull, err)
		}
		return ErrFull
	}
	f, err := os.CreateTemp(q.cfg.Directory, ".pending-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(payload)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	q.records++
	q.bytes += int64(len(payload))
	if err = syncDirectory(q.cfg.Directory); err != nil {
		return err
	}
	select {
	case q.wake <- struct{}{}:
	default:
	}
	return nil
}
func syncDirectory(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func (q *Queue) Pending(limit int) ([]Record, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	entries, err := os.ReadDir(q.cfg.Directory)
	if err != nil {
		return nil, err
	}
	type entry struct {
		name     string
		modified time.Time
		attempts int
	}
	var ordered []entry
	expired := false
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".event" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return nil, err
		}
		if time.Since(info.ModTime()) > q.cfg.MaxAge {
			if removeErr := q.removeRetry(e.Name()); removeErr != nil {
				return nil, removeErr
			}
			if err := os.Remove(filepath.Join(q.cfg.Directory, e.Name())); err != nil {
				return nil, err
			}
			q.records--
			q.bytes -= info.Size()
			q.dropped.Add(1)
			delete(q.state.Retries, e.Name())
			expired = true
			continue
		}
		if retry, ok := q.state.Retries[e.Name()]; ok && time.Now().Before(retry.After) {
			continue
		}
		ordered = append(ordered, entry{e.Name(), info.ModTime(), q.state.Retries[e.Name()].Attempts})
	}
	if expired {
		if err := q.persistLocked(); err != nil {
			return nil, err
		}
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].attempts != ordered[j].attempts {
			return ordered[i].attempts < ordered[j].attempts
		}
		if ordered[i].modified.Equal(ordered[j].modified) {
			return ordered[i].name < ordered[j].name
		}
		return ordered[i].modified.Before(ordered[j].modified)
	})
	if limit <= 0 {
		limit = 100
	}
	if len(ordered) > limit {
		ordered = ordered[:limit]
	}
	out := make([]Record, 0, len(ordered))
	for _, e := range ordered {
		p, err := os.ReadFile(filepath.Join(q.cfg.Directory, e.name))
		if err != nil {
			return nil, err
		}
		out = append(out, Record{Name: e.name, Payload: p})
	}
	return out, nil
}
func (q *Queue) Ack(record Record) error {
	if filepath.Base(record.Name) != record.Name || filepath.Ext(record.Name) != ".event" {
		return errors.New("invalid spool record name")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	path := filepath.Join(q.cfg.Directory, record.Name)
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if removeErr := q.removeRetry(record.Name); removeErr != nil {
		return removeErr
	}
	if err = os.Remove(path); err != nil {
		return err
	}
	q.records--
	q.bytes -= info.Size()
	delete(q.state.Retries, record.Name)
	q.state.LastUpload = time.Now().UTC()
	return q.persistLocked()
}
func (q *Queue) Stats() (int, int64, uint64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.records, q.bytes, q.dropped.Load()
}

// Run retries committed records and never acknowledges a failed delivery.
func (q *Queue) Run(ctx context.Context, send func(context.Context, []byte) error, onError func(error)) {
	q.RunWithPolicy(ctx, func(ctx context.Context, raw []byte) DeliveryOutcome {
		err := send(ctx, raw)
		return DeliveryOutcome{Delivered: err == nil, Err: err}
	}, onError)
}
func (q *Queue) RunWithPolicy(ctx context.Context, send func(context.Context, []byte) DeliveryOutcome, onError func(error)) {
	for ctx.Err() == nil {
		rows, err := q.Pending(100)
		if err == nil {
			for _, row := range rows {
				if ctx.Err() != nil {
					return
				}
				q.finish(row, send(ctx, row.Payload), onError)
			}
		} else if onError != nil {
			onError(err)
		}
		if len(rows) == 100 {
			continue
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-q.wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}
