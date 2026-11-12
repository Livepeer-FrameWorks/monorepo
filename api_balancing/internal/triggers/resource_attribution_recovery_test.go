package triggers

import (
	"context"
	"testing"
	"time"

	"frameworks/api_balancing/internal/identity"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	"google.golang.org/protobuf/proto"
)

func TestProcessBillingOwnerFailureRemainsRetryable(t *testing.T) {
	p := newTestProcessor(t)
	trigger := &ipcpb.MistTrigger{TriggerType: "PROCESS_BILLING", TriggerPayload: &ipcpb.MistTrigger_ProcessBilling{
		ProcessBilling: &ipcpb.ProcessBillingEvent{StreamName: "live+unknown-owner"},
	}}
	if _, _, err := p.handleProcessBilling(trigger); err == nil {
		t.Fatal("owner resolver outage was acknowledged; the durable edge would discard billing evidence")
	}
}

func TestProcessingBillingUsesLocalArtifactOwnerAfterDeletion(t *testing.T) {
	previous := identity.Default()
	t.Cleanup(func() { identity.SetDefault(previous) })
	identity.SetDefault(identity.NewResolver(identity.Config{RegistryArtifact: func(context.Context, string) (identity.ArtifactIdentity, error) {
		return identity.ArtifactIdentity{ArtifactHash: "retired-artifact", TenantID: "tenant-owner", StreamID: "parent-stream", OriginClusterID: "origin-cell", Source: "registry"}, nil
	}}))
	p := newTestProcessor(t)
	trigger := &ipcpb.MistTrigger{TriggerPayload: &ipcpb.MistTrigger_ProcessBilling{ProcessBilling: &ipcpb.ProcessBillingEvent{StreamName: "processing+retired-artifact"}}}
	if _, _, err := p.handleProcessBilling(trigger); err != nil {
		t.Fatalf("local tombstoned artifact ownership was ignored: %v", err)
	}
	if billing := trigger.GetProcessBilling(); billing.GetTenantId() != "tenant-owner" || billing.GetStreamId() != "parent-stream" {
		t.Fatalf("processing billing lost artifact provenance: %+v", billing)
	}
}

func TestFinalProcessingBillingUsesTombstonedSourceIdentity(t *testing.T) {
	for _, test := range []struct {
		name      string
		lifecycle mediaauthoritypb.AuthorityLifecycle
		ready     bool
	}{
		{"tombstone", mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_TOMBSTONE, false},
		{"expired", mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE, true},
		{"unready", mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			p, mock, closeDB, tenantBytes, objectBytes := localAuthorityFixture(t)
			defer closeDB()
			object := &mediaauthoritypb.MediaObjectAuthority{}
			if err := proto.Unmarshal(objectBytes, object); err != nil {
				t.Fatal(err)
			}
			object.Lifecycle = test.lifecycle
			objectBytes, err := proto.Marshal(object)
			if err != nil {
				t.Fatal(err)
			}
			expectLocalPair(mock, objectBytes, tenantBytes, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), test.ready)
			trigger := &ipcpb.MistTrigger{TriggerPayload: &ipcpb.MistTrigger_ProcessBilling{ProcessBilling: &ipcpb.ProcessBillingEvent{StreamName: "live+stream-internal"}}}
			if _, _, err := p.handleProcessBilling(trigger); err != nil {
				t.Fatalf("final billing required fresh admission authority: %v", err)
			}
			if billing := trigger.GetProcessBilling(); billing.GetTenantId() != object.TenantId || billing.GetStreamId() != object.GetLiveStream().GetStreamId() {
				t.Fatalf("final billing lost signed source identity: %+v", billing)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestStreamContextCacheUsesResourceIdentity(t *testing.T) {
	p := newTestProcessor(t)
	p.streamCache.Set("source:stream-owner", streamContext{TenantID: "tenant-owner", StreamID: "stream-id"}, time.Minute)
	for _, hint := range []string{"", "tenant-node", "tenant-forged"} {
		nodeHint := hint
		info := p.applyStreamContext(&ipcpb.MistTrigger{TenantId: &nodeHint}, "live+stream-owner")
		if info.TenantID != "tenant-owner" || info.StreamID != "stream-id" {
			t.Errorf("node hint %q split a canonical resource cache entry: %+v", hint, info)
		}
	}
}

func TestResourceCacheInvalidationHoldsNewOwnedKeys(t *testing.T) {
	p := newTestProcessor(t)
	p.InvalidateTenantCache("tenant-owner")
	p.streamCache.Set("source:new-resource", streamContext{TenantID: "tenant-owner"}, time.Minute)
	if _, found := p.streamCache.Peek("source:new-resource"); found {
		t.Fatal("tenant invalidation allowed stale canonical ownership to repopulate")
	}
	p.streamCache.Set("source:another-resource", streamContext{TenantID: "another-tenant"}, time.Minute)
	if _, found := p.streamCache.Peek("source:another-resource"); !found {
		t.Fatal("tenant invalidation blocked a different owner")
	}
}

func TestBareResourceInvalidationEvictsArtifactNamespaces(t *testing.T) {
	p := newTestProcessor(t)
	for _, key := range []string{"source:shared-token", "artifact_vod:shared-token", "artifact_dvr:shared-token", "artifact_processing:shared-token"} {
		p.streamCache.Set(key, streamContext{TenantID: "tenant-owner"}, time.Minute)
	}
	p.InvalidatePlaybackAuthCache("tenant-owner", []string{"shared-token"})
	for _, key := range []string{"source:shared-token", "artifact_vod:shared-token", "artifact_dvr:shared-token", "artifact_processing:shared-token"} {
		if _, found := p.streamCache.Peek(key); found {
			t.Errorf("auth invalidation left stale resource authority in %s", key)
		}
	}
}

func TestResourceInvalidationPreservesAnotherOwnersNamespace(t *testing.T) {
	p := newTestProcessor(t)
	p.streamCache.Set("source:shared-token", streamContext{TenantID: "tenant-owner"}, time.Minute)
	p.streamCache.Set("artifact_vod:shared-token", streamContext{TenantID: "another-owner"}, time.Minute)
	p.InvalidatePlaybackAuthCache("tenant-owner", []string{"shared-token"})
	if _, found := p.streamCache.Peek("artifact_vod:shared-token"); !found {
		t.Fatal("source owner's invalidation evicted a different tenant's artifact")
	}
	p.streamCache.Set("artifact_dvr:shared-token", streamContext{TenantID: "another-owner"}, time.Minute)
	if _, found := p.streamCache.Peek("artifact_dvr:shared-token"); !found {
		t.Fatal("owner-scoped hold suppressed another tenant's resource")
	}
}
