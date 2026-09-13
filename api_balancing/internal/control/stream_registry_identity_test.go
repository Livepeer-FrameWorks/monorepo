package control

import (
	"testing"
	"time"
)

func TestResolveSourceIdentityHydratesRoutingOnlyEntry(t *testing.T) {
	response := nativeResp()
	client := &fakeCommodore{resp: response}
	registry := NewStreamRegistry(client, "cell", time.Minute)
	registry.UpsertLocalSource(StreamEntry{TenantID: response.TenantId, InternalName: response.InternalName,
		Locations: map[string]Location{"cell": {SourceActive: true, SourceGeneration: "generation"}}})
	if _, err := registry.ResolveSourceIdentity(t.Context(), "foreign", response.InternalName); err == nil || client.hits != 0 {
		t.Fatal("foreign tenant triggered identity hydration")
	}
	for range 2 {
		entry, err := registry.ResolveSourceIdentity(t.Context(), response.TenantId, response.InternalName)
		if err != nil || entry.StreamID != response.StreamId || entry.TenantID != response.TenantId {
			t.Fatalf("identity: %+v, %v", entry, err)
		}
	}
	if client.hits != 1 {
		t.Fatalf("hydration was not cached: %d calls", client.hits)
	}
	entry, found, err := registry.SourceSnapshot(t.Context(), response.TenantId, response.InternalName)
	if err != nil || !found || !entry.Locations["cell"].SourceActive || entry.Locations["cell"].SourceGeneration != "generation" {
		t.Fatalf("identity hydration changed runtime source evidence: %+v, %v", entry, err)
	}
}
