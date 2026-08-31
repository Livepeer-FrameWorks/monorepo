package storage

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestIngestGenerationStore_RehydratesActiveAndBoundedTombstone(t *testing.T) {
	path := t.TempDir() + "/generations"
	store, err := NewIngestGenerationStore(path)
	if err != nil {
		t.Fatalf("NewIngestGenerationStore: %v", err)
	}
	if err = store.Put("live+active", "gen-active", 41); err != nil {
		t.Fatalf("Put active: %v", err)
	}
	if err = store.Put("live+ended", "gen-ended", 42); err != nil {
		t.Fatalf("Put ended: %v", err)
	}
	if err = store.MarkEnded("live+ended", "gen-ended", 42); err != nil {
		t.Fatalf("MarkEnded: %v", err)
	}

	reopened, err := NewIngestGenerationStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	records, err := reopened.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !records["live+active"].Active || records["live+active"].Generation != "gen-active" {
		t.Fatalf("active record not rehydrated: %+v", records["live+active"])
	}
	if records["live+ended"].Active || records["live+ended"].Generation != "gen-ended" {
		t.Fatalf("ended tombstone not rehydrated: %+v", records["live+ended"])
	}
}

func TestPruneIngestGenerationRecords_KeepsActiveAndExpiresEnded(t *testing.T) {
	now := time.Now()
	store, err := NewIngestGenerationStore(t.TempDir() + "/generations")
	if err != nil {
		t.Fatalf("NewIngestGenerationStore: %v", err)
	}
	store.records = map[string]IngestGenerationRecord{
		"live+active-old": {RuntimeName: "live+active-old", Generation: "active", Active: true, UpdatedAt: now.Add(-365 * 24 * time.Hour).UnixMilli()},
		"live+ended-old":  {RuntimeName: "live+ended-old", Generation: "ended", UpdatedAt: now.Add(-ingestGenerationTombstoneTTL - time.Second).UnixMilli()},
	}
	store.active = 1
	if _, err = store.prune(now); err != nil {
		t.Fatalf("prune: %v", err)
	}
	got := store.records
	if _, ok := got["live+active-old"]; !ok {
		t.Fatal("old active generation was evicted")
	}
	if _, ok := got["live+ended-old"]; ok {
		t.Fatal("expired ended tombstone was retained")
	}
}

func TestIngestGenerationStore_RequiresCompleteIdentityAndBoundsActiveRecords(t *testing.T) {
	store, err := NewIngestGenerationStore(t.TempDir() + "/generations")
	if err != nil {
		t.Fatalf("NewIngestGenerationStore: %v", err)
	}
	store.maxActive = 2
	if err = store.Put("", "generation", 1); err == nil {
		t.Fatal("empty runtime was accepted")
	}
	if err = store.Put("live+a", "", 1); err == nil {
		t.Fatal("empty generation was accepted")
	}
	if err = store.Put("live+a", "generation-a", 0); err == nil {
		t.Fatal("non-positive connector PID was accepted")
	}
	if err = store.Put("live+a", "generation-a", 1); err != nil {
		t.Fatalf("Put first active record: %v", err)
	}
	if err = store.Put("live+b", "generation-b", 2); err != nil {
		t.Fatalf("Put second active record: %v", err)
	}
	if err = store.Put("live+c", "generation-c", 3); err == nil {
		t.Fatal("active record beyond node ceiling was accepted")
	}
}

func TestIngestGenerationStore_ExactReplayPreservesAdmissionAge(t *testing.T) {
	store, err := NewIngestGenerationStore(t.TempDir() + "/generations")
	if err != nil {
		t.Fatalf("NewIngestGenerationStore: %v", err)
	}
	if err = store.Put("live+replayed", "generation-a", 71); err != nil {
		t.Fatalf("Put initial generation: %v", err)
	}
	records, err := store.Load()
	if err != nil {
		t.Fatalf("Load initial generation: %v", err)
	}
	original := records["live+replayed"].UpdatedAt
	store.mu.Lock()
	record := store.records["live+replayed"]
	record.UpdatedAt = original - int64(time.Minute/time.Millisecond)
	store.records["live+replayed"] = record
	store.mu.Unlock()
	if err = store.writeRecord(record); err != nil {
		t.Fatalf("age generation fixture: %v", err)
	}

	if err = store.Put("live+replayed", "generation-a", 71); err != nil {
		t.Fatalf("Put replayed generation: %v", err)
	}
	records, err = store.Load()
	if err != nil {
		t.Fatalf("Load replayed generation: %v", err)
	}
	if got := records["live+replayed"].UpdatedAt; got != record.UpdatedAt {
		t.Fatalf("replayed generation timestamp = %d, want preserved %d", got, record.UpdatedAt)
	}
}

func TestIngestGenerationStore_ConcurrentPerRuntimeWrites(t *testing.T) {
	store, err := NewIngestGenerationStore(t.TempDir() + "/generations")
	if err != nil {
		t.Fatalf("NewIngestGenerationStore: %v", err)
	}
	const publishers = 64
	var wg sync.WaitGroup
	errCh := make(chan error, publishers)
	for index := 1; index <= publishers; index++ {
		wg.Add(1)
		go func(pid int) {
			defer wg.Done()
			runtimeName := "live+publisher-" + time.Unix(int64(pid), 0).Format("150405")
			errCh <- store.Put(runtimeName, "generation", int64(pid))
		}(index)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent Put: %v", err)
		}
	}
	records, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(records) != publishers {
		t.Fatalf("records = %d, want %d", len(records), publishers)
	}
}

func TestIngestGenerationStore_MaintenanceRunsAsynchronouslyAfterEndedTransition(t *testing.T) {
	store, err := NewIngestGenerationStore(t.TempDir() + "/generations")
	if err != nil {
		t.Fatalf("NewIngestGenerationStore: %v", err)
	}
	if err = store.Put("live+ended-maintenance", "generation", 77); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err = store.MarkEnded("live+ended-maintenance", "generation", 77); err != nil {
		t.Fatalf("primary ended transition failed: %v", err)
	}
	store.mu.Lock()
	expired := store.records["live+ended-maintenance"]
	expired.UpdatedAt = time.Now().Add(-ingestGenerationTombstoneTTL - time.Hour).UnixMilli()
	store.records[expired.RuntimeName] = expired
	store.mu.Unlock()
	if err = store.writeRecord(expired); err != nil {
		t.Fatalf("age tombstone: %v", err)
	}
	maintenanceFailure := errors.New("prune unavailable")
	attempts := 0
	store.pruneHook = func(now time.Time) ([]string, error) {
		attempts++
		if attempts == 1 {
			return nil, maintenanceFailure
		}
		return store.prune(now)
	}
	records, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if records["live+ended-maintenance"].Active {
		t.Fatal("maintenance failure left durable generation active")
	}
	result := make(chan error, 2)
	store.SchedulePrune(func(_ []string, maintenanceErr error) { result <- maintenanceErr })
	select {
	case maintenanceErr := <-result:
		if !errors.Is(maintenanceErr, maintenanceFailure) {
			t.Fatalf("maintenance error = %v, want %v", maintenanceErr, maintenanceFailure)
		}
	case <-time.After(time.Second):
		t.Fatal("asynchronous maintenance did not run")
	}
	select {
	case maintenanceErr := <-result:
		if maintenanceErr != nil {
			t.Fatalf("automatic maintenance retry failed: %v", maintenanceErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("maintenance failure was not retried automatically")
	}
	records, err = store.Load()
	if err != nil {
		t.Fatalf("Load after retry: %v", err)
	}
	if _, ok := records["live+ended-maintenance"]; ok {
		t.Fatal("automatic maintenance retry did not evict the expired tombstone")
	}
}

func TestIngestGenerationStore_MaintenanceCoalescesCloseBurst(t *testing.T) {
	store, err := NewIngestGenerationStore(t.TempDir() + "/generations")
	if err != nil {
		t.Fatalf("NewIngestGenerationStore: %v", err)
	}
	var mu sync.Mutex
	runs := 0
	store.pruneHook = func(time.Time) ([]string, error) {
		mu.Lock()
		runs++
		mu.Unlock()
		return nil, nil
	}
	done := make(chan struct{}, 1)
	for index := 0; index < 1000; index++ {
		store.SchedulePrune(func([]string, error) {
			select {
			case done <- struct{}{}:
			default:
			}
		})
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("coalesced maintenance did not run")
	}
	time.Sleep(2 * ingestGenerationMaintenanceDelay)
	mu.Lock()
	defer mu.Unlock()
	if runs > 2 {
		t.Fatalf("1000 maintenance requests produced %d scans, want at most 2", runs)
	}
}
