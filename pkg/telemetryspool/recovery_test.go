package telemetryspool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFailedPrefixDoesNotStarveLaterRecords(t *testing.T) {
	q, err := Open(Config{Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 101; i++ {
		if err := q.Enqueue(fmt.Sprint(i), []byte("blocked")); err != nil {
			t.Fatal(err)
		}
	}
	if err := q.Enqueue("healthy", []byte("healthy")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	delivered := false
	attempts := 0
	q.Run(ctx, func(_ context.Context, payload []byte) error {
		attempts++
		if attempts > 103 {
			cancel()
			return errors.New("retrying the prefix again")
		}
		if string(payload) == "healthy" {
			delivered = true
			cancel()
			return nil
		}
		return errors.New("temporary failure")
	}, nil)
	if !delivered {
		t.Fatal("101 retrying records starved a later deliverable record")
	}
}

func TestReservationSurvivesPersistenceFailure(t *testing.T) {
	q, err := Open(Config{Directory: t.TempDir(), MaxRecords: 1, MaxBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	r, err := q.Reserve(512)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Cancel()
	if _, err := q.Reserve(1); !errors.Is(err, ErrFull) {
		t.Fatalf("reservation did not exclude another writer: %v", err)
	}
	blocked := filepath.Join(q.cfg.Directory, filename("stable"))
	if err := os.Mkdir(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	if err := r.Commit("stable", []byte("payload")); err == nil {
		t.Fatal("injected persistence failure was hidden")
	}
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	if err := r.Commit("stable", []byte("payload")); err != nil {
		t.Fatalf("retry lost its reserved capacity: %v", err)
	}
	n, _, lost := q.Stats()
	if n != 1 || lost != 0 {
		t.Fatalf("reservation retry lost the record: count=%d lost=%d", n, lost)
	}
}

func TestQuarantineRetainsOriginalAndSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	q, err := Open(Config{Directory: dir})
	if err != nil {
		t.Fatal(err)
	}
	if enqueueErr := q.Enqueue("bad", []byte("original bytes")); enqueueErr != nil {
		t.Fatal(enqueueErr)
	}
	ctx, cancel := context.WithCancel(t.Context())
	q.RunWithPolicy(ctx, func(context.Context, []byte) DeliveryOutcome {
		cancel()
		return DeliveryOutcome{QuarantineCode: "INVALID"}
	}, nil)
	raw, err := os.ReadFile(filepath.Join(dir, "quarantine", filename("bad")))
	if err != nil || string(raw) != "original bytes" {
		t.Fatalf("rejected evidence lost: %q %v", raw, err)
	}
	reopened, err := Open(Config{Directory: dir})
	if err != nil {
		t.Fatal(err)
	}
	n, _, _ := reopened.Stats()
	_, _, quarantined, readErr := reopened.DeliveryState()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if n != 0 || quarantined != 1 {
		t.Fatalf("quarantine disposition lost after restart: pending=%d quarantined=%d", n, quarantined)
	}
}

func TestLossCounterSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	q, err := Open(Config{Directory: dir, MaxRecords: 1})
	if err != nil {
		t.Fatal(err)
	}
	if enqueueErr := q.Enqueue("first", []byte("record")); enqueueErr != nil {
		t.Fatal(enqueueErr)
	}
	if enqueueErr := q.Enqueue("second", []byte("record")); !errors.Is(enqueueErr, ErrFull) {
		t.Fatal(enqueueErr)
	}
	reopened, err := Open(Config{Directory: dir, MaxRecords: 1})
	if err != nil {
		t.Fatal(err)
	}
	_, _, lost := reopened.Stats()
	if lost != 1 {
		t.Fatalf("restart erased loss counter: %d, want 1", lost)
	}
}

func TestRetryStateMigratesAndOrphansDoNotAccumulate(t *testing.T) {
	dir := t.TempDir()
	q, err := Open(Config{Directory: dir})
	if err != nil {
		t.Fatal(err)
	}
	if enqueueErr := q.Enqueue("legacy", []byte("stable")); enqueueErr != nil {
		t.Fatal(enqueueErr)
	}
	name := filename("legacy")
	legacy := persistentState{Dropped: 3, Sequence: 8, Retries: map[string]retryState{name: {Attempts: 2, After: time.Now().Add(time.Minute)}}}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := os.WriteFile(filepath.Join(dir, ".state.json"), raw, 0600); writeErr != nil {
		t.Fatal(writeErr)
	}
	reopened, err := Open(Config{Directory: dir})
	if err != nil {
		t.Fatal(err)
	}
	if reopened.state.Retries[name].Attempts != 2 || reopened.state.Sequence != 8 || reopened.dropped.Load() != 3 {
		t.Fatal("legacy retry/control evidence did not migrate")
	}
	if _, statErr := os.Stat(filepath.Join(dir, name+".retry")); statErr != nil {
		t.Fatal(statErr)
	}
	if removeErr := os.Remove(filepath.Join(dir, name)); removeErr != nil {
		t.Fatal(removeErr)
	}
	cleaned, err := Open(Config{Directory: dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(cleaned.state.Retries) != 0 {
		t.Fatal("retry state without a pending record remained indexed")
	}
	if _, statErr := os.Stat(filepath.Join(dir, name+".retry")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("orphan retry state not removed: %v", statErr)
	}
}
