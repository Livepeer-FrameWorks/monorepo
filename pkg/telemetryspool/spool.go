// Package telemetryspool persists bounded producer records until delivery acknowledgement.
package telemetryspool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	mu      sync.Mutex
	cfg     Config
	bytes   int64
	records int
	wake    chan struct{}
	dropped atomic.Uint64
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
	entries, err := os.ReadDir(cfg.Directory)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".event" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return nil, err
		}
		if time.Since(info.ModTime()) > cfg.MaxAge {
			if err := os.Remove(filepath.Join(cfg.Directory, e.Name())); err != nil {
				return nil, err
			}
			q.dropped.Add(1)
			continue
		}
		if info.Size() > cfg.MaxRecordBytes {
			return nil, fmt.Errorf("spool record exceeds configured size bound")
		}
		q.records++
		q.bytes += info.Size()
	}
	if err := syncDirectory(cfg.Directory); err != nil {
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
	path := filepath.Join(q.cfg.Directory, filename(id))
	if old, err := os.ReadFile(path); err == nil {
		if string(old) != string(payload) {
			return errors.New("producer ID reused with another payload")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if int64(len(payload)) > q.cfg.MaxRecordBytes || q.records >= q.cfg.MaxRecords || q.bytes+int64(len(payload)) > q.cfg.MaxBytes {
		q.dropped.Add(1)
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
	}
	var ordered []entry
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".event" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return nil, err
		}
		if time.Since(info.ModTime()) > q.cfg.MaxAge {
			if err := os.Remove(filepath.Join(q.cfg.Directory, e.Name())); err != nil {
				return nil, err
			}
			q.records--
			q.bytes -= info.Size()
			q.dropped.Add(1)
			continue
		}
		ordered = append(ordered, entry{e.Name(), info.ModTime()})
	}
	sort.Slice(ordered, func(i, j int) bool {
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
	if err = os.Remove(path); err != nil {
		return err
	}
	q.records--
	q.bytes -= info.Size()
	return syncDirectory(q.cfg.Directory)
}
func (q *Queue) Stats() (int, int64, uint64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.records, q.bytes, q.dropped.Load()
}

// Run retries committed records and never acknowledges a failed delivery.
func (q *Queue) Run(ctx context.Context, send func(context.Context, []byte) error, onError func(error)) {
	backoff := time.Second
	for ctx.Err() == nil {
		rows, err := q.Pending(100)
		if err == nil {
			var deliveryErr error
			for _, row := range rows {
				if ctx.Err() != nil {
					return
				}
				err = send(ctx, row.Payload)
				if err == nil {
					err = q.Ack(row)
				}
				if err != nil {
					deliveryErr = err
				}
			}
			err = deliveryErr
		}
		if err != nil && onError != nil {
			onError(fmt.Errorf("telemetry delivery: %w", err))
		}
		delay := time.Second
		if err != nil {
			delay = backoff
			if backoff < 30*time.Second {
				backoff *= 2
				if backoff > 30*time.Second {
					backoff = 30 * time.Second
				}
			}
		} else {
			backoff = time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		case <-q.wake:
			timer.Stop()
		}
	}
}
