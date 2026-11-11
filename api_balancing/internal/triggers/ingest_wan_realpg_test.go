//go:build schema_verify

package triggers

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/federation"
	localauthority "frameworks/api_balancing/internal/mediaauthority"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	limitspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/tenant_limits"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/latencyproxy"
	"github.com/yugabyte/pgx/v5"
	"github.com/yugabyte/pgx/v5/stdlib"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

type wanAdmissionCommodore struct {
	commodorepb.UnimplementedInternalServiceServer
	response *commodorepb.ValidateStreamKeyResponse
	calls    atomic.Int32
	leases   atomic.Int32
	held     atomic.Pointer[string]
}

func (s *wanAdmissionCommodore) ValidateStreamKey(ctx context.Context, _ *commodorepb.ValidateStreamKeyRequest) (*commodorepb.ValidateStreamKeyResponse, error) {
	s.calls.Add(1)
	timer := time.NewTimer(110 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return proto.CloneOf(s.response), nil
	}
}

func (s *wanAdmissionCommodore) AcquirePublisherLease(ctx context.Context, req *commodorepb.PublisherLeaseRequest) (*commodorepb.PublisherLeaseResponse, error) {
	s.leases.Add(1)
	timer := time.NewTimer(110 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
	}
	if held := s.held.Load(); held != nil {
		return &commodorepb.PublisherLeaseResponse{Outcome: commodorepb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_HELD_BY_OTHER, HeldExecutionToken: *held}, nil
	}
	return &commodorepb.PublisherLeaseResponse{Outcome: commodorepb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_ACQUIRED}, nil
}

type countedWANConnector struct {
	driver.Connector
	queries    atomic.Int32
	mu         sync.Mutex
	names      []string
	ownerDelay atomic.Int64
}

func (c *countedWANConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &countedWANConn{Conn: conn, parent: c}, nil
}

type countedWANConn struct {
	driver.Conn
	parent *countedWANConnector
}

func (c *countedWANConn) Ping(ctx context.Context) error { return c.Conn.(driver.Pinger).Ping(ctx) }
func (c *countedWANConn) ResetSession(ctx context.Context) error {
	if resetter, ok := c.Conn.(driver.SessionResetter); ok {
		return resetter.ResetSession(ctx)
	}
	return nil
}
func (c *countedWANConn) IsValid() bool {
	if validator, ok := c.Conn.(driver.Validator); ok {
		return validator.IsValid()
	}
	return true
}
func (c *countedWANConn) CheckNamedValue(value *driver.NamedValue) error {
	if checker, ok := c.Conn.(driver.NamedValueChecker); ok {
		return checker.CheckNamedValue(value)
	}
	return driver.ErrSkip
}

func (c *countedWANConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.parent.queries.Add(1)
	c.parent.mu.Lock()
	c.parent.names = append(c.parent.names, strings.SplitN(query, "\n", 2)[0])
	c.parent.mu.Unlock()
	if delay := time.Duration(c.parent.ownerDelay.Load()); delay > 0 && strings.Contains(query, "IngestClaimOwner") {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func (s *wanAdmissionCommodore) SyncActiveIngestPlacement(_ context.Context, request *commodorepb.SyncActiveIngestPlacementRequest) (*commodorepb.SyncActiveIngestPlacementResponse, error) {
	for _, release := range request.GetRelease() {
		if held := s.held.Load(); held != nil && release.GetClaimToken() == *held {
			s.held.CompareAndSwap(held, nil)
		}
	}
	return &commodorepb.SyncActiveIngestPlacementResponse{ExecutionFencesApplied: true}, nil
}

func (s *wanAdmissionCommodore) ReportMediaAuthorityUse(context.Context, *commodorepb.ReportMediaAuthorityUseRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func TestPublisherAdmissionWAN_RealPG(t *testing.T) {
	conn, dsn := startFoghornTriggersRealPGWithDSN(t)
	testPublisherAdmissionWAN(t, conn, dsn)
}

func TestPublisherAdmissionWAN_RealYugabyte(t *testing.T) {
	conn, dsn, ok := dockerpg.OpenSharedYugabyteBaselineWithDSN(t, "ingest_wan", "foghorn")
	if !ok {
		t.Fatal("requires make verify-ingest-admission-wan")
	}
	testPublisherAdmissionWAN(t, conn, dsn)
}

func testPublisherAdmissionWAN(t *testing.T, conn *sql.DB, dsn string) {
	t.Helper()
	const tenantID = "10000000-0000-0000-0000-000000000001"
	const streamID = "30000000-0000-0000-0000-000000000001"
	const internalName, nodeID = "stream-internal", "edge-node-1"
	ctx := t.Context()
	_, _, closeFixture, tenantBytes, objectBytes := localAuthorityFixture(t)
	closeFixture()
	tenant, object := &mediapb.TenantAuthority{}, &mediapb.MediaObjectAuthority{}
	if err := proto.Unmarshal(tenantBytes, tenant); err != nil {
		t.Fatal(err)
	}
	if err := proto.Unmarshal(objectBytes, object); err != nil {
		t.Fatal(err)
	}
	previousEnforcement := localauthority.PlacementEnforced()
	localauthority.SetPlacementEnforced(true)
	t.Cleanup(func() { localauthority.SetPlacementEnforced(previousEnforcement) })
	tenant.SchemaVersion, object.SchemaVersion = sharedauthority.PublisherAdmissionSchemaVersion, sharedauthority.PublisherAdmissionSchemaVersion
	tenant.MediaPlacement = &placementpb.PolicySet{Revision: 1}
	object.MediaPlacement = &placementpb.PolicySet{}
	object.PlacementTenantRevision = 1
	tenant.ResourceLimits = &limitspb.TenantResourceLimits{MaxStreams: 2}
	otherCluster := proto.CloneOf(tenant.EffectiveClusterGrants[0])
	otherCluster.ClusterId = "cluster-b"
	otherCluster.ResourceLimits = &limitspb.TenantResourceLimits{MaxStreams: 1}
	tenant.EffectiveClusterGrants = append(tenant.EffectiveClusterGrants, otherCluster)
	for _, grant := range tenant.EffectiveClusterGrants {
		grant.ClusterType = "edge"
		grant.MediaConsent = &placementpb.CapacityConsent{AllowIngest: true, AllowServe: true, AllowExternalSource: true}
	}

	object.GetLiveStream().PublisherConfigurationVersion = 1
	object.GetLiveStream().RecordingEnabled = false
	object.GetLiveStream().ProcessesJson = ""
	privateKey := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	trust := sharedauthority.TrustSet{"key": privateKey.Public().(ed25519.PublicKey)}
	store, err := localauthority.NewStore(conn, "cell-a", trust)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct {
		kind mediapb.AuthorityKind
		id   string
		body proto.Message
	}{
		{mediapb.AuthorityKind_AUTHORITY_KIND_TENANT, tenantID, tenant},
		{mediapb.AuthorityKind_AUTHORITY_KIND_MEDIA_OBJECT, sharedauthority.LiveStreamAuthorityID(streamID), object},
	} {
		now := time.Now().UTC()
		envelope, buildErr := sharedauthority.NewEnvelope(entry.kind, entry.id, 1, now, now.Add(time.Minute), now.Add(time.Hour), "key", "cell-a", entry.body, []*mediapb.AuthoritySourceRevision{{Service: "commodore", Revision: "1"}})
		if buildErr != nil {
			t.Fatal(buildErr)
		}
		signed, signErr := sharedauthority.Sign(envelope, privateKey)
		if signErr != nil {
			t.Fatal(signErr)
		}
		encoded, marshalErr := proto.Marshal(signed)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if _, applyErr := store.Apply(ctx, encoded); applyErr != nil {
			t.Fatal(applyErr)
		}
	}
	if _, err = store.MarkIngestPairLocalReady(ctx, tenantID, 1, sharedauthority.LiveStreamAuthorityID(streamID), 1); err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := latencyproxy.Start(ctx, parsed.Host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proxy.Close() })
	parsed.Host = proxy.Address()
	pgxConfig, err := pgx.ParseConfig(parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	pgxConfig.DefaultQueryExecMode = pgx.QueryExecModeExec
	pgxConfig.RuntimeParams["timezone"] = "UTC"
	// Every physical dial, including a smart-driver-discovered endpoint, goes
	// through the proxy. This fixture has one database server behind it.
	pgxConfig.DialFunc = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, proxy.Address())
	}
	counted := &countedWANConnector{Connector: stdlib.GetConnector(*pgxConfig)}
	wanDB := sql.OpenDB(counted)
	t.Cleanup(func() { _ = wanDB.Close() })
	wanDB.SetMaxOpenConns(1)
	wanDB.SetMaxIdleConns(1)
	if err = wanDB.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	proxy.SetRoundTrip(110 * time.Millisecond)
	started := time.Now()
	if err = wanDB.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed < 110*time.Millisecond {
		t.Fatalf("fixture did not inject the requested RTT: %s", elapsed)
	}
	wanStore, err := localauthority.NewStore(wanDB, "cell-a", trust)
	if err != nil {
		t.Fatal(err)
	}
	previousDB, previousRegistry := control.GetDB(), control.StreamRegistryInstance
	control.SetDB(wanDB)
	control.SetStreamRegistry(control.NewStreamRegistry(nil, "cluster-a", time.Minute))
	t.Cleanup(func() { control.SetDB(previousDB); control.SetStreamRegistry(previousRegistry) })
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	sm.SetNodeInfo(nodeID, "http://edge.example/view", true, nil, nil, "", "", nil)
	sm.SetNodeConnectionInfo(ctx, nodeID, "127.0.0.1:18090", "", "cluster-b", nil)
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	central := &wanAdmissionCommodore{response: &commodorepb.ValidateStreamKeyResponse{
		Valid: true, TenantId: tenantID, UserId: object.GetUserId(), StreamId: streamID,
		InternalName: internalName, PlaybackId: object.GetPlaybackId(), BillingModel: "postpaid",
		OriginClusterId: proto.String("cluster-a"), OfficialClusterId: proto.String("cluster-a"),
		TenantResourceLimits: tenant.ResourceLimits,
	}}
	commodorepb.RegisterInternalServiceServer(server, central)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	client, err := commodore.NewGRPCClient(commodore.GRPCConfig{GRPCAddr: listener.Addr().String(), AllowInsecure: true, Logger: logging.NewLogger(), ServiceToken: "test-service"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	p := NewProcessor(logging.NewLogger(), client, nil, nil, nil)
	p.clusterID = "cluster-a"
	p.SetMediaAuthorityStore(wanStore)
	p.SetPublisherAdmissionRepository(control.NewPublisherAdmissionRepository(wanDB))
	admitIngestPlacementForTest(t, p)
	admit := p.ingestPlacementAdmission
	p.SetIngestPlacementAdmission(func(ctx context.Context, connection IngestPlacementConnection) (federation.PlacementAdmissionDecision, error) {
		decision, err := admit(ctx, connection)
		decision.ObjectID = sharedauthority.LiveStreamAuthorityID(streamID)
		return decision, err
	})
	trigger := &ipcpb.MistTrigger{NodeId: nodeID, TriggerType: "PUSH_REWRITE", TriggerPayload: &ipcpb.MistTrigger_PushRewrite{
		PushRewrite: &ipcpb.PushRewriteTrigger{Pid: 4242, TriggerUuid: "wan-publisher", TriggerUnixMillis: time.Now().UnixMilli(), StreamName: "sk_local", PushUrl: "rtmp://edge/live/sk_local", ObservedConnector: "RTMP"},
	}}
	retry := proto.CloneOf(trigger)
	started = time.Now()
	name, _, admissionErr := p.handlePushRewrite(trigger)
	elapsed := time.Since(started)
	t.Logf("WAN admission: RTT=110ms elapsed=%s central_validations=%d leases=%d SQL=%d names=%v", elapsed, central.calls.Load(), central.leases.Load(), counted.queries.Load(), counted.names)
	if central.calls.Load() != 0 || central.leases.Load() != 1 || counted.queries.Load() > 3 {
		t.Fatalf("warm admission must use at most three SQL operations and one lease, without central validation: validations=%d leases=%d SQL=%d names=%v", central.calls.Load(), central.leases.Load(), counted.queries.Load(), counted.names)
	}
	if admissionErr != nil || name != "live+"+internalName {
		t.Fatalf("ready signed authority must admit the first healthy publisher at 110ms DB RTT: name=%q elapsed=%s error=%v", name, elapsed, admissionErr)
	}
	if trigger.GetOriginClusterId() != "cluster-b" {
		t.Fatalf("publisher was attributed to signed historical origin %q instead of the cluster holding its lease", trigger.GetOriginClusterId())
	}
	var persistedCap int
	if err := conn.QueryRowContext(ctx, `SELECT capacity_max_streams FROM foghorn.ingest_sessions WHERE tenant_id=$1::uuid AND id=$2::uuid`, tenantID, trigger.GetEventId()).Scan(&persistedCap); err != nil || persistedCap != 1 {
		t.Fatalf("admission used outage-owner limits for another cluster: cap=%d error=%v", persistedCap, err)
	}
	if elapsed >= 4*time.Second {
		t.Fatalf("publisher accepted after the edge stopped waiting: %s", elapsed)
	}
	publisherTemplate := proto.CloneOf(retry)
	firstGeneration := trigger.GetEventId()
	if retryName, _, retryErr := p.handlePushRewrite(retry); retryErr != nil || retryName != name || retry.GetEventId() != firstGeneration {
		t.Fatalf("warm same-execution retry changed admission: name=%s generation=%s error=%v", retryName, retry.GetEventId(), retryErr)
	}
	if central.calls.Load() != 0 || central.leases.Load() != 2 {
		t.Fatalf("warm retry revalidated configuration: validations=%d leases=%d", central.calls.Load(), central.leases.Load())
	}
	for _, rtt := range []time.Duration{110 * time.Millisecond, 150 * time.Millisecond} {
		proxy.SetRoundTrip(rtt)
		latencies := make([]time.Duration, 0, 10)
		for sample := 0; sample < 10; sample++ {
			next := proto.CloneOf(publisherTemplate)
			next.GetPushRewrite().TriggerUuid = fmt.Sprintf("wan-%d-%d", rtt.Milliseconds(), sample)
			next.GetPushRewrite().TriggerUnixMillis = time.Now().UnixMilli() + int64(sample+1)
			queriesBefore := counted.queries.Load()
			at := time.Now()
			name, _, err := p.handlePushRewrite(next)
			latency := time.Since(at)
			if err != nil || name != "live+"+internalName || latency >= 4*time.Second {
				t.Fatalf("RTT=%s sample=%d name=%s latency=%s error=%v", rtt, sample, name, latency, err)
			}
			if exchanges := counted.queries.Load() - queriesBefore; exchanges > 3 {
				t.Fatalf("normal admission used %d SQL calls: %v", exchanges, counted.names)
			}
			latencies = append(latencies, latency)
		}
		slices.Sort(latencies)
		t.Logf("WAN distribution: driver=yugabyte/pgx exec RTT=%s samples=%d p50=%s p95=%s p99=%s", rtt, len(latencies), latencies[4], latencies[9], latencies[9])
	}
	var active int
	if err = conn.QueryRowContext(ctx, `SELECT count(*) FROM foghorn.ingest_sessions WHERE tenant_id=$1::uuid AND stream_internal_name=$2 AND ended_at IS NULL AND projection_state='active'`, tenantID, internalName).Scan(&active); err != nil || active != 1 {
		t.Fatalf("active publisher count=%d error=%v", active, err)
	}
	t.Run("local recovery outlives a remote attempt", func(t *testing.T) {
		var generation, held string
		if err := conn.QueryRowContext(ctx, `SELECT id::text,start_trigger_uuid FROM foghorn.ingest_sessions WHERE tenant_id=$1::uuid AND stream_internal_name=$2 AND ended_at IS NULL`, tenantID, internalName).Scan(&generation, &held); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.ExecContext(ctx, `UPDATE foghorn.ingest_sessions SET ended_at=NOW(),ended_reason='node_lost' WHERE tenant_id=$1::uuid AND id=$2::uuid`, tenantID, generation); err != nil {
			t.Fatal(err)
		}
		central.held.Store(&held)
		counted.ownerDelay.Store(int64(1100 * time.Millisecond))
		defer counted.ownerDelay.Store(0)
		next := proto.CloneOf(publisherTemplate)
		next.GetPushRewrite().TriggerUuid = "after-ended-owner"
		next.GetPushRewrite().TriggerUnixMillis = time.Now().UnixMilli()
		started := time.Now()
		name, _, err := p.handlePushRewrite(next)
		if err != nil || name != "live+"+internalName || central.held.Load() != nil {
			t.Fatalf("local recovery inherited a remote attempt deadline: name=%s error=%v held=%v", name, err, central.held.Load())
		}
		if elapsed := time.Since(started); elapsed >= 4*time.Second {
			t.Fatalf("recovery exceeded publisher lifetime: %s", elapsed)
		}
		if p.CommodoreOutageBackoff().InOutage() {
			t.Fatal("healthy central service entered outage during local recovery")
		}
	})

	// A configuration marker cannot turn an authoritative signed denial into
	// a connected fallback, even while the connected stub still says allow.
	tenant.BillingDecision = mediapb.TenantBillingDecision_TENANT_BILLING_DECISION_SUSPENDED
	tenant.EffectiveClusterGrants = nil
	tenant.AllowPlatformSharedPlayback = false
	object.GetLiveStream().PublisherConfigurationVersion = 0
	for _, entry := range []struct {
		kind mediapb.AuthorityKind
		id   string
		body proto.Message
	}{
		{mediapb.AuthorityKind_AUTHORITY_KIND_TENANT, tenantID, tenant},
		{mediapb.AuthorityKind_AUTHORITY_KIND_MEDIA_OBJECT, sharedauthority.LiveStreamAuthorityID(streamID), object},
	} {
		now := time.Now().UTC()
		envelope, buildErr := sharedauthority.NewEnvelope(entry.kind, entry.id, 2, now, now.Add(time.Minute), now.Add(time.Hour), "key", "cell-a", entry.body, []*mediapb.AuthoritySourceRevision{{Service: "commodore", Revision: "2"}})
		if buildErr != nil {
			t.Fatal(buildErr)
		}
		signed, signErr := sharedauthority.Sign(envelope, privateKey)
		if signErr != nil {
			t.Fatal(signErr)
		}
		encoded, marshalErr := proto.Marshal(signed)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if _, applyErr := store.Apply(ctx, encoded); applyErr != nil {
			t.Fatal(applyErr)
		}
	}
	denied := proto.CloneOf(publisherTemplate)
	denied.GetPushRewrite().TriggerUuid = "denied-old-configuration"
	denied.GetPushRewrite().TriggerUnixMillis = time.Now().UnixMilli() + 1
	beforeCalls := central.calls.Load()
	beforeLeases := central.leases.Load()
	if _, _, err := p.handlePushRewrite(denied); err == nil {
		t.Fatal("incomplete configuration hid signed tenant denial")
	}
	if central.calls.Load() != beforeCalls || central.leases.Load() != beforeLeases {
		t.Fatal("signed tenant denial fell through to central admission")
	}
}
