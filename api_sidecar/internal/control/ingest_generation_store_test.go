package control

import (
	"sync"
	"testing"
	"time"

	"frameworks/api_sidecar/internal/storage"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
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
	resetControlState(t)
	if err := RecordAdmittedIngestGeneration("", "generation", 1); err == nil {
		t.Fatal("empty runtime was accepted")
	}
	if err := RecordAdmittedIngestGeneration("live+strict", "", 1); err == nil {
		t.Fatal("empty generation was accepted")
	}
	if err := RecordAdmittedIngestGeneration("live+strict", "generation", 0); err == nil {
		t.Fatal("non-positive connector PID was accepted")
	}
	responseCh := make(chan *ipcpb.MistTriggerResponse, 1)
	pendingMutex <- struct{}{}
	pendingMistTriggers["strict-push"] = pendingMistTrigger{
		responseCh:  responseCh,
		triggerType: string(mist.TriggerPushRewrite),
	}
	<-pendingMutex
	response := &ipcpb.MistTriggerResponse{RequestId: "strict-push", Response: "live+strict", IngestGeneration: "generation"}
	handleMistTriggerResponse(response)
	<-responseCh
	if !response.GetAbort() {
		t.Fatal("accepted response without connector PID did not fail closed")
	}
}

func TestHandleMistTriggerResponse_DoesNotApplyIngestFenceToOtherBlockingTriggers(t *testing.T) {
	resetControlState(t)
	tests := []struct {
		name        string
		triggerType string
		response    *ipcpb.MistTriggerResponse
	}{
		{
			name:        "play rewrite deny",
			triggerType: string(mist.TriggerPlayRewrite),
			response: &ipcpb.MistTriggerResponse{
				RequestId: "play",
				Action:    ipcpb.MistTriggerAction_MIST_TRIGGER_ACTION_DENY,
			},
		},
		{
			name:        "stream source value",
			triggerType: string(mist.TriggerStreamSource),
			response: &ipcpb.MistTriggerResponse{
				RequestId: "source",
				Response:  "balance:http://foghorn:18008",
				Action:    ipcpb.MistTriggerAction_MIST_TRIGGER_ACTION_VALUE,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			responseCh := make(chan *ipcpb.MistTriggerResponse, 1)
			pendingMutex <- struct{}{}
			pendingMistTriggers[tt.response.GetRequestId()] = pendingMistTrigger{
				responseCh:  responseCh,
				triggerType: tt.triggerType,
			}
			<-pendingMutex

			handleMistTriggerResponse(tt.response)
			got := <-responseCh
			if got.GetAbort() {
				t.Fatalf("non-ingest response was changed to abort: %+v", got)
			}
			if got.GetResponse() != tt.response.GetResponse() || got.GetAction() != tt.response.GetAction() {
				t.Fatalf("response changed: got %+v want %+v", got, tt.response)
			}
		})
	}
}

func TestHandleMistTriggerResponse_AcceptedPushPersistsBeforeDelivery(t *testing.T) {
	resetControlState(t)
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

	responseCh := make(chan *ipcpb.MistTriggerResponse, 1)
	pendingMutex <- struct{}{}
	pendingMistTriggers["accepted-push"] = pendingMistTrigger{
		responseCh:  responseCh,
		triggerType: string(mist.TriggerPushRewrite),
	}
	<-pendingMutex
	response := &ipcpb.MistTriggerResponse{
		RequestId:          "accepted-push",
		Response:           "live+accepted",
		IngestGeneration:   "generation-accepted",
		IngestConnectorPid: 88,
	}
	handleMistTriggerResponse(response)

	records, err := store.Load()
	if err != nil {
		t.Fatalf("load before delivery: %v", err)
	}
	if record := records["live+accepted"]; !record.Active || record.Generation != "generation-accepted" || record.ConnectorPID != 88 {
		t.Fatalf("accepted push was not persisted before delivery: %+v", record)
	}
	if got := <-responseCh; got.GetAbort() {
		t.Fatalf("accepted push was aborted: %+v", got)
	}
}

func TestHandleMistTriggerResponse_AbortedPushDoesNotPersist(t *testing.T) {
	resetControlState(t)
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

	responseCh := make(chan *ipcpb.MistTriggerResponse, 1)
	pendingMutex <- struct{}{}
	pendingMistTriggers["aborted-push"] = pendingMistTrigger{
		responseCh:  responseCh,
		triggerType: string(mist.TriggerPushRewrite),
	}
	<-pendingMutex
	handleMistTriggerResponse(&ipcpb.MistTriggerResponse{
		RequestId:          "aborted-push",
		Abort:              true,
		Response:           "live+denied",
		IngestGeneration:   "generation-denied",
		IngestConnectorPid: 89,
	})
	<-responseCh
	records, err := store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("aborted push persisted generation records: %+v", records)
	}
}

func TestMarkAdmittedIngestGenerationEndedExact_PreservesReplacementWithReusedPID(t *testing.T) {
	const runtimeName = "live+exact-generation-fence"
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

	if err = RecordAdmittedIngestGeneration(runtimeName, "generation-old", 77); err != nil {
		t.Fatalf("record old generation: %v", err)
	}
	if err = RecordAdmittedIngestGeneration(runtimeName, "generation-new", 77); err != nil {
		t.Fatalf("record replacement generation: %v", err)
	}
	if err = MarkAdmittedIngestGenerationEndedExact(runtimeName, "generation-old", 77); err != nil {
		t.Fatalf("mark stale generation: %v", err)
	}
	records, err := store.Load()
	if err != nil {
		t.Fatalf("load after stale acknowledgement: %v", err)
	}
	if !records[runtimeName].Active || records[runtimeName].Generation != "generation-new" {
		t.Fatalf("replacement was tombstoned: %+v", records[runtimeName])
	}

	active, err := ActiveAdmittedIngestGenerations()
	if err != nil {
		t.Fatalf("active snapshot: %v", err)
	}
	if len(active) != 1 || active[0].RuntimeName != runtimeName || active[0].Generation != "generation-new" || active[0].ConnectorPID != 77 {
		t.Fatalf("active snapshot = %+v", active)
	}

	if err = MarkAdmittedIngestGenerationEndedExact(runtimeName, "generation-new", 77); err != nil {
		t.Fatalf("mark current generation: %v", err)
	}
	records, err = store.Load()
	if err != nil {
		t.Fatalf("load tombstone: %v", err)
	}
	if records[runtimeName].Active {
		t.Fatal("current generation remained active")
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
