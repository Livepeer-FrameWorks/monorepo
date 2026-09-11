package triggers

import (
	"context"
	"testing"
	"time"

	localauthority "frameworks/api_balancing/internal/mediaauthority"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/protobuf/proto"
)

func TestArtifactSourceShadowRejectsPlacementSchemaBeforeIO(t *testing.T) {
	for _, schemas := range [][2]uint32{{1, 2}, {2, 1}, {2, 2}} {
		counter := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_shadow_outcomes_total"}, []string{"outcome"})
		processor := &Processor{mediaAuthorityStore: &localauthority.Store{}, metrics: &ProcessorMetrics{MediaAuthorityShadow: counter}}
		snapshot := localauthority.SourceSnapshot{Tenant: localauthority.TenantSnapshot{Authority: &mediapb.TenantAuthority{SchemaVersion: schemas[0]}},
			Object: localauthority.MediaObjectSnapshot{Authority: &mediapb.MediaObjectAuthority{SchemaVersion: schemas[1], InternalName: "internal"}}}
		processor.promoteLocalArtifactSourceIfMatching(context.Background(), &commodorepb.ResolveArtifactInternalNameResponse{Found: true}, snapshot)
		if got := testutil.ToFloat64(counter.WithLabelValues("artifact_source_mismatch")); got != 1 {
			t.Fatalf("schema %v was not rejected: %v", schemas, got)
		}
	}
}

func TestPushShadowRejectsPlacementObjectAndTenant(t *testing.T) {
	for _, placementObject := range []bool{false, true} {
		p, mock, closeDB, tenantBytes, objectBytes := localAuthorityFixture(t)
		t.Cleanup(closeDB)
		tenant, object := &mediapb.TenantAuthority{}, &mediapb.MediaObjectAuthority{}
		if err := proto.Unmarshal(tenantBytes, tenant); err != nil {
			t.Fatal(err)
		}
		if err := proto.Unmarshal(objectBytes, object); err != nil {
			t.Fatal(err)
		}
		wantOutcome := "ingest_tenant_mismatch"
		if placementObject {
			object.SchemaVersion = sharedauthority.PlacementSchemaVersion
			wantOutcome = "ingest_object_mismatch"
		} else {
			tenant.SchemaVersion = sharedauthority.PlacementSchemaVersion
		}
		objectBytes, err := proto.Marshal(object)
		if err != nil {
			t.Fatal(err)
		}
		tenantBytes, err = proto.Marshal(tenant)
		if err != nil {
			t.Fatal(err)
		}
		live := object.GetLiveStream()
		connected := &commodorepb.ValidateStreamKeyResponse{Valid: true, TenantId: object.TenantId, UserId: object.UserId, StreamId: live.StreamId,
			PlaybackId: object.PlaybackId, InternalName: object.InternalName, IsRecordingEnabled: live.RecordingEnabled, ProcessesJson: live.ProcessesJson, DvrProcessesJson: live.DvrProcessesJson,
			OriginClusterId: proto.String(object.OriginClusterId), OfficialClusterId: proto.String(tenant.OfficialClusterId), BillingModel: localBillingModel(tenant.BillingModel),
			AuthorityClusterPeers: localClusterPeers(tenant), TenantResourceLimits: localTenantResourceLimits(tenant, "cluster-a"), DvrPolicy: tenant.DvrPolicy, Allowances: tenant.Allowances}
		if !sameLocalTenantIngestDecision(tenant, "cluster-a", connected) {
			t.Fatal("fixture does not match legacy tenant admission")
		}
		counter := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_shadow_outcomes_total"}, []string{"outcome"})
		p.metrics = &ProcessorMetrics{MediaAuthorityShadow: counter}
		expectLocalObject(mock, objectBytes, time.Now().Add(time.Hour), false)
		if !placementObject {
			expectLocalTenant(mock, tenantBytes, time.Now().Add(time.Hour), true)
		}
		p.promoteLocalIngestIfMatching(context.Background(), "sk_local", "cluster-a", connected)
		if got := testutil.ToFloat64(counter.WithLabelValues(wantOutcome)); got != 1 {
			t.Fatalf("placement reached legacy push promotion: outcome=%s count=%v", wantOutcome, got)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPlaybackShadowRejectsPlacementObjectAndTenant(t *testing.T) {
	for _, placementObject := range []bool{false, true} {
		p, mock, closeDB, tenantBytes, objectBytes := localAuthorityFixture(t)
		t.Cleanup(closeDB)
		object, tenant := &mediapb.MediaObjectAuthority{}, &mediapb.TenantAuthority{}
		if err := proto.Unmarshal(objectBytes, object); err != nil {
			t.Fatal(err)
		}
		if err := proto.Unmarshal(tenantBytes, tenant); err != nil {
			t.Fatal(err)
		}
		wantOutcome := "policy_not_comparable"
		if placementObject {
			object.SchemaVersion = sharedauthority.PlacementSchemaVersion
			wantOutcome = "object_mismatch"
		} else {
			tenant.SchemaVersion = sharedauthority.PlacementSchemaVersion
		}
		objectBytes, err := proto.Marshal(object)
		if err != nil {
			t.Fatal(err)
		}
		tenantBytes, err = proto.Marshal(tenant)
		if err != nil {
			t.Fatal(err)
		}
		counter := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_shadow_outcomes_total"}, []string{"outcome"})
		p.metrics = &ProcessorMetrics{MediaAuthorityShadow: counter}
		client, cleanup, stub := setupCommodoreClientWithStub(t, nil, nil)
		t.Cleanup(cleanup)
		stub.resolvePlaybackResponse = &commodorepb.ResolvePlaybackPolicyResponse{TenantId: object.TenantId, Type: "public"}
		p.SetCommodoreClient(client)
		expectLocalObject(mock, objectBytes, time.Now().Add(time.Hour), false)
		if !placementObject {
			expectLocalTenant(mock, tenantBytes, time.Now().Add(time.Hour), true)
		}
		p.promoteLocalPlaybackIfMatching(context.Background(), object.PlaybackId, localStreamTarget(object, nil), streamContext{}, &BillingStatus{TenantID: object.TenantId, BillingModel: "postpaid", State: BillingStatusHealthy})
		if got := testutil.ToFloat64(counter.WithLabelValues(wantOutcome)); got != 1 {
			t.Fatalf("placement reached legacy playback promotion: outcome=%s count=%v", wantOutcome, got)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	}
}
