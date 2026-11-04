//go:build schema_verify

package control

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"github.com/google/uuid"
)

func TestPublisherRecoveryDispatch_RealPG(t *testing.T) {
	testPublisherRecoveryDispatch(t, startRealPG(t))
}
func TestPublisherRecoveryDispatch_RealYugabyte(t *testing.T) {
	conn, _, ok := dockerpg.OpenSharedYugabyteBaselineWithDSN(t, "publisher_dispatch", "foghorn")
	if !ok {
		t.Fatal("requires shared Yugabyte fixture")
	}
	testPublisherRecoveryDispatch(t, conn)
}
func testPublisherRecoveryDispatch(t *testing.T, conn *sql.DB) {
	previous, hints := db, dvrStopDispatchHints
	SetDB(conn)
	dvrStopDispatchHints = make(chan DVRStopDispatchHint, 64)
	t.Cleanup(func() { SetDB(previous); dvrStopDispatchHints = hints })
	ctx, logger := t.Context(), logging.NewLogger()
	request := func() IngestSessionRequest {
		return IngestSessionRequest{TenantID: uuid.NewString(), StreamID: uuid.NewString(), InternalName: uuid.NewString(), NodeID: uuid.NewString(), TriggerUUID: uuid.NewString(), ConnectorPID: 100, StartedAtMillis: time.Now().UnixMilli(), IngestClusterID: "cell-a", Authority: &IngestAuthoritySnapshot{CapacityMaxStreams: 1}}
	}
	t.Run("takeover and settlement wake durable DVR stops", func(t *testing.T) {
		repository, req := NewPublisherAdmissionRepository(conn), request()
		prepare := func() PreparedPublisherAdmission {
			t.Helper()
			got, err := repository.Prepare(ctx, req, logger)
			if err != nil || got.Outcome != IngestSessionActive {
				t.Fatalf("prepare=%+v err=%v", got, err)
			}
			return got
		}
		insertDVR := func(generation string) string {
			t.Helper()
			hash := uuid.NewString()[:32]
			_, err := conn.ExecContext(ctx, `INSERT INTO foghorn.artifacts (artifact_hash,artifact_type,stream_internal_name,tenant_id,status,dvr_start_dispatch,ingest_generation) VALUES ($1,'dvr',$2,$3::uuid,'recording','{"state":"recording","node_id":"storage-dispatch"}'::jsonb,$4::uuid)`, hash, req.InternalName, req.TenantID, generation)
			if err != nil {
				t.Fatal(err)
			}
			return hash
		}
		first := prepare()
		hash := insertDVR(first.Generation)
		req.TriggerUUID = uuid.NewString()
		req.StartedAtMillis++
		second := prepare()
		select {
		case hint := <-dvrStopDispatchHints:
			if len(hint.Claims) != 1 || hint.Claims[0].DVRHash != hash || hint.Claims[0].StorageNodeID != "storage-dispatch" {
				t.Fatalf("takeover hint=%+v", hint)
			}
		default:
			t.Fatal("committed takeover stop was not queued for immediate worker dispatch")
		}
		hash = insertDVR(second.Generation)
		if err := foghorndb.New(conn).RecordIngestAdmissionAbandonment(ctx, foghorndb.RecordIngestAdmissionAbandonmentParams{NodeID: req.NodeID, StartTriggerUuid: req.TriggerUUID, ConnectorPid: req.ConnectorPID}); err != nil {
			t.Fatal(err)
		}
		if settled, err := repository.Settle(ctx, req, second.Generation, IngestEndedAdmissionAbandoned); err != nil || !settled {
			t.Fatalf("settle=%v %v", settled, err)
		}
		select {
		case hint := <-dvrStopDispatchHints:
			if hint.TenantID != req.TenantID || hint.Generation != second.Generation {
				t.Fatalf("settle hint=%+v", hint)
			}
			claims, err := foghorndb.New(conn).ListDVRStopClaimsByGeneration(ctx, foghorndb.ListDVRStopClaimsByGenerationParams{TenantID: hint.TenantID, Generation: hint.Generation})
			if err != nil || len(claims) != 1 || claims[0].ArtifactHash != hash {
				t.Fatalf("durable claims=%+v %v", claims, err)
			}
		default:
			t.Fatal("committed settlement stop was not queued for immediate worker dispatch")
		}
	})
	t.Run("local marker completion is independent of lease forwarding", func(t *testing.T) {
		req := request()
		prepared, err := NewPublisherAdmissionRepository(conn).Prepare(ctx, req, logger)
		if err != nil || prepared.Outcome != IngestSessionActive {
			t.Fatalf("prepare: %+v %v", prepared, err)
		}
		q := foghorndb.New(conn)
		params := foghorndb.RecordIngestAdmissionAbandonmentParams{NodeID: req.NodeID, StartTriggerUuid: req.TriggerUUID, ConnectorPid: req.ConnectorPID}
		if err := q.RecordIngestAdmissionAbandonment(ctx, params); err != nil {
			t.Fatal(err)
		}
		if changed, err := q.MarkPublisherSessionsSettled(ctx, foghorndb.MarkPublisherSessionsSettledParams{NodeID: req.NodeID, TriggerUuid: req.TriggerUUID}); err != nil || changed != 0 {
			t.Fatalf("open session's local settlement was acknowledged: changed=%d err=%v", changed, err)
		}
		client := &unavailablePublisherForwarder{}
		if err := RecoverPublisherAdmissions(ctx, client); err == nil {
			t.Fatal("global forwarding outage was lost")
		}
		var local, global bool
		if err := conn.QueryRowContext(ctx, `SELECT publisher_sessions_settled_at IS NOT NULL,publisher_lease_settled_at IS NOT NULL
 FROM foghorn.ingest_admission_abandonments WHERE node_id=$1 AND start_trigger_uuid=$2`, req.NodeID, req.TriggerUUID).Scan(&local, &global); err != nil || !local || global {
			t.Fatalf("local settlement must finish independently of global outage: local=%v global=%v err=%v", local, global, err)
		}
		pending, err := q.ListPublisherAdmissionsToSettle(ctx, IngestAdmissionWindow.Milliseconds())
		if err != nil {
			t.Fatal(err)
		}
		for _, candidate := range pending {
			if candidate.NodeID == req.NodeID && candidate.StartTriggerUuid == req.TriggerUUID {
				t.Fatal("settled local marker was rescanned while global forwarding is unavailable")
			}
		}
		resumed, err := NewPublisherAdmissionRepository(conn).Prepare(ctx, req, logger)
		if err != nil || resumed.Outcome != IngestSessionAlreadyEnded {
			t.Fatalf("locally settled abandonment lost its fence: %+v %v", resumed, err)
		}
	})

	t.Run("legacy abandonment retains exact claim release until acknowledged", func(t *testing.T) {
		req := request()
		generation, outcome, err := MintIngestSession(ctx, req, logger)
		if err != nil || outcome != IngestSessionActive {
			t.Fatalf("legacy mint=%s %v %v", generation, outcome, err)
		}
		if err := foghorndb.New(conn).RecordIngestAdmissionAbandonment(ctx, foghorndb.RecordIngestAdmissionAbandonmentParams{NodeID: req.NodeID, StartTriggerUuid: req.TriggerUUID, ConnectorPid: req.ConnectorPID}); err != nil {
			t.Fatal(err)
		}
		client := &legacyRecoveryReleaseClient{fail: true}
		if err := RecoverPublisherAdmissions(ctx, client); err == nil {
			t.Fatal("legacy claim release failure was lost; metadata-free lease remains held")
		}
		var ended, forwarded bool
		if err := conn.QueryRowContext(ctx, `SELECT ended_at IS NOT NULL FROM foghorn.ingest_sessions WHERE tenant_id=$1::uuid AND id=$2::uuid`, req.TenantID, generation).Scan(&ended); err != nil || !ended {
			t.Fatalf("retired=%v %v", ended, err)
		}
		if err := conn.QueryRowContext(ctx, `SELECT publisher_lease_settled_at IS NOT NULL FROM foghorn.ingest_admission_abandonments WHERE node_id=$1 AND start_trigger_uuid=$2`, req.NodeID, req.TriggerUUID).Scan(&forwarded); err != nil || forwarded {
			t.Fatalf("failed legacy release acknowledged=%v %v", forwarded, err)
		}
		client.fail = false
		if err := RecoverPublisherAdmissions(ctx, client); err != nil {
			t.Fatal(err)
		}
		if client.released == nil || client.released.GetTenantId() != req.TenantID || client.released.GetInternalName() != req.InternalName || client.released.GetClaimToken() != req.TriggerUUID || client.cluster != req.IngestClusterID || client.released.GetNodeId() != "" || client.released.GetControlCellId() != "" {
			t.Fatalf("legacy exact-owner release=%+v cluster=%q", client.released, client.cluster)
		}
		if err := conn.QueryRowContext(ctx, `SELECT publisher_lease_settled_at IS NOT NULL FROM foghorn.ingest_admission_abandonments WHERE node_id=$1 AND start_trigger_uuid=$2`, req.NodeID, req.TriggerUUID).Scan(&forwarded); err != nil || !forwarded {
			t.Fatalf("successful legacy release unacknowledged=%v %v", forwarded, err)
		}
	})
}

type legacyRecoveryReleaseClient struct {
	fail     bool
	released *pb.ActiveIngestStream
	cluster  string
}

func (*legacyRecoveryReleaseClient) AbandonPublisherLease(context.Context, string, string, string) error {
	return nil
}
func (c *legacyRecoveryReleaseClient) SyncActiveIngestPlacement(_ context.Context, cluster string, _ []*pb.ActiveIngestStream, release []*pb.ActiveIngestStream) (*pb.SyncActiveIngestPlacementResponse, error) {
	for _, entry := range release {
		if entry.GetNodeId() == "" {
			if c.fail {
				return nil, errors.New("legacy release unavailable")
			}
			c.released = entry
			c.cluster = cluster
		}
	}
	return &pb.SyncActiveIngestPlacementResponse{ExecutionFencesApplied: true}, nil
}

type unavailablePublisherForwarder struct{ legacyRecoveryReleaseClient }

func (*unavailablePublisherForwarder) AbandonPublisherLease(context.Context, string, string, string) error {
	return errors.New("Commodore unavailable")
}
