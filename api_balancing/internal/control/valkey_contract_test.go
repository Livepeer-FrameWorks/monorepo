//go:build schema_verify

package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockervalkey"
)

func TestStreamRegistryContracts_RealValkey(t *testing.T) {
	engine := dockervalkey.Start(t)
	ctx := context.Background()
	store := NewRedisRegistryStore(engine.Client, "registry-contract")
	entry := StreamEntry{InternalName: "live+one"}
	const highRevision = int64(9007199254740993)
	payload, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	upsert := RegistryChange{InstanceID: "instance-a", Entity: RegistryEntitySource, Operation: RegistryOpUpsert, Key: entry.InternalName, Payload: payload, SourceRevision: highRevision}
	if applied, setErr := store.SetSourceRevisioned(ctx, entry, upsert, highRevision); setErr != nil || !applied {
		t.Fatalf("set source: applied=%v err=%v", applied, setErr)
	}
	if raw, getErr := engine.Client.Get(ctx, store.keySourceRevision(entry.InternalName)).Result(); getErr != nil || raw != "9007199254740993" {
		t.Fatalf("raw source revision=%q err=%v, want exact high-range decimal", raw, getErr)
	}
	staleDelete := RegistryChange{InstanceID: "instance-b", Entity: RegistryEntitySource, Operation: RegistryOpDelete, Key: entry.InternalName, SourceRevision: highRevision - 1}
	if applied, deleteErr := store.DeleteSourceRevisioned(ctx, entry.InternalName, staleDelete, highRevision-1); deleteErr != nil || applied {
		t.Fatalf("stale delete applied=%v err=%v", applied, deleteErr)
	}
	deleteChange := staleDelete
	deleteChange.SourceRevision = highRevision
	if applied, deleteErr := store.DeleteSourceRevisioned(ctx, entry.InternalName, deleteChange, highRevision); deleteErr != nil || !applied {
		t.Fatalf("current delete: applied=%v err=%v", applied, deleteErr)
	}
	if applied, setErr := store.SetSourceRevisioned(ctx, entry, upsert, highRevision-1); setErr != nil || applied {
		t.Fatalf("stale upsert resurrected source: applied=%v err=%v", applied, setErr)
	}
	upsert.SourceRevision = highRevision + 1
	if applied, setErr := store.SetSourceRevisioned(ctx, entry, upsert, highRevision+1); setErr != nil || !applied {
		t.Fatalf("newer upsert: applied=%v err=%v", applied, setErr)
	}
	if revision, revErr := store.GetSourceRevision(ctx, entry.InternalName); revErr != nil || revision != highRevision+1 {
		t.Fatalf("source revision=%d err=%v, want %d", revision, revErr, highRevision+1)
	}

	artifact := ArtifactEntry{ArtifactHash: "artifact-one"}
	if err = store.SetArtifact(artifact); err != nil {
		t.Fatalf("set artifact: %v", err)
	}
	tail, err := store.ChangelogTail(ctx)
	if err != nil || tail == "0-0" {
		t.Fatalf("registry changelog tail=%q err=%v", tail, err)
	}
	engine.Restart(t)
	store = NewRedisRegistryStore(engine.Client, "registry-contract")
	if raw, getErr := engine.Client.Get(ctx, store.keySourceRevision(entry.InternalName)).Result(); getErr != nil || raw != "9007199254740994" {
		t.Fatalf("AOF restart source revision=%q err=%v, want exact high-range decimal", raw, getErr)
	}
	sources, err := store.GetAllSources()
	if err != nil || sources[entry.InternalName].InternalName != entry.InternalName {
		t.Fatalf("container replacement lost source: sources=%+v err=%v", sources, err)
	}
	artifacts, err := store.GetAllArtifacts()
	if err != nil || artifacts[artifact.ArtifactHash].ArtifactHash != artifact.ArtifactHash {
		t.Fatalf("container replacement lost artifact: artifacts=%+v err=%v", artifacts, err)
	}
	if got, tailErr := store.ChangelogTail(ctx); tailErr != nil || got != tail {
		t.Fatalf("container replacement lost changelog: got=%q want=%q err=%v", got, tail, tailErr)
	}

	placementStore := NewRedisRegistryStore(engine.Client, "placement-contract")
	sourceStore := NewRedisRegistryStore(engine.Client, "source-observation-contract")
	sourceReader := NewStreamRegistry(nil, "source-observation-contract", time.Minute)
	sourceEntry := StreamEntry{TenantID: "tenant", InternalName: "publisher-observation", IngestMode: IngestPush,
		Locations: map[string]Location{"source-observation-contract": {SourceActive: true, OwnerNodeID: "publisher", SourceGeneration: "generation", SourceRevision: highRevision}}}
	sourceReader.UpsertLocalSource(sourceEntry)
	sourceReader.redisStore = sourceStore
	sourceEntry.Locations = cloneLocations(sourceEntry.Locations)
	withdrawal := sourceEntry.Locations["source-observation-contract"]
	withdrawal.SourceActive, withdrawal.SourceRevision = false, highRevision+1
	sourceEntry.Locations["source-observation-contract"] = withdrawal
	if applied, sourceErr := sourceStore.SetSourceRevisioned(ctx, sourceEntry, RegistryChange{}, highRevision+1); sourceErr != nil || !applied {
		t.Fatalf("write shared source withdrawal: %v, %v", applied, sourceErr)
	}
	if observed, found, sourceErr := sourceReader.SourceSnapshot(ctx, "tenant", sourceEntry.InternalName); sourceErr != nil || !found || observed.Locations["source-observation-contract"].SourceActive || observed.Locations["source-observation-contract"].SourceRevision != highRevision+1 {
		t.Fatalf("real engine source reader reused stale ownership: %+v, %v, %v", observed, found, sourceErr)
	}
	if applied, sourceErr := sourceStore.DeleteSourceRevisioned(ctx, sourceEntry.InternalName, RegistryChange{}, highRevision+1); sourceErr != nil || !applied {
		t.Fatalf("delete shared source: %v, %v", applied, sourceErr)
	}
	if observed, found, sourceErr := sourceReader.SourceSnapshot(ctx, "tenant", sourceEntry.InternalName); sourceErr != nil || found || observed.TenantID != "" {
		t.Fatalf("real engine missing source fell back to cached publisher: %+v, %v, %v", observed, found, sourceErr)
	}
	inbound := func(node string) InboundPull {
		pull := testInbound(node)
		pull.SourceMediaClusterID = "source-media"
		return pull
	}
	outbound := func(node string) OutboundPull {
		pull := testOutbound(node)
		pull.SourceMediaClusterID = "source-media"
		return pull
	}
	replica := func(id string) *StreamRegistry {
		registry := NewStreamRegistry(nil, "placement-contract", time.Minute)
		registry.redisStore, registry.instanceID = placementStore, id
		return registry
	}
	var workers sync.WaitGroup
	results := make(chan error, 8)
	for i := range 8 {
		workers.Go(func() {
			_, recordErr := replica(fmt.Sprintf("writer-%d", i)).RecordInboundPull(ctx, "placement-stream", inbound(fmt.Sprintf("edge-%d", i)))
			results <- recordErr
		})
	}
	workers.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	stale, found, err := placementStore.GetSource(ctx, "placement-stream")
	if err != nil || !found || len(activeInboundPulls(stale.Locations["placement-contract"])) != 8 {
		t.Fatalf("concurrent pull destinations were lost: %+v %v", stale, err)
	}
	first := stale.Locations["placement-contract"].InboundPulls["edge-0"]
	observed := stale.Locations["placement-contract"].InboundPulls["edge-1"]
	if marked, markErr := replica("observer").MarkInboundDestinationObserved(ctx, "placement-stream", "edge-1", observed.AttemptID); markErr != nil || !marked {
		t.Fatalf("shared destination observation failed: %v", markErr)
	}
	if _, clearErr := replica("finisher").ClearInboundPull(ctx, "placement-stream", "edge-0", first.AttemptID); clearErr != nil {
		t.Fatal(clearErr)
	}
	staleChange := RegistryChange{InstanceID: "stale-writer", Entity: RegistryEntitySource, Operation: RegistryOpUpsert, Key: "placement-stream"}
	if applied, mergeErr := placementStore.SetSourceRevisioned(ctx, stale, staleChange, 0); mergeErr != nil || !applied {
		t.Fatalf("stale metadata merge: %t %v", applied, mergeErr)
	}
	engine.Restart(t)
	placementStore = NewRedisRegistryStore(engine.Client, "placement-contract")
	recovered, found, err := placementStore.GetSource(ctx, "placement-stream")
	if err != nil || !found || !recovered.Locations["placement-contract"].InboundPulls["edge-0"].Cleared || len(activeInboundPulls(recovered.Locations["placement-contract"])) != 7 {
		t.Fatalf("AOF restart lost destination records or their clearing fence: %+v %v", recovered, err)
	}
	if pull := recovered.Locations["placement-contract"].InboundPulls["edge-1"]; pull.Cleared || !pull.DestinationObserved || pull.AttemptID != observed.AttemptID {
		t.Fatalf("restart or stale merge lost observed source binding: %+v", pull)
	}
	for _, pull := range recovered.Locations["placement-contract"].InboundPulls {
		if pull.SourceMediaClusterID != "source-media" || pull.SourceClusterID != "source-cluster" {
			t.Fatalf("restart or tombstone merge lost source cell/virtual identity: %+v", pull)
		}
	}
	if _, replayErr := replica("late-acceptance").RecordInboundPull(ctx, "placement-stream", first); !errors.Is(replayErr, ErrReplicationConflict) {
		t.Fatalf("completed attempt reopened after AOF restart: %v", replayErr)
	}
	unbound := inbound("edge-0")
	unbound.SourceGeneration, unbound.SourceRevision = "", 0
	if _, downgradeErr := replica("unbound-source").RecordInboundPull(ctx, "placement-stream", unbound); !errors.Is(downgradeErr, ErrReplicationConflict) {
		t.Fatalf("source generation downgraded after AOF restart: %v", downgradeErr)
	}
	replacement, err := replica("replacement").RecordInboundPull(ctx, "placement-stream", inbound("edge-0"))
	if err != nil || replacement.AttemptID == first.AttemptID {
		t.Fatalf("replacement attempt did not advance: %+v %v", replacement, err)
	}
	if _, clearErr := replica("late-finisher").ClearInboundPull(ctx, "placement-stream", "edge-0", first.AttemptID); !errors.Is(clearErr, ErrReplicationConflict) {
		t.Fatalf("late completion cleared replacement: %v", clearErr)
	}

	markedPull, markedFound, markerErr := replica("marker-reader").CurrentInboundPull(ctx, "placement-stream", "edge-2")
	if markerErr != nil || !markedFound {
		t.Fatal("placement marker fixture has no physical pull")
	}
	if markerErr = replica("marker-writer").RequireInboundPlacement(ctx, "placement-stream", markedPull); markerErr != nil {
		t.Fatal(markerErr)
	}
	replayed, markerErr := replica("unmarked-writer").RecordInboundPull(ctx, "placement-stream", markedPull)
	if markerErr != nil || !replayed.PlacementRequired || replayed.PlacementDemand != "" {
		t.Fatalf("unmarked replay lost durable admission requirement: %+v, %v", replayed, markerErr)
	}
	markedPull, markedFound, markerErr = replica("marker-restarted").CurrentInboundPull(ctx, "placement-stream", "edge-2")
	if markerErr != nil || !markedFound || !markedPull.PlacementRequired {
		t.Fatalf("restarted replica lost source admission requirement: %+v, %v", markedPull, markerErr)
	}

	results = make(chan error, 8)
	for i := range 8 {
		workers.Go(func() {
			_, recordErr := replica(fmt.Sprintf("source-%d", i)).RecordOutboundPull(ctx, "placement-stream", outbound(fmt.Sprintf("remote-%d", i)))
			results <- recordErr
		})
	}
	workers.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	staleOutbound, found, err := placementStore.GetSource(ctx, "placement-stream")
	if err != nil || !found || len(staleOutbound.Locations["placement-contract"].OutboundPullers) != 8 {
		t.Fatalf("concurrent source handoffs lost: %+v %v", staleOutbound, err)
	}
	oldOutbound := staleOutbound.Locations["placement-contract"].OutboundPullers[0]
	if clearErr := replica("source-clear").ClearOutboundPull(ctx, "placement-stream", oldOutbound.DestClusterID, oldOutbound.DestNodeID, oldOutbound.AttemptID, time.Time{}); clearErr != nil {
		t.Fatal(clearErr)
	}
	if applied, mergeErr := placementStore.SetSourceRevisioned(ctx, staleOutbound, staleChange, 0); mergeErr != nil || !applied {
		t.Fatalf("stale outbound merge: %t %v", applied, mergeErr)
	}
	engine.Restart(t)
	placementStore = NewRedisRegistryStore(engine.Client, "placement-contract")
	recoveredOutbound, found, err := placementStore.GetSource(ctx, "placement-stream")
	if err != nil || !found || len(recoveredOutbound.Locations["placement-contract"].OutboundPullers) != 7 || len(activeInboundPulls(recoveredOutbound.Locations["placement-contract"])) != 8 {
		t.Fatalf("AOF restart lost independent source/destination records: %+v %v", recoveredOutbound, err)
	}
	if !recoveredOutbound.Locations["placement-contract"].InboundPulls["edge-2"].PlacementRequired {
		t.Fatal("AOF restart lost the source admission requirement")
	}
	for _, pull := range recoveredOutbound.Locations["placement-contract"].OutboundPullers {
		if pull.SourceMediaClusterID != "source-media" {
			t.Fatalf("restart lost source-side virtual binding: %+v", pull)
		}
	}
	newOutbound, err := replica("source-replace").RecordOutboundPull(ctx, "placement-stream", outbound(oldOutbound.DestNodeID))
	if err != nil || newOutbound.AttemptID == oldOutbound.AttemptID {
		t.Fatalf("outbound replacement attempt did not advance: %+v %v", newOutbound, err)
	}
	if err := replica("source-late").ClearOutboundPull(ctx, "placement-stream", oldOutbound.DestClusterID, oldOutbound.DestNodeID, oldOutbound.AttemptID, time.Time{}); !errors.Is(err, ErrReplicationConflict) {
		t.Fatalf("old source completion cleared replacement: %v", err)
	}
}

func TestRelayGrantContracts_RealValkey(t *testing.T) {
	engine := dockervalkey.Start(t)
	previousCluster := GetLocalClusterID()
	SetLocalClusterID("relay-contract")
	SetRelayGrantRedis(engine.Client)
	t.Cleanup(func() {
		SetRelayGrantRedis(nil)
		SetLocalClusterID(previousCluster)
	})

	id, err := MintRelayGrant("artifact-one", "node-one", []string{"/media.mp4", "/media.mp4.dtsh"})
	if err != nil {
		t.Fatalf("mint relay grant: %v", err)
	}
	grant, found := lookupRelayGrant(id)
	if allowed, reason := relayGrantAllows(grant, found, "node-one", "artifact-one", "/media.mp4"); !allowed || reason != "" {
		t.Fatalf("minted grant denied: allowed=%v reason=%q", allowed, reason)
	}
	if allowed, _ := relayGrantAllows(grant, found, "node-two", "artifact-one", "/media.mp4"); allowed {
		t.Fatal("grant authorized a different serving node")
	}
	if allowed, _ := relayGrantAllows(grant, found, "node-one", "artifact-one", "/other.mp4"); allowed {
		t.Fatal("grant authorized a different path")
	}

	engine.Restart(t)
	SetRelayGrantRedis(engine.Client)
	grant, found = lookupRelayGrant(id)
	if allowed, reason := relayGrantAllows(grant, found, "node-one", "artifact-one", "/media.mp4.dtsh"); !allowed || reason != "" {
		t.Fatalf("container replacement lost grant: allowed=%v reason=%q", allowed, reason)
	}
}
