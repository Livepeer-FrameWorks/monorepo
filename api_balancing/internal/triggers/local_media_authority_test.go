package triggers

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"regexp"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	localauthority "frameworks/api_balancing/internal/mediaauthority"
	"frameworks/api_balancing/internal/state"
	"github.com/DATA-DOG/go-sqlmock"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	meteringpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/metering_contract"
	tenantlimitspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/tenant_limits"
	"google.golang.org/protobuf/proto"
)

func localPayloadDigest(payload []byte) []byte {
	digest := sha256.Sum256(payload)
	return digest[:]
}

func TestLocalTenantDecisionMismatchClassifiesBoundedReasons(t *testing.T) {
	matching := func() (*mediaauthoritypb.TenantAuthority, streamContext, *BillingStatus) {
		tenant := &mediaauthoritypb.TenantAuthority{
			Lifecycle:         mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
			BillingDecision:   mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW,
			BillingModel:      mediaauthoritypb.TenantBillingModel_TENANT_BILLING_MODEL_POSTPAID,
			OfficialClusterId: "cluster-a",
			EffectiveClusterGrants: []*mediaauthoritypb.TenantClusterGrant{{
				ClusterId: "cluster-a",
			}},
		}
		info := streamContext{
			OfficialClusterID:     "cluster-a",
			AuthorityClusterPeers: []*clusterpeerpb.TenantClusterPeer{{ClusterId: "cluster-a"}},
		}
		billing := &BillingStatus{BillingModel: "postpaid", State: BillingStatusHealthy}
		return tenant, info, billing
	}

	tenant, info, billing := matching()
	if got := localTenantDecisionMismatch(tenant, info, billing); got != "" {
		t.Fatalf("matching decision classified as %q", got)
	}

	tests := []struct {
		name   string
		want   string
		mutate func(*mediaauthoritypb.TenantAuthority, *streamContext, *BillingStatus)
	}{
		{name: "lifecycle", want: "lifecycle", mutate: func(tenant *mediaauthoritypb.TenantAuthority, _ *streamContext, _ *BillingStatus) {
			tenant.Lifecycle = mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_INACTIVE
		}},
		{name: "billing decision", want: "billing_decision", mutate: func(_ *mediaauthoritypb.TenantAuthority, _ *streamContext, billing *BillingStatus) {
			billing.State = BillingStatusDenied
		}},
		{name: "billing model", want: "billing_model", mutate: func(_ *mediaauthoritypb.TenantAuthority, _ *streamContext, billing *BillingStatus) {
			billing.BillingModel = "prepaid"
		}},
		{name: "official cluster", want: "official_cluster", mutate: func(_ *mediaauthoritypb.TenantAuthority, info *streamContext, _ *BillingStatus) {
			info.OfficialClusterID = "cluster-b"
		}},
		{name: "cluster grants", want: "cluster_grants", mutate: func(_ *mediaauthoritypb.TenantAuthority, info *streamContext, _ *BillingStatus) {
			info.AuthorityClusterPeers = nil
		}},
		{name: "resource limits", want: "resource_limits", mutate: func(tenant *mediaauthoritypb.TenantAuthority, info *streamContext, _ *BillingStatus) {
			tenant.ResourceLimits = &tenantlimitspb.TenantResourceLimits{MaxStreams: 2}
			info.MaxStreams = 1
		}},
		{name: "allowances", want: "allowances", mutate: func(tenant *mediaauthoritypb.TenantAuthority, info *streamContext, _ *BillingStatus) {
			tenant.Allowances = []*meteringpb.MeterAllowance{{Meter: "delivered_minutes", IsFreeTier: true}}
			info.IsFreeTier = false
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tenant, info, billing := matching()
			test.mutate(tenant, &info, billing)
			if got := localTenantDecisionMismatch(tenant, info, billing); got != test.want {
				t.Fatalf("mismatch = %q, want %q", got, test.want)
			}
		})
	}
}

func TestLocalIngestComparatorUsesTheActualAdmissionCluster(t *testing.T) {
	tenant := &mediaauthoritypb.TenantAuthority{
		Lifecycle:          mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
		BillingDecision:    mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW,
		BillingModel:       mediaauthoritypb.TenantBillingModel_TENANT_BILLING_MODEL_POSTPAID,
		OfficialClusterId:  "cluster-a",
		PreferredClusterId: "cluster-b",
		ResourceLimits:     &tenantlimitspb.TenantResourceLimits{MaxStreams: 5, MaxViewers: 50},
		Allowances:         []*meteringpb.MeterAllowance{{Meter: "delivered_minutes", IsFreeTier: true, Used: 10, Remaining: 90}},
		EffectiveClusterGrants: []*mediaauthoritypb.TenantClusterGrant{
			{ClusterId: "cluster-a"},
			{ClusterId: "cluster-b", AllowPrivatePullSources: true, ResourceLimits: &tenantlimitspb.TenantResourceLimits{MaxStreams: 2}},
		},
	}
	connected := &commodorepb.ValidateStreamKeyResponse{
		Valid: true, BillingModel: "postpaid", OfficialClusterId: proto.String("cluster-a"),
		TenantResourceLimits: &tenantlimitspb.TenantResourceLimits{MaxStreams: 2, MaxViewers: 50},
		Allowances:           []*meteringpb.MeterAllowance{{Meter: "delivered_minutes", IsFreeTier: true, Used: 20, Remaining: 80}},
		AuthorityClusterPeers: []*clusterpeerpb.TenantClusterPeer{
			{ClusterId: "cluster-a", AccessActive: true, Role: "official"},
			{ClusterId: "cluster-b", AccessActive: true, Role: "preferred", AllowPrivatePullSources: true, ResourceLimits: &tenantlimitspb.TenantResourceLimits{MaxStreams: 2}},
		},
	}
	if !sameLocalTenantIngestDecision(tenant, "cluster-b", connected) {
		t.Fatal("realistic non-primary admission could not pass the promotion comparator")
	}
	if sameLocalTenantIngestDecision(tenant, "cluster-a", connected) {
		t.Fatal("comparator ignored the cluster-scoped resource-limit decision")
	}
}

func TestShadowPlaybackComparisonScopesFullContextToLocalCluster(t *testing.T) {
	validUntil := time.Now().Add(time.Hour)
	p, mock, closeDB, tenantBytes, objectBytes := localAuthorityFixture(t)
	defer closeDB()
	p.SetClusterID("cluster-a")

	var object mediaauthoritypb.MediaObjectAuthority
	if err := proto.Unmarshal(objectBytes, &object); err != nil {
		t.Fatal(err)
	}
	var tenant mediaauthoritypb.TenantAuthority
	if err := proto.Unmarshal(tenantBytes, &tenant); err != nil {
		t.Fatal(err)
	}
	target := localStreamTarget(&object, nil)

	client, cleanup, stub := setupCommodoreClientWithStub(t, nil, nil)
	defer cleanup()
	stub.resolvePlaybackResponse = &commodorepb.ResolvePlaybackPolicyResponse{TenantId: object.GetTenantId(), Type: "public"}
	stub.resolveStreamContextByKey = map[string]*commodorepb.ResolveStreamContextResponse{
		"stream_id:" + object.GetLiveStream().GetStreamId(): {
			Admitted: true, TenantId: object.GetTenantId(), StreamId: object.GetLiveStream().GetStreamId(),
			PlaybackId: object.GetPlaybackId(), InternalName: object.GetInternalName(), OfficialClusterId: proto.String("cluster-a"),
			ClusterPeers:          []*clusterpeerpb.TenantClusterPeer{{ClusterId: "cluster-a"}},
			AuthorityClusterPeers: localClusterPeers(&tenant),
		},
	}
	p.SetCommodoreClient(client)

	expectLocalObject(mock, objectBytes, validUntil, false)
	expectLocalTenant(mock, tenantBytes, validUntil, false)
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("UPDATE foghorn.tenant_authority_projection")).
		WithArgs(object.GetTenantId(), int64(8)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE foghorn.media_object_authority_projection")).
		WithArgs(sharedauthority.LiveStreamAuthorityID(object.GetLiveStream().GetStreamId()), int64(4)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	p.promoteLocalPlaybackIfMatching(context.Background(), object.GetPlaybackId(), target, streamContext{}, &BillingStatus{
		TenantID: object.GetTenantId(), BillingModel: "postpaid", State: BillingStatusHealthy,
	})
	if got := stub.ResolveStreamContextClusterIDs(); len(got) != 1 || got[0] != "" {
		t.Fatalf("ResolveStreamContext cluster IDs = %v, want an admission-neutral shadow read", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func localAuthorityFixture(t *testing.T) (*Processor, sqlmock.Sqlmock, func(), []byte, []byte) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	seed := make([]byte, ed25519.SeedSize)
	privateKey := ed25519.NewKeyFromSeed(seed)
	store, err := localauthority.NewStore(db, "cell-a", sharedauthority.TrustSet{"key": privateKey.Public().(ed25519.PublicKey)})
	if err != nil {
		db.Close()
		t.Fatalf("NewStore: %v", err)
	}
	tenantID := "10000000-0000-0000-0000-000000000001"
	tenant := &mediaauthoritypb.TenantAuthority{
		SchemaVersion: sharedauthority.SchemaVersion, TenantId: tenantID,
		Lifecycle:         mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
		BillingDecision:   mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW,
		BillingModel:      mediaauthoritypb.TenantBillingModel_TENANT_BILLING_MODEL_POSTPAID,
		OfficialClusterId: "cluster-a", AllowPlatformSharedPlayback: true,
		EffectiveClusterGrants: []*mediaauthoritypb.TenantClusterGrant{{
			ClusterId: "cluster-a", AccessSource: clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_PLATFORM_TIER,
			SubscriptionStatus: "active", ClusterClass: "platform_official", ControlCellId: "cell-a", EligibleServingCellIds: []string{"cell-a"},
		}},
	}
	object := &mediaauthoritypb.MediaObjectAuthority{
		SchemaVersion: sharedauthority.SchemaVersion, ObjectKind: mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM,
		TenantId: tenantID, UserId: "20000000-0000-0000-0000-000000000001", InternalName: "stream-internal",
		PlaybackId: "PlaybackKey", Lifecycle: mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
		OriginClusterId: "cluster-a", PlaybackPolicy: &mediaauthoritypb.PlaybackPolicy{Kind: mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_PUBLIC},
		Object: &mediaauthoritypb.MediaObjectAuthority_LiveStream{LiveStream: &mediaauthoritypb.LiveStreamAuthority{
			StreamId: "30000000-0000-0000-0000-000000000001", IngestMode: "push",
			PublishingCredentialSha256: sharedauthority.PublishingCredentialDigest("sk_local"),
			OutageIngestClusterId:      "cluster-a", RecordingEnabled: true,
			ProcessesJson: "[]", DvrProcessesJson: "[]",
		}},
	}
	tenantBytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(tenant)
	if err != nil {
		t.Fatalf("marshal tenant: %v", err)
	}
	objectBytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(object)
	if err != nil {
		t.Fatalf("marshal object: %v", err)
	}
	p := NewProcessor(testLogger(), nil, nil, nil, nil)
	p.SetMediaAuthorityStore(store)
	return p, mock, func() { _ = db.Close() }, tenantBytes, objectBytes
}

func TestReadyLocalIngestAuthorityVerifiesCredentialAndFencesOwner(t *testing.T) {
	p, mock, closeDB, tenantBytes, objectBytes := localAuthorityFixture(t)
	defer closeDB()
	validUntil := time.Now().Add(time.Hour)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT authority.payload, authority.payload_sha256, authority.refresh_after, authority.valid_until,")).
		WithArgs(sharedauthority.PublishingCredentialDigest("SK_LOCAL")).
		WillReturnRows(sqlmock.NewRows([]string{"payload", "payload_sha256", "refresh_after", "valid_until", "authority_id", "authority_version", "local_ingest_ready"}).
			AddRow(objectBytes, localPayloadDigest(objectBytes), time.Now().Add(time.Minute), validUntil, sharedauthority.LiveStreamAuthorityID("30000000-0000-0000-0000-000000000001"), int64(4), true))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT authority.payload, authority.payload_sha256, authority.refresh_after, authority.valid_until,")).
		WillReturnRows(sqlmock.NewRows([]string{"payload", "payload_sha256", "refresh_after", "valid_until", "authority_version", "local_read_ready", "local_ingest_ready", "local_source_ready"}).
			AddRow(tenantBytes, localPayloadDigest(tenantBytes), time.Now().Add(time.Minute), validUntil, int64(8), false, true, false))

	response, local, found, err := p.resolveReadyLocalIngest(context.Background(), "SK_LOCAL")
	if err != nil || !found || response == nil || !response.GetValid() {
		t.Fatalf("resolveReadyLocalIngest = response=%+v found=%v err=%v", response, found, err)
	}
	if response.GetInternalName() != "stream-internal" || !response.GetIsRecordingEnabled() {
		t.Fatalf("local validation response = %+v", response)
	}
	if !localOutageIngestAllowed(local, "cluster-a") {
		t.Fatal("signed outage owner was rejected")
	}
	if localOutageIngestAllowed(local, "cluster-b") {
		t.Fatal("non-owner outage cluster was admitted")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestReadyLocalIngestAuthorityRejectsMismatchedProjectedCredential(t *testing.T) {
	p, mock, closeDB, _, objectBytes := localAuthorityFixture(t)
	defer closeDB()
	var object mediaauthoritypb.MediaObjectAuthority
	if err := proto.Unmarshal(objectBytes, &object); err != nil {
		t.Fatal(err)
	}
	object.GetLiveStream().PublishingCredentialSha256 = sharedauthority.PublishingCredentialDigest("different-key")
	mismatchedBytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(&object)
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT authority.payload, authority.payload_sha256, authority.refresh_after, authority.valid_until,")).
		WithArgs(sharedauthority.PublishingCredentialDigest("presented-key")).
		WillReturnRows(sqlmock.NewRows([]string{"payload", "payload_sha256", "refresh_after", "valid_until", "authority_id", "authority_version", "local_ingest_ready"}).
			AddRow(mismatchedBytes, localPayloadDigest(mismatchedBytes), time.Now().Add(time.Minute), time.Now().Add(time.Hour), sharedauthority.LiveStreamAuthorityID(object.GetLiveStream().GetStreamId()), int64(4), true))

	response, _, found, err := p.resolveReadyLocalIngest(context.Background(), "presented-key")
	if err != nil || !found || response == nil || response.GetValid() {
		t.Fatalf("mismatched projected credential response=%+v found=%v err=%v", response, found, err)
	}
	if response.GetRejectionReason() != commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_INVALID_KEY {
		t.Fatalf("rejection reason = %v, want invalid key", response.GetRejectionReason())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestReadyLocalIngestUsesPullModeRejectionContract(t *testing.T) {
	p, mock, closeDB, _, objectBytes := localAuthorityFixture(t)
	defer closeDB()
	var object mediaauthoritypb.MediaObjectAuthority
	if err := proto.Unmarshal(objectBytes, &object); err != nil {
		t.Fatal(err)
	}
	object.GetLiveStream().IngestMode = "pull"
	pullBytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(&object)
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT authority.payload, authority.payload_sha256, authority.refresh_after, authority.valid_until,")).
		WithArgs(sharedauthority.PublishingCredentialDigest("sk_local")).
		WillReturnRows(sqlmock.NewRows([]string{"payload", "payload_sha256", "refresh_after", "valid_until", "authority_id", "authority_version", "local_ingest_ready"}).
			AddRow(pullBytes, localPayloadDigest(pullBytes), time.Now().Add(time.Minute), time.Now().Add(time.Hour), sharedauthority.LiveStreamAuthorityID(object.GetLiveStream().GetStreamId()), int64(4), true))

	response, _, found, err := p.resolveReadyLocalIngest(context.Background(), "sk_local")
	if err != nil || !found || response == nil || response.GetValid() {
		t.Fatalf("pull-mode local response=%+v found=%v err=%v", response, found, err)
	}
	if response.GetRejectionReason() != commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_PULL_MODE {
		t.Fatalf("rejection reason = %v, want pull mode", response.GetRejectionReason())
	}
}

func TestReadyLocalIngestContextDoesNotChooseOutageOwnerBeforeFrontDoorFallback(t *testing.T) {
	p, mock, closeDB, tenantBytes, objectBytes := localAuthorityFixture(t)
	defer closeDB()
	validUntil := time.Now().Add(time.Hour)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT authority.payload, authority.payload_sha256, authority.refresh_after, authority.valid_until,")).
		WithArgs(sharedauthority.PublishingCredentialDigest("sk_local")).
		WillReturnRows(sqlmock.NewRows([]string{"payload", "payload_sha256", "refresh_after", "valid_until", "authority_id", "authority_version", "local_ingest_ready"}).
			AddRow(objectBytes, localPayloadDigest(objectBytes), time.Now().Add(time.Minute), validUntil, sharedauthority.LiveStreamAuthorityID("30000000-0000-0000-0000-000000000001"), int64(4), true))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT authority.payload, authority.payload_sha256, authority.refresh_after, authority.valid_until,")).
		WillReturnRows(sqlmock.NewRows([]string{"payload", "payload_sha256", "refresh_after", "valid_until", "authority_version", "local_read_ready", "local_ingest_ready", "local_source_ready"}).
			AddRow(tenantBytes, localPayloadDigest(tenantBytes), time.Now().Add(time.Minute), validUntil, int64(8), false, true, false))

	streamCtx, handled, err := p.ResolveLocalIngestContext(context.Background(), "SK_LOCAL")
	if err != nil || !handled || streamCtx == nil || !streamCtx.GetAdmitted() {
		t.Fatalf("ResolveLocalIngestContext = %+v, %v, %v", streamCtx, handled, err)
	}
	if streamCtx.GetActiveIngestClusterId() != "" || streamCtx.GetInternalName() != "stream-internal" || len(streamCtx.GetClusterPeers()) != 0 || len(streamCtx.GetAuthorityClusterPeers()) != 1 {
		t.Fatalf("local ingest context = %+v", streamCtx)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPushRewriteUsesReadyLocalAuthorityOnlyOnSignedOutageOwner(t *testing.T) {
	installIngestSessionMintMock(t)
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	sm.SetNodeInfo("edge-node-1", "http://edge.example/view", true, nil, nil, "", "", nil)
	sm.SetNodeConnectionInfo(context.Background(), "edge-node-1", "edge-node-1:18090", "", "cluster-a", nil)
	previousRegistry := control.StreamRegistryInstance
	control.SetStreamRegistry(control.NewStreamRegistry(nil, "cluster-a", time.Minute))
	t.Cleanup(func() { control.SetStreamRegistry(previousRegistry) })

	p, mock, closeDB, tenantBytes, objectBytes := localAuthorityFixture(t)
	defer closeDB()
	validUntil := time.Now().Add(time.Hour)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT authority.payload, authority.payload_sha256, authority.refresh_after, authority.valid_until,")).
		WithArgs(sharedauthority.PublishingCredentialDigest("sk_local")).
		WillReturnRows(sqlmock.NewRows([]string{"payload", "payload_sha256", "refresh_after", "valid_until", "authority_id", "authority_version", "local_ingest_ready"}).
			AddRow(objectBytes, localPayloadDigest(objectBytes), time.Now().Add(time.Minute), validUntil, sharedauthority.LiveStreamAuthorityID("30000000-0000-0000-0000-000000000001"), int64(4), true))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT authority.payload, authority.payload_sha256, authority.refresh_after, authority.valid_until,")).
		WillReturnRows(sqlmock.NewRows([]string{"payload", "payload_sha256", "refresh_after", "valid_until", "authority_version", "local_read_ready", "local_ingest_ready", "local_source_ready"}).
			AddRow(tenantBytes, localPayloadDigest(tenantBytes), time.Now().Add(time.Minute), validUntil, int64(8), false, true, false))

	trigger := &ipcpb.MistTrigger{NodeId: "edge-node-1", TriggerPayload: &ipcpb.MistTrigger_PushRewrite{
		PushRewrite: &ipcpb.PushRewriteTrigger{Pid: 4242, TriggerUuid: "outage-trigger", TriggerUnixMillis: 1, StreamName: "sk_local", PushUrl: "rtmp://example/live/sk_local"},
	}}
	streamName, blocking, err := p.handlePushRewrite(trigger)
	if err != nil || blocking || streamName != "live+stream-internal" {
		t.Fatalf("local PUSH_REWRITE = stream=%q blocking=%v err=%v", streamName, blocking, err)
	}
	if trigger.GetTenantId() != "10000000-0000-0000-0000-000000000001" || trigger.GetClusterId() != "cluster-a" {
		t.Fatalf("local trigger identity = %+v", trigger)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPushRewriteRejectsReadyLocalAuthorityOnNonOwnerCluster(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	sm.SetNodeInfo("edge-node-2", "http://edge.example/view", true, nil, nil, "", "", nil)
	sm.SetNodeConnectionInfo(context.Background(), "edge-node-2", "edge-node-2:18090", "", "cluster-b", nil)

	controlDB, controlMock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	previousDB := control.GetDB()
	control.SetDB(controlDB)
	t.Cleanup(func() {
		control.SetDB(previousDB)
		_ = controlDB.Close()
	})
	controlMock.ExpectQuery(`tenant_id = \$1::uuid AND node_id = \$2 AND start_trigger_uuid = \$3 AND ended_at IS NULL`).
		WillReturnError(sql.ErrNoRows)

	p, mock, closeDB, tenantBytes, objectBytes := localAuthorityFixture(t)
	defer closeDB()
	validUntil := time.Now().Add(time.Hour)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT authority.payload, authority.payload_sha256, authority.refresh_after, authority.valid_until,")).
		WithArgs(sharedauthority.PublishingCredentialDigest("sk_local")).
		WillReturnRows(sqlmock.NewRows([]string{"payload", "payload_sha256", "refresh_after", "valid_until", "authority_id", "authority_version", "local_ingest_ready"}).
			AddRow(objectBytes, localPayloadDigest(objectBytes), time.Now().Add(time.Minute), validUntil, sharedauthority.LiveStreamAuthorityID("30000000-0000-0000-0000-000000000001"), int64(4), true))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT authority.payload, authority.payload_sha256, authority.refresh_after, authority.valid_until,")).
		WillReturnRows(sqlmock.NewRows([]string{"payload", "payload_sha256", "refresh_after", "valid_until", "authority_version", "local_read_ready", "local_ingest_ready", "local_source_ready"}).
			AddRow(tenantBytes, localPayloadDigest(tenantBytes), time.Now().Add(time.Minute), validUntil, int64(8), false, true, false))

	trigger := &ipcpb.MistTrigger{NodeId: "edge-node-2", TriggerPayload: &ipcpb.MistTrigger_PushRewrite{
		PushRewrite: &ipcpb.PushRewriteTrigger{Pid: 4242, TriggerUuid: "outage-nonowner", TriggerUnixMillis: 1, StreamName: "sk_local"},
	}}
	streamName, blocking, err := p.handlePushRewrite(trigger)
	if err == nil || !blocking || streamName != "" {
		t.Fatalf("non-owner PUSH_REWRITE = stream=%q blocking=%v err=%v", streamName, blocking, err)
	}
	if err := controlMock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPushRewriteHardExpiredMarkedAuthorityNeverFallsBack(t *testing.T) {
	p, mock, closeDB, _, objectBytes := localAuthorityFixture(t)
	defer closeDB()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT authority.payload, authority.payload_sha256, authority.refresh_after, authority.valid_until,")).
		WithArgs(sharedauthority.PublishingCredentialDigest("sk_local")).
		WillReturnRows(sqlmock.NewRows([]string{"payload", "payload_sha256", "refresh_after", "valid_until", "authority_id", "authority_version", "local_ingest_ready"}).
			AddRow(objectBytes, localPayloadDigest(objectBytes), time.Now().Add(-2*time.Minute), time.Now().Add(-time.Minute), sharedauthority.LiveStreamAuthorityID("30000000-0000-0000-0000-000000000001"), int64(4), true))

	trigger := &ipcpb.MistTrigger{NodeId: "edge-node-1", TriggerPayload: &ipcpb.MistTrigger_PushRewrite{
		PushRewrite: &ipcpb.PushRewriteTrigger{Pid: 4242, TriggerUuid: "expired", TriggerUnixMillis: 1, StreamName: "sk_local"},
	}}
	streamName, blocking, err := p.handlePushRewrite(trigger)
	if err == nil || !blocking || streamName != "" {
		t.Fatalf("hard-expired PUSH_REWRITE = stream=%q blocking=%v err=%v", streamName, blocking, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func expectLocalObject(mock sqlmock.Sqlmock, objectBytes []byte, validUntil time.Time, ready bool) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT authority.payload, authority.payload_sha256, authority.refresh_after, authority.valid_until,")).
		WillReturnRows(sqlmock.NewRows([]string{"payload", "payload_sha256", "refresh_after", "valid_until", "authority_id", "authority_version", "local_read_ready"}).
			AddRow(objectBytes, localPayloadDigest(objectBytes), time.Now().Add(time.Minute), validUntil, sharedauthority.LiveStreamAuthorityID("30000000-0000-0000-0000-000000000001"), int64(4), ready))
}

func expectLocalTenant(mock sqlmock.Sqlmock, tenantBytes []byte, validUntil time.Time, ready bool) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT authority.payload, authority.payload_sha256, authority.refresh_after, authority.valid_until,")).
		WillReturnRows(sqlmock.NewRows([]string{"payload", "payload_sha256", "refresh_after", "valid_until", "authority_version", "local_read_ready", "local_ingest_ready", "local_source_ready"}).
			AddRow(tenantBytes, localPayloadDigest(tenantBytes), time.Now().Add(time.Minute), validUntil, int64(8), ready, false, false))
}

func TestReadyLocalAuthorityResolvesWithoutControlPlane(t *testing.T) {
	validUntil := time.Now().Add(time.Hour)
	p, mock, closeDB, tenantBytes, objectBytes := localAuthorityFixture(t)
	defer closeDB()
	expectLocalObject(mock, objectBytes, validUntil, true)
	expectLocalTenant(mock, tenantBytes, validUntil, true)

	resolution, handled, err := p.ResolveLocalContent(context.Background(), "playbackkey")
	if err != nil || !handled {
		t.Fatalf("ResolveLocalContent = %+v, %v, %v", resolution, handled, err)
	}
	if resolution.RoutingInternalName() != "stream-internal" || resolution.StreamId == "" || resolution.TenantId == "" {
		t.Fatalf("resolution = %+v", resolution)
	}
	if p.commodoreClient != nil || p.tenantAdmission != nil {
		t.Fatal("fixture unexpectedly has a connected authority client")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestUnreadyTenantAuthorityFallsBackToConnectedPlaybackPolicy(t *testing.T) {
	validUntil := time.Now().Add(time.Hour)
	p, mock, closeDB, tenantBytes, objectBytes := localAuthorityFixture(t)
	defer closeDB()
	expectLocalObject(mock, objectBytes, validUntil, true)
	expectLocalTenant(mock, tenantBytes, validUntil, false)
	expectLocalObject(mock, objectBytes, validUntil, true)
	expectLocalTenant(mock, tenantBytes, validUntil, false)

	if resolution, handled, err := p.ResolveLocalContent(context.Background(), "playbackkey"); err != nil || handled || resolution != nil {
		t.Fatalf("unready tenant resolution = %+v, %v, %v; want connected fallback", resolution, handled, err)
	}
	decision, handled := p.EvaluateLocalPlaybackPolicy(context.Background(), "playbackkey", "stream-internal", &ipcpb.ViewerConnectTrigger{})
	if handled || decision != "" {
		t.Fatalf("unready tenant policy = %q, %v; want connected fallback", decision, handled)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestLocalAuthorityDriverErrorFallsBackInsteadOfDenyingViewer(t *testing.T) {
	p, mock, closeDB, _, _ := localAuthorityFixture(t)
	defer closeDB()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT authority.payload, authority.payload_sha256, authority.refresh_after, authority.valid_until,")).
		WillReturnError(sql.ErrConnDone)

	decision, handled := p.EvaluateLocalPlaybackPolicy(context.Background(), "playbackkey", "stream-internal", &ipcpb.ViewerConnectTrigger{})
	if handled || decision != "" {
		t.Fatalf("local driver error = %q, %v; want connected fallback", decision, handled)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPlayRewriteLocalDriverErrorUsesConnectedResolver(t *testing.T) {
	p, mock, closeDB, _, _ := localAuthorityFixture(t)
	defer closeDB()
	for range 2 {
		mock.ExpectQuery(regexp.QuoteMeta("SELECT authority.payload, authority.payload_sha256, authority.refresh_after, authority.valid_until,")).
			WillReturnError(sql.ErrConnDone)
	}

	connected, cleanup, stub := setupCommodoreClientWithStub(t, nil, nil)
	t.Cleanup(cleanup)
	p.SetCommodoreClient(connected)
	previous := control.CommodoreClient
	control.SetCommodoreClient(nil)
	t.Cleanup(func() { control.SetCommodoreClient(previous) })
	stub.resolveStreamContextByKey = map[string]*commodorepb.ResolveStreamContextResponse{
		"internal_name:stream-internal": {
			Admitted: true, StreamId: "stream-id", PlaybackId: "playback-id", InternalName: "stream-internal",
			TenantId: "tenant-id", IngestMode: "push", BillingModel: "postpaid",
		},
	}
	p.streamCache.Set("tenant-id:stream-internal", streamContext{
		TenantID: "tenant-id", StreamID: "stream-id", BillingModel: "postpaid",
	}, time.Minute)

	response, abort, err := p.handlePlayRewrite(&ipcpb.MistTrigger{
		NodeId: "edge-node-1",
		TriggerPayload: &ipcpb.MistTrigger_PlayRewrite{PlayRewrite: &ipcpb.ViewerResolveTrigger{
			RequestedStream: "stream-internal", ViewerHost: "192.0.2.10", OutputType: "HTTP",
		}},
	})
	if err != nil || abort || response != "live+stream-internal" {
		t.Fatalf("connected PLAY_REWRITE fallback = response:%q abort:%v err:%v", response, abort, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestUserNewLocalAuthorityDriverErrorUsesConnectedPublicMarker(t *testing.T) {
	p, mock, closeDB, _, _ := localAuthorityFixture(t)
	defer closeDB()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT authority.payload, authority.payload_sha256, authority.refresh_after, authority.valid_until,")).
		WillReturnError(sql.ErrConnDone)

	decision, err := p.enforcePlaybackPolicy(context.Background(), "stream-internal", streamContext{
		RequiresAuthKnown: true,
		RequiresAuth:      false,
	}, &ipcpb.ViewerConnectTrigger{})
	if err != nil || decision != "true" {
		t.Fatalf("connected public fallback = %q, %v; want allow", decision, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPlayRewriteReadyLocalAuthoritySkipsConnectedEnrichment(t *testing.T) {
	validUntil := time.Now().Add(time.Hour)
	p, mock, closeDB, tenantBytes, objectBytes := localAuthorityFixture(t)
	defer closeDB()
	expectLocalObject(mock, objectBytes, validUntil, true)
	expectLocalTenant(mock, tenantBytes, validUntil, true)
	expectLocalTenant(mock, tenantBytes, validUntil, true)

	connected, cleanup, stub := setupCommodoreClientWithStub(t, nil, nil)
	t.Cleanup(cleanup)
	p.SetCommodoreClient(connected)

	trigger := &ipcpb.MistTrigger{
		NodeId: "edge-node-1",
		TriggerPayload: &ipcpb.MistTrigger_PlayRewrite{PlayRewrite: &ipcpb.ViewerResolveTrigger{
			RequestedStream: "PlaybackKey", ViewerHost: "192.0.2.10", OutputType: "HTTP",
		}},
	}
	response, abort, err := p.handlePlayRewrite(trigger)
	if err != nil || abort || response != "live+stream-internal" {
		t.Fatalf("local PLAY_REWRITE = response:%q abort:%v err:%v", response, abort, err)
	}
	if trigger.GetTenantId() == "" || trigger.GetUserId() == "" || trigger.GetStreamId() == "" || trigger.GetOriginClusterId() != "cluster-a" {
		t.Fatalf("local PLAY_REWRITE context = %+v", trigger)
	}
	if calls := stub.ResolveStreamContextKeys(); len(calls) != 0 {
		t.Fatalf("ready local PLAY_REWRITE made connected enrichment calls: %v", calls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMarkedHardExpiredAuthorityDoesNotFallBack(t *testing.T) {
	validUntil := time.Now().Add(-time.Minute)
	p, mock, closeDB, _, objectBytes := localAuthorityFixture(t)
	defer closeDB()
	expectLocalObject(mock, objectBytes, validUntil, true)

	resolution, handled, err := p.ResolveLocalContent(context.Background(), "PlaybackKey")
	if err == nil || !handled || resolution != nil {
		t.Fatalf("ResolveLocalContent = %+v, %v, %v", resolution, handled, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestMarkedWebhookAuthorityWithoutCellSecretDeniesWithoutCentralFallback(t *testing.T) {
	validUntil := time.Now().Add(time.Hour)
	p, mock, closeDB, tenantBytes, objectBytes := localAuthorityFixture(t)
	defer closeDB()
	object := &mediaauthoritypb.MediaObjectAuthority{}
	if err := proto.Unmarshal(objectBytes, object); err != nil {
		t.Fatal(err)
	}
	object.PlaybackPolicy = &mediaauthoritypb.PlaybackPolicy{Kind: mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_WEBHOOK}
	objectBytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	expectLocalObject(mock, objectBytes, validUntil, true)
	expectLocalTenant(mock, tenantBytes, validUntil, true)

	decision, handled := p.EvaluateLocalPlaybackPolicy(context.Background(), "PlaybackKey", "stream-internal", &ipcpb.ViewerConnectTrigger{})
	if !handled || decision != "false" {
		t.Fatalf("EvaluateLocalPlaybackPolicy = %q, %v; want fail-closed handled denial", decision, handled)
	}
	if p.commodoreClient != nil {
		t.Fatal("fixture unexpectedly has a connected authority client")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestArtifactSourcePromotionRequiresExactSignedSourceIdentity(t *testing.T) {
	tenant := &mediaauthoritypb.TenantAuthority{
		TenantId: "tenant-1", Lifecycle: mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
		BillingDecision: mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW,
		EffectiveClusterGrants: []*mediaauthoritypb.TenantClusterGrant{{
			ClusterId: "cluster-a", AccessSource: clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_PLATFORM_TIER,
			AccessLevel: "full", SubscriptionStatus: "active", ClusterClass: "platform_official", AllowPrivatePullSources: true,
			ControlCellId: "cell-a", EligibleServingCellIds: []string{"cell-a"},
		}},
	}
	object := &mediaauthoritypb.MediaObjectAuthority{
		ObjectKind: mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_ARTIFACT, TenantId: "tenant-1", UserId: "user-1",
		InternalName: "artifact-internal", OriginClusterId: "cluster-a", Lifecycle: mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
		PlaybackPolicy: &mediaauthoritypb.PlaybackPolicy{Kind: mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_PUBLIC},
		Object: &mediaauthoritypb.MediaObjectAuthority_Artifact{Artifact: &mediaauthoritypb.ArtifactAuthority{
			ArtifactId: "artifact-1", ArtifactHash: "hash-1", ArtifactKind: mediaauthoritypb.ArtifactKind_ARTIFACT_KIND_CLIP, ParentStreamId: "stream-1",
		}},
	}
	connected := &commodorepb.ResolveArtifactInternalNameResponse{
		Found: true, ArtifactHash: "hash-1", InternalName: "artifact-internal", TenantId: "tenant-1", UserId: "user-1",
		StreamId: "stream-1", ContentType: "clip", OriginClusterId: "cluster-a", ClusterPeers: localClusterPeers(tenant),
		AuthorityClusterPeers: localClusterPeers(tenant), RequiresAuth: false,
	}
	snapshot := localauthority.SourceSnapshot{Object: localauthority.MediaObjectSnapshot{Authority: object}, Tenant: localauthority.TenantSnapshot{Authority: tenant}}
	if !sameLocalArtifactSource(snapshot, connected) {
		t.Fatal("equivalent artifact source did not match")
	}
	changed := proto.Clone(connected).(*commodorepb.ResolveArtifactInternalNameResponse)
	changed.ArtifactHash = "hash-forged"
	if sameLocalArtifactSource(snapshot, changed) {
		t.Fatal("artifact source promotion accepted a different content hash")
	}
	changed = proto.Clone(connected).(*commodorepb.ResolveArtifactInternalNameResponse)
	changed.AuthorityClusterPeers[0].AllowPrivatePullSources = false
	if sameLocalArtifactSource(snapshot, changed) {
		t.Fatal("artifact source promotion accepted a different cluster grant")
	}
}

func TestDVRStreamSourceUsesMarkedLocalAuthorityWithoutControlPlane(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	sm.SetNodeStoragePaths("edge-recording", "/srv/frameworks/storage", "", "")

	authorityDB, authorityMock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = authorityDB.Close() })
	seed := make([]byte, ed25519.SeedSize)
	privateKey := ed25519.NewKeyFromSeed(seed)
	store, err := localauthority.NewStore(authorityDB, "cell-a", sharedauthority.TrustSet{"key": privateKey.Public().(ed25519.PublicKey)})
	if err != nil {
		t.Fatal(err)
	}
	tenantID := "10000000-0000-0000-0000-000000000009"
	streamID := "30000000-0000-0000-0000-000000000009"
	tenant := &mediaauthoritypb.TenantAuthority{
		SchemaVersion: sharedauthority.SchemaVersion, TenantId: tenantID,
		Lifecycle:       mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
		BillingDecision: mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW,
		BillingModel:    mediaauthoritypb.TenantBillingModel_TENANT_BILLING_MODEL_POSTPAID,
		EffectiveClusterGrants: []*mediaauthoritypb.TenantClusterGrant{{
			ClusterId: "cluster-a", AccessSource: clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_PLATFORM_TIER,
			SubscriptionStatus: "active", ClusterClass: "platform_official", ControlCellId: "cell-a", EligibleServingCellIds: []string{"cell-a"},
		}},
	}
	object := &mediaauthoritypb.MediaObjectAuthority{
		SchemaVersion: sharedauthority.SchemaVersion, ObjectKind: mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_ARTIFACT,
		TenantId: tenantID, UserId: "20000000-0000-0000-0000-000000000009", InternalName: "dvr-internal-local",
		PlaybackId: "dvr-playback-local", Lifecycle: mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
		OriginClusterId: "cluster-a", PlaybackPolicy: &mediaauthoritypb.PlaybackPolicy{Kind: mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_PUBLIC},
		Object: &mediaauthoritypb.MediaObjectAuthority_Artifact{Artifact: &mediaauthoritypb.ArtifactAuthority{
			ArtifactId: "artifact-dvr-local", ArtifactHash: "dvr-hash-local", ArtifactKind: mediaauthoritypb.ArtifactKind_ARTIFACT_KIND_DVR,
			ParentStreamId: streamID,
		}},
	}
	tenantBytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(tenant)
	if err != nil {
		t.Fatal(err)
	}
	objectBytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	validUntil := time.Now().Add(time.Hour)
	authorityMock.ExpectQuery(regexp.QuoteMeta("SELECT authority.payload, authority.payload_sha256, authority.refresh_after, authority.valid_until,")).
		WithArgs("dvr-internal-local").
		WillReturnRows(sqlmock.NewRows([]string{"payload", "payload_sha256", "refresh_after", "valid_until", "authority_id", "authority_version", "local_source_ready"}).
			AddRow(objectBytes, localPayloadDigest(objectBytes), time.Now().Add(time.Minute), validUntil, sharedauthority.ArtifactAuthorityID("artifact-dvr-local"), int64(4), true))
	authorityMock.ExpectQuery(regexp.QuoteMeta("SELECT authority.payload, authority.payload_sha256, authority.refresh_after, authority.valid_until,")).
		WithArgs(tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"payload", "payload_sha256", "refresh_after", "valid_until", "authority_version", "local_source_ready"}).
			AddRow(tenantBytes, localPayloadDigest(tenantBytes), time.Now().Add(time.Minute), validUntil, int64(8), true))

	localDB, localMock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	previousDB, previousCommodore := control.GetDB(), control.CommodoreClient
	control.SetDB(localDB)
	control.CommodoreClient = nil
	t.Cleanup(func() {
		control.SetDB(previousDB)
		control.CommodoreClient = previousCommodore
		_ = localDB.Close()
	})
	localMock.ExpectQuery(regexp.QuoteMeta("SELECT COALESCE(artifact_type, '')::text AS artifact_type")).
		WithArgs("dvr-hash-local").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_type", "stream_id", "stream_internal_name"}).
			AddRow("dvr", streamID, "stream-internal-local"))
	localMock.ExpectQuery(regexp.QuoteMeta("SELECT status\nFROM foghorn.artifacts")).
		WithArgs("dvr-hash-local").
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("recording"))
	localMock.ExpectQuery(regexp.QuoteMeta("SELECT node_id, COALESCE(is_orphaned, false)::boolean AS is_orphaned")).
		WithArgs("dvr-hash-local").
		WillReturnRows(sqlmock.NewRows([]string{"node_id", "is_orphaned"}).AddRow("edge-recording", false))

	p := NewProcessor(testLogger(), nil, nil, nil, nil)
	p.SetMediaAuthorityStore(store)
	response, abort, err := p.handleStreamSource(&ipcpb.MistTrigger{
		NodeId:         "edge-recording",
		TriggerPayload: &ipcpb.MistTrigger_StreamSource{StreamSource: &ipcpb.StreamSourceTrigger{StreamName: "dvr+dvr-internal-local"}},
	})
	if err != nil || abort {
		t.Fatalf("local DVR STREAM_SOURCE = response:%q abort:%v err:%v", response, abort, err)
	}
	want := "/srv/frameworks/storage/dvr/" + streamID + "/dvr-hash-local/dvr-hash-local.m3u8"
	if response != want {
		t.Fatalf("local DVR STREAM_SOURCE response=%q want=%q", response, want)
	}
	if p.commodoreClient != nil || control.CommodoreClient != nil {
		t.Fatal("local DVR source unexpectedly installed a central client")
	}
	if err := authorityMock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if err := localMock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
