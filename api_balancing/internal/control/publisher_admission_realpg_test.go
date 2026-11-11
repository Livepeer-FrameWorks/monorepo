//go:build schema_verify

package control

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"google.golang.org/protobuf/proto"
)

func TestPublisherAdmissionOperations_RealPG(t *testing.T) {
	conn, dsn := startRealPGWithDSN(t)
	testPublisherAdmissionOperations(t, conn, dsn)
}
func TestPublisherAdmissionOperations_RealYugabyte(t *testing.T) {
	conn, dsn, ok := dockerpg.OpenSharedYugabyteBaselineWithDSN(t, "publisher_operations", "foghorn")
	if !ok {
		t.Fatal("requires shared Yugabyte fixture")
	}
	testPublisherAdmissionOperations(t, conn, dsn)
}

func testPublisherAdmissionOperations(t *testing.T, conn *sql.DB, dsn string) {
	t.Helper()
	previous := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(previous) })
	t.Run("push-target confirmation and replay", func(t *testing.T) { testPublisherPushTargetConfirmation(t, conn) })
	repository := NewPublisherAdmissionRepository(conn)
	logger := logging.NewLogger()
	ctx := t.Context()
	request := func() IngestSessionRequest {
		return IngestSessionRequest{TenantID: uuid.NewString(), StreamID: uuid.NewString(), NodeID: uuid.NewString(), InternalName: uuid.NewString(), ConnectorPID: 100, TriggerUUID: uuid.NewString(), StartedAtMillis: time.Now().UnixMilli(), IngestClusterID: "cell-a", Authority: &IngestAuthoritySnapshot{CapacityMaxStreams: 1}}
	}
	prepare := func(t *testing.T, req IngestSessionRequest) PreparedPublisherAdmission {
		t.Helper()
		got, err := repository.Prepare(ctx, req, logger)
		if err != nil || got.Outcome != IngestSessionActive {
			t.Fatalf("prepare: %+v %v", got, err)
		}
		return got
	}
	t.Run("release recovery advances only confirmed execution obligations", func(t *testing.T) {
		oldCell := localControlCellID
		localControlCellID = "cell-a"
		t.Cleanup(func() { localControlCellID = oldCell })
		for _, test := range []struct {
			name        string
			ack         bool
			refused     bool
			rpcError    bool
			wantSettled bool
		}{{"fenced refusal", true, true, false, true}, {"missing server fence acknowledgement", false, false, false, false}, {"unknown transport result", false, false, true, false}} {
			t.Run(test.name, func(t *testing.T) {
				one, two := request(), request()
				first, second := prepare(t, one), prepare(t, two)
				for _, item := range []struct{ tenant, generation string }{{one.TenantID, first.Generation}, {two.TenantID, second.Generation}} {
					if _, err := conn.ExecContext(ctx, `UPDATE foghorn.ingest_sessions SET ended_at=NOW(),ended_reason='node_lost' WHERE tenant_id=$1::uuid AND id=$2::uuid`, item.tenant, item.generation); err != nil {
						t.Fatal(err)
					}
				}
				client := &publisherReleaseBatchClient{fenced: test.ack}
				if test.refused {
					client.refused = one.TriggerUUID
				}
				if test.rpcError {
					client.rpcErr = errors.New("unknown release outcome")
				}
				releaseErr := releasePublisherLeases(ctx, client, foghorndb.New(conn))
				if releaseErr == nil {
					t.Fatal("release mismatch or uncertainty was not reported")
				}
				for _, item := range []struct{ tenant, generation string }{{one.TenantID, first.Generation}, {two.TenantID, second.Generation}} {
					var settled bool
					if err := conn.QueryRowContext(ctx, `SELECT publisher_lease_released_at IS NOT NULL FROM foghorn.ingest_sessions WHERE tenant_id=$1::uuid AND id=$2::uuid`, item.tenant, item.generation).Scan(&settled); err != nil {
						t.Fatal(err)
					}
					if settled != test.wantSettled {
						t.Fatalf("release obligation settled=%v, want %v (one refused execution must not block the batch)", settled, test.wantSettled)
					}
				}
			})
		}
	})

	t.Run("malformed public stream identity is refused", func(t *testing.T) {
		req := request()
		req.StreamID = "malformed"
		if result, err := repository.Prepare(ctx, req, logger); err == nil {
			t.Fatalf("malformed stream identity admitted: %+v", result)
		}
	})

	t.Run("idempotent prepare and confirm", func(t *testing.T) {
		req := request()
		first := prepare(t, req)
		second := prepare(t, req)
		if first.Generation != second.Generation || first.SourceRevision != second.SourceRevision {
			t.Fatalf("execution minted twice: %+v %+v", first, second)
		}
		registry := NewStreamRegistry(nil, "cell-a", time.Minute)
		if applied, _, err := repository.Project(ctx, registry, req, first, AdmissionEffectIntent{BroadcastLive: true}); err != nil || !applied {
			t.Fatalf("confirm: %v %v", applied, err)
		}
		resumed := prepare(t, req)
		if !resumed.Resumed {
			t.Fatal("committed accept must be retrievable on retry")
		}
		var events, effects int
		if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM foghorn.domain_event_outbox WHERE tenant_id=$1::uuid AND aggregate_id=$2`, req.TenantID, req.StreamID).Scan(&events); err != nil {
			t.Fatal(err)
		}
		if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM foghorn.ingest_admission_effects WHERE tenant_id=$1::uuid AND source_generation=$2::uuid`, req.TenantID, first.Generation).Scan(&effects); err != nil {
			t.Fatal(err)
		}
		if events != 1 || effects != 1 {
			t.Fatalf("duplicate events/effects: %d/%d", events, effects)
		}
	})
	t.Run("lost confirmation acknowledgement is recoverable", func(t *testing.T) {
		connector, err := pq.NewConnector(dsn)
		if err != nil {
			t.Fatal(err)
		}
		faulty := &publisherLostAckConnector{Connector: connector}
		faulty.armed.Store(true)
		uncertainDB := sql.OpenDB(faulty)
		t.Cleanup(func() { _ = uncertainDB.Close() })
		uncertainRepository := NewPublisherAdmissionRepository(uncertainDB)
		req := request()
		prepared := prepare(t, req)
		registry := NewStreamRegistry(nil, "cell-a", time.Minute)
		if applied, _, err := uncertainRepository.Project(ctx, registry, req, prepared, AdmissionEffectIntent{BroadcastLive: true}); err == nil || applied {
			t.Fatal("lost acknowledgement was reported as confirmed")
		}
		status, err := foghorndb.New(conn).GetPublisherAdmission(ctx, foghorndb.GetPublisherAdmissionParams{TenantID: req.TenantID, NodeID: req.NodeID, TriggerUuid: req.TriggerUUID})
		if err != nil || status.ProjectionState != "active" || status.EndedAt.Valid {
			t.Fatalf("unknown result destroyed committed admission: %+v %v", status, err)
		}
		resumed := prepare(t, req)
		if !resumed.Resumed || resumed.Generation != prepared.Generation {
			t.Fatal("retry did not find committed generation")
		}
		if applied, resumed, err := repository.Project(ctx, registry, req, resumed, AdmissionEffectIntent{}); err != nil || !applied || !resumed {
			t.Fatalf("retry did not recover accepted execution: %v %v %v", applied, resumed, err)
		}
	})

	t.Run("resumed generation is fenced after prepare", func(t *testing.T) {
		req := request()
		prepared := prepare(t, req)
		registry := NewStreamRegistry(nil, "cell-a", time.Minute)
		if applied, _, err := repository.Project(ctx, registry, req, prepared, AdmissionEffectIntent{}); err != nil || !applied {
			t.Fatal(err)
		}
		resumed := prepare(t, req)
		if _, err := AbandonIngestAdmission(ctx, req.NodeID, req.TriggerUUID, req.ConnectorPID, logger); err != nil {
			t.Fatal(err)
		}
		if applied, _, err := repository.Project(ctx, registry, req, resumed, AdmissionEffectIntent{}); applied || err == nil {
			t.Fatalf("resumed publisher survived abandonment between prepare and confirm: applied=%v err=%v", applied, err)
		}
	})

	t.Run("expired placement decision cannot confirm", func(t *testing.T) {
		req := request()
		req.DecisionExpiresAt = time.Now().Add(time.Hour)
		prepared := prepare(t, req)
		req.DecisionExpiresAt = time.Now().Add(-time.Second)
		if applied, _, err := repository.Project(ctx, NewStreamRegistry(nil, "cell-a", time.Minute), req, prepared, AdmissionEffectIntent{}); applied || err == nil {
			t.Fatalf("expired decision confirmed publisher: applied=%v err=%v", applied, err)
		}
	})

	t.Run("expired decision cannot pass divergent source repair", func(t *testing.T) {
		req := request()
		prepared := prepare(t, req)
		registry := NewStreamRegistry(nil, "cell-a", time.Minute)
		if applied, _, err := repository.Project(ctx, registry, req, prepared, AdmissionEffectIntent{}); err != nil || !applied {
			t.Fatal(err)
		}
		resumed := prepare(t, req)
		if _, applied, err := registry.ProjectSource(req.InternalName, "divergent", 999, "other", uuid.NewString(), prepared.SourceRevision+1); err != nil || !applied {
			t.Fatalf("diverge: %v %v", applied, err)
		}
		req.DecisionExpiresAt = time.Now().Add(-time.Second)
		if applied, _, err := repository.Project(ctx, registry, req, resumed, AdmissionEffectIntent{}); applied || err == nil {
			t.Fatalf("expired decision survived source repair: %v %v", applied, err)
		}
	})

	t.Run("cancelled request cannot publish its source", func(t *testing.T) {
		req := request()
		prepared := prepare(t, req)
		store, _, _ := newTestRedis(t)
		registry := NewStreamRegistry(nil, "cluster-test", time.Minute)
		registry.redisStore = store
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		if applied, _, err := repository.Project(cancelled, registry, req, prepared, AdmissionEffectIntent{}); applied || err == nil {
			t.Fatalf("cancelled admission: %v %v", applied, err)
		}
		revision, err := store.SourceRevision(ctx, req.InternalName)
		if err != nil || revision != 0 {
			t.Fatalf("cancelled admission published a shared source: revision=%d err=%v", revision, err)
		}
	})

	t.Run("wrong PID cannot retire an active generation", func(t *testing.T) {
		req := request()
		prepared := prepare(t, req)
		if applied, _, err := repository.Project(ctx, NewStreamRegistry(nil, "cell-a", time.Minute), req, prepared, AdmissionEffectIntent{}); err != nil || !applied {
			t.Fatal(err)
		}
		if ended, err := AbandonIngestAdmission(ctx, req.NodeID, req.TriggerUUID, req.ConnectorPID+1, logger); err == nil || ended != 0 {
			t.Fatalf("wrong PID settled accepted execution: %d %v", ended, err)
		}
		state, err := foghorndb.New(conn).GetPublisherAdmission(ctx, foghorndb.GetPublisherAdmissionParams{TenantID: req.TenantID, NodeID: req.NodeID, TriggerUuid: req.TriggerUUID})
		if err != nil || state.EndedAt.Valid || state.Generation != prepared.Generation {
			t.Fatalf("wrong PID changed generation: %+v %v", state, err)
		}
	})

	t.Run("last capacity slot serializes different streams", func(t *testing.T) {
		one, two := request(), request()
		two.TenantID = one.TenantID
		start := make(chan struct{})
		results := make(chan PreparedPublisherAdmission, 2)
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		for _, req := range []IngestSessionRequest{one, two} {
			wg.Go(func() { <-start; got, err := repository.Prepare(ctx, req, logger); results <- got; errs <- err })
		}
		close(start)
		wg.Wait()
		close(results)
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		active, capped := 0, 0
		for got := range results {
			if got.Outcome == IngestSessionActive {
				active++
			}
			if got.Outcome == IngestSessionRejectedCapacity {
				capped++
			}
		}
		if active != 1 || capped != 1 {
			t.Fatalf("capacity over-admitted after lock wait: active=%d capped=%d", active, capped)
		}
	})
	t.Run("abandon between prepare and confirm fences late work", func(t *testing.T) {
		req := request()
		prepared := prepare(t, req)
		if _, err := AbandonIngestAdmission(ctx, req.NodeID, req.TriggerUUID, req.ConnectorPID, logger); err != nil {
			t.Fatal(err)
		}
		applied, _, err := repository.Project(ctx, NewStreamRegistry(nil, "cell-a", time.Minute), req, prepared, AdmissionEffectIntent{})
		if applied || err == nil {
			t.Fatal("abandoned generation was confirmed")
		}
		if got, err := repository.Prepare(ctx, req, logger); err != nil || got.Outcome != IngestSessionAlreadyEnded {
			t.Fatalf("abandoned execution resumed: %+v %v", got, err)
		}
	})
	t.Run("lapsed scan cannot retire a subsequently confirmed publisher", func(t *testing.T) {
		req := request()
		prepared := prepare(t, req)
		if _, err := conn.ExecContext(ctx, `UPDATE foghorn.ingest_sessions SET started_at=NOW()-INTERVAL '1 minute' WHERE tenant_id=$1::uuid AND id=$2::uuid`, req.TenantID, prepared.Generation); err != nil {
			t.Fatal(err)
		}
		if _, err := foghorndb.New(conn).ListPublisherAdmissionsToSettle(ctx, IngestAdmissionWindow.Milliseconds()); err != nil {
			t.Fatal(err)
		}
		if applied, _, err := repository.Project(ctx, NewStreamRegistry(nil, "cell-a", time.Minute), req, prepared, AdmissionEffectIntent{}); err != nil || !applied {
			t.Fatalf("confirm: %v", err)
		}
		if retired, err := repository.Settle(ctx, req, prepared.Generation, IngestEndedAdmissionLapsed); err != nil || retired {
			t.Fatalf("atomic settlement retired a confirmed publisher: %v %v", retired, err)
		}
		if retired, err := RetireIngestSession(ctx, prepared.Generation, req.TenantID, req.InternalName, IngestEndedAdmissionLapsed, logger); err != nil || retired {
			t.Fatalf("lapsed scan retired a publisher that confirmed before settlement: retired=%v error=%v", retired, err)
		}
	})
	t.Run("durable abandonment forwards without a generation and retries lost acknowledgement", func(t *testing.T) {
		req := request()
		if _, err := AbandonIngestAdmission(ctx, req.NodeID, req.TriggerUUID, req.ConnectorPID, logger); err != nil {
			t.Fatal(err)
		}
		oldCell := localControlCellID
		localControlCellID = "cell-recovery"
		t.Cleanup(func() { localControlCellID = oldCell })
		client := &publisherRecoveryClient{failExecution: req.TriggerUUID}
		if err := RecoverPublisherAdmissions(ctx, client); err == nil {
			t.Fatal("lost global acknowledgement was not retained")
		}
		var settled bool
		read := func() {
			t.Helper()
			if err := conn.QueryRowContext(ctx, `SELECT publisher_lease_settled_at IS NOT NULL FROM foghorn.ingest_admission_abandonments WHERE node_id=$1 AND start_trigger_uuid=$2`, req.NodeID, req.TriggerUUID).Scan(&settled); err != nil {
				t.Fatal(err)
			}
		}
		read()
		if settled {
			t.Fatal("unacknowledged abandonment was marked delivered")
		}
		client.failExecution = ""
		if err := RecoverPublisherAdmissions(ctx, client); err != nil {
			t.Fatal(err)
		}
		read()
		if !settled {
			t.Fatal("global acknowledgement was not durable")
		}
		client.mu.Lock()
		defer client.mu.Unlock()
		if client.attempts[req.TriggerUUID] != 2 || client.cell != "cell-recovery" || client.nodes[req.TriggerUUID] != req.NodeID {
			t.Fatalf("lost execution identity or retry: %+v", client)
		}
	})

	t.Run("exceptional recovery terminally releases the actual ended v1 execution", func(t *testing.T) {
		for _, adapter := range []string{"published", "executed"} {
			for _, version := range []int{0, 1} {
				t.Run(fmt.Sprintf("%s/version%d", adapter, version), func(t *testing.T) {
					req := request()
					prepared := prepare(t, req)
					if _, err := conn.ExecContext(ctx, `UPDATE foghorn.ingest_sessions SET ended_at=NOW(),ended_reason='node_lost',publisher_admission_version=$3 WHERE tenant_id=$1::uuid AND id=$2::uuid`, req.TenantID, prepared.Generation, version); err != nil {
						t.Fatal(err)
					}
					client := &endedExecutionClaimClient{oldToken: req.TriggerUUID, oldNode: req.NodeID, cellID: "cell-a", held: req.TriggerUUID, renewLate: version == 1, identity: &pb.ValidateStreamKeyResponse{Valid: true, TenantId: req.TenantID, InternalName: req.InternalName, StreamId: req.StreamID}}
					newNode, newToken := uuid.NewString(), uuid.NewString()
					var response *pb.ValidateStreamKeyResponse
					var err error
					if adapter == "published" {
						response, err = ClaimPublishedIngestPlacement(ctx, client, &pb.PublisherLeaseRequest{TenantId: req.TenantID, StreamId: req.StreamID, InternalName: req.InternalName, ClusterId: req.IngestClusterID, ControlCellId: client.cellID, NodeId: newNode, ExecutionToken: newToken}, client.identity, newNode, logger)
					} else {
						response, err = ClaimExecutedIngestPlacement(ctx, client, "credential", req.TenantID, req.InternalName, newNode, req.IngestClusterID, newToken, client.cellID, logger)
					}
					if len(client.released) != 1 {
						t.Fatalf("ended-owner release missing: %+v", client.released)
					}
					entry := client.released[0]
					if version == 1 && (entry.GetNodeId() != req.NodeID || entry.GetControlCellId() != client.cellID) {
						t.Fatalf("v1 release lost actual ended owner: node=%q cell=%q, want node=%q cell=%q", entry.GetNodeId(), entry.GetControlCellId(), req.NodeID, client.cellID)
					}
					if version == 0 && (entry.GetNodeId() != "" || entry.GetControlCellId() != "") {
						t.Fatalf("v0 compatibility release changed: %v", entry)
					}
					if err != nil || !response.GetValid() || client.held != newToken || client.calls != 2 {
						t.Fatalf("late outage renewal stole immediate retry: response=%v error=%v held=%q calls=%d", response, err, client.held, client.calls)
					}
				})
			}
		}
	})

}

type publisherLostAckConnector struct {
	driver.Connector
	armed atomic.Bool
}

func (c *publisherLostAckConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &publisherLostAckConn{Conn: conn, parent: c}, nil
}

type publisherLostAckConn struct {
	driver.Conn
	parent *publisherLostAckConnector
}

func (c *publisherLostAckConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	rows, err := c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
	if err == nil && strings.Contains(query, "ConfirmPublisherAdmission") && c.parent.armed.CompareAndSwap(true, false) {
		return &publisherLostAckRows{Rows: rows}, nil
	}
	return rows, err
}

type publisherLostAckRows struct{ driver.Rows }

func (r *publisherLostAckRows) Close() error {
	if err := r.Rows.Close(); err != nil {
		return err
	}
	return errors.New("lost publisher confirmation acknowledgement")
}

type publisherRecoveryClient struct {
	mu                  sync.Mutex
	failExecution, cell string
	attempts            map[string]int
	nodes               map[string]string
}

func (c *publisherRecoveryClient) AbandonPublisherLease(_ context.Context, cell, node, execution string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.attempts == nil {
		c.attempts = map[string]int{}
		c.nodes = map[string]string{}
	}
	c.attempts[execution]++
	c.nodes[execution] = node
	c.cell = cell
	if execution == c.failExecution {
		return errors.New("lost global abandonment acknowledgement")
	}
	return nil
}
func (c *publisherRecoveryClient) SyncActiveIngestPlacement(context.Context, string, []*pb.ActiveIngestStream, []*pb.ActiveIngestStream) (*pb.SyncActiveIngestPlacementResponse, error) {
	return &pb.SyncActiveIngestPlacementResponse{ExecutionFencesApplied: true}, nil
}

// This transport injects an already-enumerated outage renewal immediately
// after the exceptional release. The real-engine Commodore suite proves the
// terminal marker semantics represented here; this suite proves both adapters
// select the ended node from the cell database rather than the new publisher.
type endedExecutionClaimClient struct {
	oldToken, oldNode, cellID, held string
	renewLate                       bool
	terminal                        bool
	identity                        *pb.ValidateStreamKeyResponse
	released                        []*pb.ActiveIngestStream
	calls                           int
}

func (c *endedExecutionClaimClient) AcquirePublisherLease(_ context.Context, request *pb.PublisherLeaseRequest) (*pb.PublisherLeaseResponse, error) {
	c.calls++
	if c.held != "" && c.held != request.GetExecutionToken() {
		return &pb.PublisherLeaseResponse{Outcome: pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_HELD_BY_OTHER, HeldExecutionToken: c.held}, nil
	}
	c.held = request.GetExecutionToken()
	return &pb.PublisherLeaseResponse{Outcome: pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_ACQUIRED}, nil
}
func (c *endedExecutionClaimClient) ValidateStreamKeyForClaim(ctx context.Context, key, cluster, token string) (*pb.ValidateStreamKeyResponse, error) {
	return c.ValidateStreamKeyForPublisherClaim(ctx, key, cluster, token, "", "")
}
func (c *endedExecutionClaimClient) ValidateStreamKeyForPublisherClaim(ctx context.Context, _ string, cluster, token, node, cell string) (*pb.ValidateStreamKeyResponse, error) {
	lease, err := c.AcquirePublisherLease(ctx, &pb.PublisherLeaseRequest{ExecutionToken: token, ClusterId: cluster, NodeId: node, ControlCellId: cell})
	response := proto.CloneOf(c.identity)
	if lease.GetOutcome() == pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_HELD_BY_OTHER {
		response.Valid = false
		response.RejectionReason = pb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_DUPLICATE_INGEST
		response.HeldClaimToken = lease.GetHeldExecutionToken()
	}
	return response, err
}
func (c *endedExecutionClaimClient) SyncActiveIngestPlacement(_ context.Context, _ string, _ []*pb.ActiveIngestStream, releases []*pb.ActiveIngestStream) (*pb.SyncActiveIngestPlacementResponse, error) {
	for _, entry := range releases {
		c.released = append(c.released, proto.CloneOf(entry))
		if entry.GetNodeId() == c.oldNode && entry.GetControlCellId() == c.cellID {
			c.terminal = true
		}
		if c.held == entry.GetClaimToken() && (entry.GetNodeId() == "" || (entry.GetNodeId() == c.oldNode && entry.GetControlCellId() == c.cellID)) {
			c.held = ""
		}
	}
	if c.renewLate && !c.terminal && c.held == "" {
		c.held = c.oldToken
	}
	return &pb.SyncActiveIngestPlacementResponse{ExecutionFencesApplied: true}, nil
}

type publisherReleaseBatchClient struct {
	fenced  bool
	refused string
	rpcErr  error
}

func (c *publisherReleaseBatchClient) AbandonPublisherLease(context.Context, string, string, string) error {
	return nil
}
func (c *publisherReleaseBatchClient) SyncActiveIngestPlacement(_ context.Context, _ string, _ []*pb.ActiveIngestStream, releases []*pb.ActiveIngestStream) (*pb.SyncActiveIngestPlacementResponse, error) {
	if c.rpcErr != nil {
		return nil, c.rpcErr
	}
	response := &pb.SyncActiveIngestPlacementResponse{ExecutionFencesApplied: c.fenced}
	for _, release := range releases {
		if release.GetClaimToken() == c.refused {
			response.ReleaseRefused = append(response.ReleaseRefused, &pb.ActiveIngestStream{TenantId: release.GetTenantId(), InternalName: release.GetInternalName(), ClusterId: release.GetClusterId(), ClaimToken: release.GetClaimToken()})
		}
	}
	return response, nil
}
