package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// Changelog is an ordered, replayable change feed over a Redis Stream — a
// log, not a fire-and-forget bus. Entries get
// server-assigned, monotonically increasing IDs, persist until trimmed, and
// readers resume from their last-applied ID, so a disconnected or restarting
// consumer replays what it missed instead of losing it. Entry IDs double as
// logical versions: comparing two IDs orders the writes they carry without
// reference to any wall clock.
//
// Intended consumption pattern (see state.StreamStateManager.EnableRedisSync):
//
//	tail, _ := log.Tail(ctx)        // 1. capture the current position
//	rehydrateFromKeys()             // 2. load the write-through key snapshot
//	go log.Read(ctx, tail, handler) // 3. replay everything after the capture
//
// The capture-then-load order makes the snapshot+replay a consistent cut: a
// change before the capture is fully reflected in the keys; a change after it
// is replayed.
type Changelog[T any] struct {
	client goredis.UniversalClient
	key    string
	maxLen int64
}

// NewChangelog creates a changelog on the given stream key. maxLen bounds the
// stream with approximate trimming (XADD MAXLEN ~); pick it so the retained
// window comfortably covers a consumer's worst-case downtime.
func NewChangelog[T any](client goredis.UniversalClient, key string, maxLen int64) *Changelog[T] {
	return &Changelog[T]{client: client, key: key, maxLen: maxLen}
}

// Append adds an entry and returns its server-assigned ID.
func (c *Changelog[T]) Append(ctx context.Context, msg T) (string, error) {
	payload, err := json.Marshal(msg)
	if err != nil {
		return "", fmt.Errorf("marshal changelog payload: %w", err)
	}
	id, err := c.client.XAdd(ctx, &goredis.XAddArgs{
		Stream: c.key,
		MaxLen: c.maxLen,
		Approx: true,
		Values: map[string]any{"data": payload},
	}).Result()
	if err != nil {
		return "", fmt.Errorf("append to changelog: %w", err)
	}
	return id, nil
}

// Tail returns the ID of the newest entry, or "0-0" when the stream is empty
// or absent. Reading from the returned ID yields only entries appended after
// this call.
func (c *Changelog[T]) Tail(ctx context.Context) (string, error) {
	entries, err := c.client.XRevRangeN(ctx, c.key, "+", "-", 1).Result()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return "0-0", nil
		}
		return "", fmt.Errorf("read changelog tail: %w", err)
	}
	if len(entries) == 0 {
		return "0-0", nil
	}
	return entries[0].ID, nil
}

// ErrChangelogGap reports that a reader's resume cursor has fallen behind
// the stream's retention window: entries between the cursor and the oldest
// retained entry may have been trimmed away unread. The consumer must
// re-run its consistent cut (capture the tail, reload the write-through
// keys, resume from the new tail) instead of continuing blind.
var ErrChangelogGap = errors.New("redis changelog: reader fell behind retention window")

// Read consumes entries after fromID in order, invoking handler with each
// entry's ID and decoded payload, until ctx is done. Undecodable entries are
// skipped. Returns nil on context cancellation.
//
// After any read failure, the resume position is checked against the
// stream's oldest retained entry; if trimming has passed the cursor while
// the reader was away, Read returns ErrChangelogGap rather than silently
// skipping the trimmed range. (A connected reader at the tail can't be
// outrun: trimming only removes the oldest entries.)
func (c *Changelog[T]) Read(ctx context.Context, fromID string, handler func(id string, msg T)) error {
	lastID := fromID
	if lastID == "" {
		lastID = "0-0"
	}
	checkGap := false
	for {
		if checkGap {
			gap, gapErr := c.gapBehind(ctx, lastID)
			if gapErr == nil {
				if gap {
					return ErrChangelogGap
				}
				checkGap = false
			}
			// On gapErr keep the flag: the XRead below will fail too and
			// route back through the backoff.
		}
		res, err := c.client.XRead(ctx, &goredis.XReadArgs{
			Streams: []string{c.key, lastID},
			Block:   5 * time.Second,
			Count:   128,
		}).Result()
		if err != nil {
			if ctx.Err() != nil {
				return nil //nolint:nilerr // context cancellation is the clean shutdown path, not a read failure
			}
			if errors.Is(err, goredis.Nil) {
				continue // block timeout, no new entries
			}
			// Transient (failover, hiccup): back off and resume from lastID —
			// that resumability is the whole point of the log. The error
			// window is exactly when trimming can outrun the cursor, so
			// verify retention before trusting the resume.
			checkGap = true
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Second):
			}
			continue
		}
		for _, stream := range res {
			for _, entry := range stream.Messages {
				lastID = entry.ID
				raw, ok := entry.Values["data"].(string)
				if !ok {
					continue
				}
				var msg T
				if err := json.Unmarshal([]byte(raw), &msg); err != nil {
					continue
				}
				handler(entry.ID, msg)
			}
		}
	}
}

// gapBehind reports whether the stream's oldest retained entry is newer
// than lastID — meaning entries between them may have been trimmed unread.
// "0-0" cursors are exempt: they deliberately mean "start of the retained
// log", not a position that could have been trimmed past.
func (c *Changelog[T]) gapBehind(ctx context.Context, lastID string) (bool, error) {
	if lastID == "" || lastID == "0-0" {
		return false, nil
	}
	entries, err := c.client.XRangeN(ctx, c.key, "-", "+", 1).Result()
	if err != nil {
		return false, err
	}
	if len(entries) == 0 {
		return false, nil
	}
	return CompareStreamIDs(entries[0].ID, lastID) > 0, nil
}

// Watermarks tracks, per key, the highest changelog entry ID a consumer has
// published or applied. Because entry IDs are server-assigned and monotonic,
// the watermark is a logical version: an entry at or below it is by
// definition older than what local state already reflects, and is skipped.
// This is what makes the apply path idempotent under replay and immune to
// out-of-order application — with no wall clocks involved.
type Watermarks struct {
	mu  sync.Mutex
	ids map[string]string
}

// NewWatermarks creates an empty watermark set.
func NewWatermarks() *Watermarks {
	return &Watermarks{ids: make(map[string]string)}
}

// Record raises the key's watermark to id if newer. Used by writers for
// their own appends, so a peer entry logged before the local write can never
// be applied over it afterwards.
func (w *Watermarks) Record(key, id string) {
	if id == "" {
		return
	}
	w.mu.Lock()
	if CompareStreamIDs(id, w.ids[key]) > 0 {
		w.ids[key] = id
	}
	w.mu.Unlock()
}

// ShouldApply reports whether an entry is newer than the key's watermark,
// raising the watermark when it is.
func (w *Watermarks) ShouldApply(key, id string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if CompareStreamIDs(id, w.ids[key]) <= 0 {
		return false
	}
	w.ids[key] = id
	return true
}

// CompareStreamIDs orders two Redis stream IDs ("<ms>-<seq>") numerically.
// Returns -1, 0, or 1. Empty or malformed IDs order lowest, so "no version
// recorded" always loses to a real entry.
func CompareStreamIDs(a, b string) int {
	am, as, aok := splitStreamID(a)
	bm, bs, bok := splitStreamID(b)
	if !aok || !bok {
		switch {
		case aok:
			return 1
		case bok:
			return -1
		default:
			return 0
		}
	}
	switch {
	case am != bm:
		if am < bm {
			return -1
		}
		return 1
	case as != bs:
		if as < bs {
			return -1
		}
		return 1
	default:
		return 0
	}
}

func splitStreamID(id string) (ms, seq uint64, ok bool) {
	if id == "" {
		return 0, 0, false
	}
	head, tail, found := strings.Cut(id, "-")
	if !found {
		return 0, 0, false
	}
	ms, err := strconv.ParseUint(head, 10, 64)
	if err != nil {
		return 0, 0, false
	}
	seq, err = strconv.ParseUint(tail, 10, 64)
	if err != nil {
		return 0, 0, false
	}
	return ms, seq, true
}
