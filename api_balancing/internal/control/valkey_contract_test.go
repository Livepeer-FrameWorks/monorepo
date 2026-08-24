//go:build schema_verify

package control

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockervalkey"
)

func TestStreamRegistryContracts_RealValkey(t *testing.T) {
	engine := dockervalkey.Start(t)
	ctx := context.Background()
	store := NewRedisRegistryStore(engine.Client, "registry-contract")
	entry := StreamEntry{InternalName: "live+one"}
	payload, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	upsert := RegistryChange{InstanceID: "instance-a", Entity: RegistryEntitySource, Operation: RegistryOpUpsert, Key: entry.InternalName, Payload: payload, SourceRevision: 10}
	if applied, setErr := store.SetSourceRevisioned(ctx, entry, upsert, 10); setErr != nil || !applied {
		t.Fatalf("set source: applied=%v err=%v", applied, setErr)
	}
	staleDelete := RegistryChange{InstanceID: "instance-b", Entity: RegistryEntitySource, Operation: RegistryOpDelete, Key: entry.InternalName, SourceRevision: 9}
	if applied, deleteErr := store.DeleteSourceRevisioned(ctx, entry.InternalName, staleDelete, 9); deleteErr != nil || applied {
		t.Fatalf("stale delete applied=%v err=%v", applied, deleteErr)
	}
	deleteChange := staleDelete
	deleteChange.SourceRevision = 10
	if applied, deleteErr := store.DeleteSourceRevisioned(ctx, entry.InternalName, deleteChange, 10); deleteErr != nil || !applied {
		t.Fatalf("current delete: applied=%v err=%v", applied, deleteErr)
	}
	if applied, setErr := store.SetSourceRevisioned(ctx, entry, upsert, 9); setErr != nil || applied {
		t.Fatalf("stale upsert resurrected source: applied=%v err=%v", applied, setErr)
	}
	upsert.SourceRevision = 11
	if applied, setErr := store.SetSourceRevisioned(ctx, entry, upsert, 11); setErr != nil || !applied {
		t.Fatalf("newer upsert: applied=%v err=%v", applied, setErr)
	}
	if revision, revErr := store.GetSourceRevision(ctx, entry.InternalName); revErr != nil || revision != 11 {
		t.Fatalf("source revision=%d err=%v, want 11", revision, revErr)
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
