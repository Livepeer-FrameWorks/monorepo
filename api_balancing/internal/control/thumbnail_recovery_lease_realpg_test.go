//go:build schema_verify

package control

import (
	"context"
	"sync"
	"testing"
	"time"
)

// Exercises the HA-safe recovery claim (invariant I6) against the REAL foghorn.sql baseline: two concurrent
// workers never lease the same attempt, a backed-off (poison) attempt is not re-selected while other due lost
// completions are, and the backlog reflects actionable work.
func TestThumbnailRecoveryLease_RealPG(t *testing.T) {
	conn := startRealPG(t)
	ctx := context.Background()
	files := []string{"poster.jpg", "sprite.jpg", "sprite.vtt"}

	// Helper: create a stuck-incomplete ('assigned') attempt with a future expiry so it is eligible for re-drive.
	mkAttempt := func(id, asset string) {
		t.Helper()
		if ok, err := ClaimThumbnailAttempt(ctx, conn, id, "tenant-a", asset, "node-1", "cluster-a", files, time.Now().Add(time.Hour)); err != nil || !ok {
			t.Fatalf("claim %s: ok=%v err=%v", id, ok, err)
		}
	}

	t.Run("two workers never lease the same attempt", func(t *testing.T) {
		for i, id := range []string{"tw-1", "tw-2", "tw-3", "tw-4", "tw-5", "tw-6"} {
			mkAttempt(id, "asset-tw-"+string(rune('a'+i)))
		}
		now := time.Now()
		staleBefore := now.Add(time.Hour) // every fresh attempt counts as "stale" (updated_at < now+1h)

		var wg sync.WaitGroup
		results := make([][]ClaimedRecoveryAttempt, 2)
		for w := 0; w < 2; w++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				claimed, err := ClaimStuckIncompleteThumbnailAttempts(ctx, conn, staleBefore, time.Minute, 6)
				if err != nil {
					t.Errorf("worker %d claim: %v", idx, err)
					return
				}
				results[idx] = claimed
			}(w)
		}
		wg.Wait()

		seen := map[string]int{}
		for _, batch := range results {
			for _, c := range batch {
				seen[c.AttemptID]++
			}
		}
		for id, n := range seen {
			if n != 1 {
				t.Fatalf("attempt %s was leased by %d workers; a lease must be exclusive", id, n)
			}
		}
		// An immediate re-claim finds everything still leased → nothing due.
		if again, err := ClaimStuckIncompleteThumbnailAttempts(ctx, conn, staleBefore, time.Minute, 6); err != nil || len(again) != 0 {
			t.Fatalf("leased attempts must not be re-claimable; got %d (err=%v)", len(again), err)
		}
	})

	t.Run("a backed-off poison attempt does not starve a due one", func(t *testing.T) {
		mkAttempt("poison-x", "asset-poison")
		mkAttempt("due-y", "asset-due")
		now := time.Now()
		staleBefore := now.Add(time.Hour)

		if n, err := ThumbnailRecoveryBacklog(ctx, conn, staleBefore); err != nil || n != 2 {
			t.Fatalf("initial backlog = %d (err=%v), want 2", n, err)
		}

		claimed, err := ClaimStuckIncompleteThumbnailAttempts(ctx, conn, staleBefore, time.Minute, 10)
		if err != nil || len(claimed) != 2 {
			t.Fatalf("claim: got %d (err=%v), want 2", len(claimed), err)
		}
		byID := map[string]ClaimedRecoveryAttempt{}
		for _, c := range claimed {
			byID[c.AttemptID] = c
		}
		// poison-x can't be re-driven → back it off FAR into the future (1h) so it is not re-selected. due-y models
		// a transient miss that cleared: release its lease + backoff (recovery_next_attempt_at = NULL) so it is due
		// again immediately. Using a NULL due-time (rather than NOW()+0) keeps the "due" assertion independent of any
		// host↔DB clock skew — a NULL next_attempt_at is always due; a NOW()+1h one never is within skew tolerance.
		if err := BackoffThumbnailRecovery(ctx, conn, "poison-x", byID["poison-x"].Token, time.Hour, "staging never uploaded"); err != nil {
			t.Fatalf("backoff poison: %v", err)
		}
		if err := SettleThumbnailRecoveryDone(ctx, conn, "due-y", byID["due-y"].Token); err != nil {
			t.Fatalf("settle due: %v", err)
		}

		// Backlog now reflects only actionable work: due-y is due, poison-x is backed off out of view.
		if n, err := ThumbnailRecoveryBacklog(ctx, conn, staleBefore); err != nil || n != 1 {
			t.Fatalf("post-backoff backlog = %d (err=%v), want 1 (poison excluded)", n, err)
		}
		// A fresh claim returns ONLY due-y — the poison row cannot occupy a slot / starve it.
		next, err := ClaimStuckIncompleteThumbnailAttempts(ctx, conn, staleBefore, time.Minute, 10)
		if err != nil {
			t.Fatalf("re-claim: %v", err)
		}
		if len(next) != 1 || next[0].AttemptID != "due-y" {
			ids := make([]string, 0, len(next))
			for _, c := range next {
				ids = append(ids, c.AttemptID)
			}
			t.Fatalf("re-claim = %v, want exactly [due-y] (poison-x backed off)", ids)
		}
	})
}
