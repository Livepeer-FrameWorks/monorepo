package grpc

import (
	"context"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const (
	// mediaAuthorityCompileMemoMaxAge bounds how long an entry is used at all.
	// Purser state that changes without a refresh trigger reaches compiles once
	// the entry holding the older answer ages out.
	mediaAuthorityCompileMemoMaxAge = 2 * time.Minute
	// mediaAuthorityCompileMemoEntries bounds the memo; past it, aged entries
	// are dropped, and if that is not enough, all of them.
	mediaAuthorityCompileMemoEntries = 4096
	// mediaAuthorityCompileMemoClockMargin covers the skew between the database
	// nodes whose clocks stamp a row's trigger and a claim (YugabyteDB refuses
	// to run nodes further apart than its 500 ms default maximum): an entry
	// serves a row only when it was read at least this long after the row's
	// trigger.
	mediaAuthorityCompileMemoClockMargin = 500 * time.Millisecond
)

// mediaAuthorityCompileMemo shares the Purser answers a tenant's objects all
// derive from between their compiles, across claims, so a fanout over a
// tenant's objects asks Purser once rather than once per object.
//
// Each entry is stamped with read_at: the claimed_at of the claim whose compile
// loaded it, by the database clock, which is no later than the load began. A
// compile uses an entry only when its row's triggered_at is earlier than that,
// so the entry was read after the change that enqueued the row: a compile never
// uses Purser state older than its trigger. An unchanged compile publishes
// nothing, so a compile that used older state would never be corrected. This
// relies on Purser committing its change before the event that enqueues the
// refresh, which holds for Purser's own state; state Commodore writes in the
// enqueueing transaction itself must not be memoized here.
type mediaAuthorityCompileMemo struct {
	mu      sync.Mutex
	entries map[string]mediaAuthorityCompileMemoEntry
	group   singleflight.Group
}

type mediaAuthorityCompileMemoEntry struct {
	value  any
	readAt time.Time
	// storedAt is the local clock, used only to age entries out.
	storedAt time.Time
}

func newMediaAuthorityCompileMemo() *mediaAuthorityCompileMemo {
	return &mediaAuthorityCompileMemo{entries: map[string]mediaAuthorityCompileMemoEntry{}}
}

// compileMemoForClaim returns the memo the obligation worker's compiles share.
func (s *CommodoreServer) compileMemoForClaim() *mediaAuthorityCompileMemo {
	s.mediaAuthorityCompileInputsOnce.Do(func() {
		s.mediaAuthorityCompileInputs = newMediaAuthorityCompileMemo()
	})
	return s.mediaAuthorityCompileInputs
}

type mediaAuthorityCompileMemoContextKey struct{}

// mediaAuthorityCompileScope is one claimed row's view of the memo: what
// triggered it and when it was claimed, both by the database clock.
type mediaAuthorityCompileScope struct {
	memo        *mediaAuthorityCompileMemo
	triggeredAt time.Time
	claimedAt   time.Time
}

func withMediaAuthorityCompileMemo(ctx context.Context, memo *mediaAuthorityCompileMemo, triggeredAt, claimedAt time.Time) context.Context {
	return context.WithValue(ctx, mediaAuthorityCompileMemoContextKey{}, &mediaAuthorityCompileScope{
		memo: memo, triggeredAt: triggeredAt, claimedAt: claimedAt,
	})
}

// lookup returns the entry for key when it was read late enough to serve a row
// triggered at triggeredAt.
func (m *mediaAuthorityCompileMemo) lookup(key string, triggeredAt time.Time) (any, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.entries[key]
	if !ok || time.Since(entry.storedAt) > mediaAuthorityCompileMemoMaxAge {
		return nil, false
	}
	return entry.value, triggeredAt.Add(mediaAuthorityCompileMemoClockMargin).Before(entry.readAt)
}

// store keeps value for key unless an entry read later is already there.
func (m *mediaAuthorityCompileMemo) store(key string, value any, readAt time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.entries[key]; ok && existing.readAt.After(readAt) {
		return
	}
	if len(m.entries) >= mediaAuthorityCompileMemoEntries {
		for stale, entry := range m.entries {
			if time.Since(entry.storedAt) > mediaAuthorityCompileMemoMaxAge {
				delete(m.entries, stale)
			}
		}
		if len(m.entries) >= mediaAuthorityCompileMemoEntries {
			clear(m.entries)
		}
	}
	m.entries[key] = mediaAuthorityCompileMemoEntry{value: value, readAt: readAt, storedAt: time.Now()}
}

// memoizedCompileInput returns the value for key from the memo when an entry
// may serve this row, and otherwise loads it and keeps it for later rows. Rows
// of one claim share a load in flight; rows of different claims do not, since
// a load for one claim may have begun before another's trigger. Outside an
// obligation worker there is no memo and load runs every time. A failed load is
// not kept.
func memoizedCompileInput[T any](ctx context.Context, key string, load func() (T, error)) (T, error) {
	scope, _ := ctx.Value(mediaAuthorityCompileMemoContextKey{}).(*mediaAuthorityCompileScope) //nolint:errcheck // absent means a compile outside the obligation worker
	if scope == nil || scope.memo == nil || scope.triggeredAt.IsZero() || scope.claimedAt.IsZero() {
		return load()
	}
	memo := scope.memo
	if cached, ok := memo.lookup(key, scope.triggeredAt); ok {
		if value, typed := cached.(T); typed {
			return value, nil
		}
	}
	flight := key + "\x00" + strconv.FormatInt(scope.claimedAt.UnixNano(), 10)
	loaded, err, _ := memo.group.Do(flight, func() (any, error) {
		value, loadErr := load()
		if loadErr != nil {
			return nil, loadErr
		}
		memo.store(key, value, scope.claimedAt)
		return value, nil
	})
	if err != nil {
		var zero T
		return zero, err
	}
	value, _ := loaded.(T) //nolint:errcheck // the group only ever stores T under this key
	return value, nil
}
