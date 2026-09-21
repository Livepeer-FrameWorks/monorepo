package mediaauthority

import (
	"context"
	"fmt"
	"sync"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"
)

// restoreFence holds back authority the cell may hold stale.
//
// A cell normally holds at least the version it last acknowledged: its version
// fence refuses anything older, and the control plane relies on that to stop
// correcting copies once a cell acknowledges. A restored database breaks it: it
// can bring back a version the cell had already replaced, and one that is still
// valid, while the replacement has since expired and can no longer be sent.
//
// While the fence is up an authority is decided on only once the control plane
// has confirmed it through a fetch or inventory begun in this fence generation. Delayed
// ordinary delivery cannot confirm it. Until then a read reports it
// hard-expired, which every decision path
// already answers by asking the control plane and, when that cannot be done,
// refusing. Nothing is removed; the fence only withholds trust.
//
// The fence is up in two cases. Durably, from a row in the cell's database:
// raised by a restore, and by the control plane finding the cell holding
// something other than what it acknowledged; lowered only when a summary of
// what the cell holds matches the control plane's record. An ordinary restart
// reads this local marker before serving and checks core in the background.
// Restoration must stop every replica and install the marker before restarting;
// core reachability alone can neither establish nor refute a restore.
type restoreFence struct {
	mu         sync.Mutex
	startup    bool
	durableAt  time.Time
	confirmed  map[string]struct{}
	generation uint64
	// answered is set once the control plane has answered a summary from this
	// process. Until then the cell keeps summarising: a database restored by
	// other means while the control plane was unreachable is found on the first
	// answer, which raises the durable fence.
	answered           bool
	recoveryPending    bool
	recoveryGeneration uint64
}

// FenceCheck is the answer to a summary of what the cell holds.
type FenceCheck struct {
	// Unsupported records a protocol mismatch, not a reason to clear a fence
	// or stop retries: the same gRPC connection can reach an upgraded core.
	Unsupported bool
	// InventoryComplete means inventory finished, or the acknowledged-set
	// digest matched with no tenant-revival confirmations outstanding.
	InventoryComplete   bool
	InventoryGeneration uint64
	// Reachable is false when the summary could not be sent or answered.
	Reachable bool
	// Checked is false when the control plane did not compare the summary (it
	// predates the comparison, or the clocks disagree): it can neither confirm
	// nor refute what the cell holds.
	Checked bool
	Matches bool
	// AsOf is when the summary was taken. A durable fence raised after it is
	// not lowered by it.
	AsOf time.Time
}

func fenceKey(kind, id string) string { return kind + "\x00" + id }

func (f *restoreFence) upLocked() bool { return f.startup || !f.durableAt.IsZero() }

// holds reports an authority the fence withholds.
func (f *restoreFence) holds(kind, id string) bool {
	if f == nil {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.upLocked() {
		return false
	}
	_, ok := f.confirmed[fenceKey(kind, id)]
	return !ok
}

// confirm records current-version confirmation begun in this fence generation. It reports
// whether that made the authority usable again.
func (f *restoreFence) confirm(kind, id string, generation uint64) bool {
	if f == nil || kind == "" || id == "" {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.upLocked() || generation != f.generation {
		return false
	}
	key := fenceKey(kind, id)
	if _, ok := f.confirmed[key]; ok {
		return false
	}
	if f.confirmed == nil {
		f.confirmed = map[string]struct{}{}
	}
	f.confirmed[key] = struct{}{}
	return true
}

// setDurableLocked adopts the durable fence as the database has it. A fence
// that comes up anew forgets what was confirmed before it: those confirmations
// may describe a database that has since been replaced.
func (f *restoreFence) setDurableLocked(at time.Time) {
	if at.IsZero() {
		f.durableAt = time.Time{}
		if !f.startup {
			f.confirmed = nil
		}
		return
	}
	if !at.Equal(f.durableAt) {
		f.confirmed = nil
		f.generation++
	}
	f.durableAt = at
}

// fencedFreshness is what a read reports for an authority: hard-expired while
// the restore fence withholds it, or while its tenant's return withholds it
// (revivalWithheld, from the row: an object copy applied before its tenant
// came back after lapsing here, see Store.Apply).
func (s *Store) fencedFreshness(freshness Freshness, kind, id string, revivalWithheld bool) Freshness {
	if revivalWithheld || (s != nil && s.fence.holds(kind, id)) {
		return FreshnessHardExpired
	}
	return freshness
}

// RaiseStartupFence loads the local restore marker before serving. Only an
// unreadable local marker or a durable restore fence withholds valid authority;
// a normal restart never waits for the control plane.
func (s *Store) RaiseStartupFence(ctx context.Context) error {
	if s == nil || s.fence == nil {
		return nil
	}
	at, err := s.loadDurableFence(ctx)
	s.fence.mu.Lock()
	defer s.fence.mu.Unlock()
	s.fence.startup = err != nil
	s.fence.generation++
	s.fence.confirmed = nil
	s.fence.recoveryPending = true
	if err != nil {
		// Unknown local state must fail closed until the marker can be read.
		return err
	}
	s.fence.setDurableLocked(at)
	return nil
}

// SettleFence applies the answer to a summary of what the cell holds.
func (s *Store) SettleFence(ctx context.Context, check FenceCheck) error {
	if s == nil || s.fence == nil {
		return nil
	}
	queries := foghorndb.New(s.db)
	var err error
	switch {
	case check.Checked && check.Matches:
		_, err = queries.LowerMediaAuthorityRestoreFence(ctx, check.AsOf.UTC())
	case check.Checked:
		err = queries.RaiseMediaAuthorityRestoreFence(ctx)
	}
	if err != nil {
		return fmt.Errorf("settle media authority restore fence: %w", err)
	}
	at, loadErr := s.loadDurableFence(ctx)
	s.fence.mu.Lock()
	defer s.fence.mu.Unlock()
	s.fence.startup = loadErr != nil
	s.fence.answered = s.fence.answered || check.Checked
	if check.InventoryComplete && check.Checked && check.Matches && check.InventoryGeneration == s.fence.recoveryGeneration {
		s.fence.recoveryPending = false
	}
	if loadErr != nil {
		return loadErr
	}
	s.fence.setDurableLocked(at)
	return nil
}

func (f *restoreFence) confirmationGeneration() uint64 {
	if f == nil {
		return 0
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.generation
}

// ReloadFence adopts the durable fence as the database has it now, so every
// replica of the cell follows one that another replica lowered or raised.
func (s *Store) ReloadFence(ctx context.Context) error {
	if s == nil || s.fence == nil {
		return nil
	}
	at, err := s.loadDurableFence(ctx)
	if err != nil {
		return err
	}
	s.fence.mu.Lock()
	s.fence.startup = false
	s.fence.setDurableLocked(at)
	s.fence.mu.Unlock()
	return nil
}

// DurablyFenced reports a durable fence.
func (s *Store) DurablyFenced() bool {
	if s == nil || s.fence == nil {
		return false
	}
	s.fence.mu.Lock()
	defer s.fence.mu.Unlock()
	return !s.fence.durableAt.IsZero()
}

// SummaryOwed reports that the cell must summarise what it holds again: a
// durable fence is up and waits for a matching summary, or the control plane
// has not yet answered any summary from this process.
func (s *Store) SummaryOwed() bool {
	if s == nil || s.fence == nil {
		return false
	}
	s.fence.mu.Lock()
	defer s.fence.mu.Unlock()
	return !s.fence.durableAt.IsZero() || !s.fence.answered || s.fence.recoveryPending
}

func (f *restoreFence) requireRecovery() {
	if f == nil {
		return
	}
	f.mu.Lock()
	f.recoveryPending = true
	f.recoveryGeneration++
	f.mu.Unlock()
}

func (s *Store) RecoveryPending() bool {
	if s == nil || s.fence == nil {
		return false
	}
	s.fence.mu.Lock()
	defer s.fence.mu.Unlock()
	return s.fence.recoveryPending
}

func (s *Store) loadDurableFence(ctx context.Context) (time.Time, error) {
	at, err := foghorndb.New(s.db).GetMediaAuthorityRestoreFence(ctx)
	if err != nil {
		return time.Time{}, fmt.Errorf("load media authority restore fence: %w", err)
	}
	if !at.After(time.Unix(0, 0)) {
		return time.Time{}, nil
	}
	return at.UTC(), nil
}
