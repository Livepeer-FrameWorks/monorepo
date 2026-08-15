package control

import (
	"sync"
	"testing"
	"time"

	"frameworks/api_sidecar/internal/storage"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func TestAdmittedIngestGeneration_PersistsAndRehydratesAcrossRestart(t *testing.T) {
	const runtimeName = "live+persisted-generation-fence"
	store, err := storage.NewIngestGenerationStore(t.TempDir() + "/generations")
	if err != nil {
		t.Fatalf("NewIngestGenerationStore: %v", err)
	}
	ingestGenerationStoreMu.Lock()
	previousStore := ingestGenerationStore
	ingestGenerationStore = store
	ingestGenerationStoreMu.Unlock()
	t.Cleanup(func() {
		ingestGenerationStoreMu.Lock()
		ingestGenerationStore = previousStore
		ingestGenerationStoreMu.Unlock()
	})

	if err = RecordAdmittedIngestGeneration(runtimeName, "generation-a", 42); err != nil {
		t.Fatalf("RecordAdmittedIngestGeneration: %v", err)
	}
	fence, _ := lockIngestFence(runtimeName, false)
	fence.generation = ""
	fence.Unlock()

	records, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	rehydrateIngestGenerationFences(records)
	if generation, known := admittedIngestGeneration(runtimeName); !known || generation != "generation-a" {
		t.Fatalf("rehydrated generation = %q, known=%v", generation, known)
	}
	if err = MarkAdmittedIngestGenerationEnded(runtimeName, 41); err != nil {
		t.Fatalf("stale connector tombstone: %v", err)
	}
	records, err = store.Load()
	if err != nil {
		t.Fatalf("reload after stale connector: %v", err)
	}
	if !records[runtimeName].Active {
		t.Fatal("stale connector close tombstoned the current generation")
	}
	if err = MarkAdmittedIngestGenerationEnded(runtimeName, 42); err != nil {
		t.Fatalf("MarkAdmittedIngestGenerationEnded: %v", err)
	}
	records, err = store.Load()
	if err != nil {
		t.Fatalf("reload tombstone: %v", err)
	}
	if records[runtimeName].Active {
		t.Fatal("ended generation remained active in persistent store")
	}
}

func TestRecordAdmittedIngestGeneration_RequiresCompleteIdentity(t *testing.T) {
	if err := RecordAdmittedIngestGeneration("", "generation", 1); err == nil {
		t.Fatal("empty runtime was accepted")
	}
	if err := RecordAdmittedIngestGeneration("live+strict", "", 1); err == nil {
		t.Fatal("empty generation was accepted")
	}
	if err := RecordAdmittedIngestGeneration("live+strict", "generation", 0); err == nil {
		t.Fatal("non-positive connector PID was accepted")
	}
	response := &ipcpb.MistTriggerResponse{Response: "live+strict", IngestGeneration: "generation"}
	handleMistTriggerResponse(response)
	if !response.GetAbort() {
		t.Fatal("accepted response without connector PID did not fail closed")
	}
}

func TestGenerationFence_ConcurrentAdmissionCannotBePruned(t *testing.T) {
	const runtimeName = "live+concurrent-generation-fence"
	ingestGenerationStoreMu.Lock()
	previousStore := ingestGenerationStore
	ingestGenerationStore = nil
	ingestGenerationStoreMu.Unlock()
	t.Cleanup(func() {
		ingestGenerationStoreMu.Lock()
		ingestGenerationStore = previousStore
		ingestGenerationStoreMu.Unlock()
	})

	fence, _ := lockIngestFence(runtimeName, true)
	fence.generation = "ended"
	fence.connectorPID = 1
	fence.active = false
	fence.Unlock()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := RecordAdmittedIngestGeneration(runtimeName, "accepted", 2); err != nil {
			t.Errorf("RecordAdmittedIngestGeneration: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		evictInMemoryGenerationFences([]string{runtimeName})
	}()
	wg.Wait()

	fence, ok := lockIngestFence(runtimeName, false)
	if !ok {
		t.Fatal("concurrent eviction orphaned the admitted generation fence")
	}
	defer fence.Unlock()
	if !fence.active || fence.generation != "accepted" || fence.connectorPID != 2 {
		t.Fatalf("admitted fence = %+v, want active accepted generation", fence)
	}
}

func TestGenerationFence_SameRuntimeWaitDoesNotConvoyOtherRuntimes(t *testing.T) {
	held, _ := lockIngestFence("live+held", true)
	waiterDone := make(chan struct{})
	go func() {
		waiting, _ := lockIngestFence("live+held", false)
		waiting.Unlock()
		close(waiterDone)
	}()

	deadline := time.Now().Add(time.Second)
	for {
		admittedIngestGenerations.Lock()
		references := held.references
		admittedIngestGenerations.Unlock()
		if references == 2 {
			break
		}
		if time.Now().After(deadline) {
			held.Unlock()
			t.Fatal("same-runtime waiter did not pin its fence")
		}
		time.Sleep(time.Millisecond)
	}

	otherDone := make(chan struct{})
	go func() {
		other, _ := lockIngestFence("live+other", true)
		other.Unlock()
		close(otherDone)
	}()
	select {
	case <-otherDone:
	case <-time.After(250 * time.Millisecond):
		held.Unlock()
		t.Fatal("same-runtime waiter held the global fence map lock")
	}
	held.Unlock()
	select {
	case <-waiterDone:
	case <-time.After(time.Second):
		t.Fatal("same-runtime waiter did not resume")
	}
}
