package control

import (
	"testing"

	"frameworks/api_sidecar/internal/storage"
)

func swapIngestGenerationStore(t *testing.T, store *storage.IngestGenerationStore) {
	t.Helper()
	ingestGenerationStoreMu.Lock()
	previousStore := ingestGenerationStore
	ingestGenerationStore = store
	ingestGenerationStoreMu.Unlock()
	t.Cleanup(func() {
		ingestGenerationStoreMu.Lock()
		ingestGenerationStore = previousStore
		ingestGenerationStoreMu.Unlock()
	})
}

// Register carries the store's active generations so Foghorn can keep the node's live sessions and
// end the rest; an ended (tombstoned) generation is not listed.
func TestLiveIngestGenerationsForRegister_ListsActiveStoreGenerations(t *testing.T) {
	store, err := storage.NewIngestGenerationStore(t.TempDir() + "/generations")
	if err != nil {
		t.Fatalf("NewIngestGenerationStore: %v", err)
	}
	swapIngestGenerationStore(t, store)
	if err = RecordAdmittedIngestGeneration("live+register-live", "generation-live", 71); err != nil {
		t.Fatalf("record live generation: %v", err)
	}
	if err = RecordAdmittedIngestGeneration("live+register-ended", "generation-ended", 72); err != nil {
		t.Fatalf("record ended generation: %v", err)
	}
	if err = MarkAdmittedIngestGenerationEnded("live+register-ended", 72); err != nil {
		t.Fatalf("end generation: %v", err)
	}

	generations, reported := liveIngestGenerationsForRegister()
	if !reported {
		t.Fatal("a readable store must mark the inventory as reported")
	}
	if len(generations) != 1 {
		t.Fatalf("generations = %+v, want only the live one", generations)
	}
	got := generations[0]
	if got.GetRuntimeName() != "live+register-live" || got.GetGeneration() != "generation-live" || got.GetConnectorPid() != 71 {
		t.Fatalf("generation = %+v", got)
	}
}

// Without a store the sidecar cannot prove which publishers it holds; an empty list must not be
// presented as an authoritative "nothing is live".
func TestLiveIngestGenerationsForRegister_UnreportedWithoutStore(t *testing.T) {
	swapIngestGenerationStore(t, nil)
	if generations, reported := liveIngestGenerationsForRegister(); reported || len(generations) != 0 {
		t.Fatalf("no store: generations=%v reported=%v, want none and unreported", generations, reported)
	}
}
