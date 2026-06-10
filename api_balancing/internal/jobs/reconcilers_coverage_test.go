package jobs

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// reconSendingStream is a fake HelmsmanControl_ConnectServer that records every
// ControlMessage the registry sends through it. Seeding it under a nodeID makes
// control.Send{Clip,DVR,Vod}Delete succeed (local delivery) instead of returning
// ErrNotConnected, so the orphan-retry success branch is reachable.
type reconSendingStream struct {
	ipcpb.HelmsmanControl_ConnectServer
	mu   sync.Mutex
	sent []*ipcpb.ControlMessage
}

func (s *reconSendingStream) Send(msg *ipcpb.ControlMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, msg)
	return nil
}

func (s *reconSendingStream) messages() []*ipcpb.ControlMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*ipcpb.ControlMessage, len(s.sent))
	copy(out, s.sent)
	return out
}

func newOrphanJobRecon(t *testing.T) *OrphanCleanupJob {
	t.Helper()
	return NewOrphanCleanupJob(OrphanCleanupConfig{Logger: logging.NewLogger()})
}

// TestRetryClipDeletionSendsToOwningNodeRecon locks the orphan-clip reconcile
// decision: a soft-deleted clip that still has a live node copy is re-issued a
// ClipDeleteRequest addressed to THAT node, carrying the clip's hash. The retry
// targets the node holding the storage artifact — not a broadcast — so the
// delete lands where the orphaned bytes actually live.
func TestRetryClipDeletionSendsToOwningNodeRecon(t *testing.T) {
	stream := &reconSendingStream{}
	restore := control.SetupTestRegistry("clip-node-recon", stream)
	defer restore()

	j := newOrphanJobRecon(t)
	j.retryClipDeletion(context.Background(), orphanedClip{ClipHash: "clip-hash-recon", NodeID: "clip-node-recon"})

	msgs := stream.messages()
	if len(msgs) != 1 {
		t.Fatalf("expected exactly 1 control message sent, got %d", len(msgs))
	}
	cd := msgs[0].GetClipDelete()
	if cd == nil {
		t.Fatalf("expected a ClipDelete payload, got %T", msgs[0].GetPayload())
	}
	if cd.GetClipHash() != "clip-hash-recon" {
		t.Fatalf("ClipDelete targeted hash %q; want clip-hash-recon", cd.GetClipHash())
	}
	if cd.GetRequestId() == "" {
		t.Fatal("ClipDelete carried empty request_id; retries must be correlatable")
	}
}

// TestRetryDVRDeletionSendsToOwningNodeRecon: same reconcile invariant for DVRs.
func TestRetryDVRDeletionSendsToOwningNodeRecon(t *testing.T) {
	stream := &reconSendingStream{}
	restore := control.SetupTestRegistry("dvr-node-recon", stream)
	defer restore()

	j := newOrphanJobRecon(t)
	j.retryDVRDeletion(context.Background(), orphanedDVR{DVRHash: "dvr-hash-recon", NodeID: "dvr-node-recon"})

	msgs := stream.messages()
	if len(msgs) != 1 {
		t.Fatalf("expected exactly 1 control message sent, got %d", len(msgs))
	}
	dd := msgs[0].GetDvrDelete()
	if dd == nil {
		t.Fatalf("expected a DvrDelete payload, got %T", msgs[0].GetPayload())
	}
	if dd.GetDvrHash() != "dvr-hash-recon" {
		t.Fatalf("DvrDelete targeted hash %q; want dvr-hash-recon", dd.GetDvrHash())
	}
}

// TestRetryVODDeletionSendsToOwningNodeRecon: same reconcile invariant for VODs.
func TestRetryVODDeletionSendsToOwningNodeRecon(t *testing.T) {
	stream := &reconSendingStream{}
	restore := control.SetupTestRegistry("vod-node-recon", stream)
	defer restore()

	j := newOrphanJobRecon(t)
	j.retryVODDeletion(context.Background(), orphanedVOD{VODHash: "vod-hash-recon", NodeID: "vod-node-recon"})

	msgs := stream.messages()
	if len(msgs) != 1 {
		t.Fatalf("expected exactly 1 control message sent, got %d", len(msgs))
	}
	vd := msgs[0].GetVodDelete()
	if vd == nil {
		t.Fatalf("expected a VodDelete payload, got %T", msgs[0].GetPayload())
	}
	if vd.GetVodHash() != "vod-hash-recon" {
		t.Fatalf("VodDelete targeted hash %q; want vod-hash-recon", vd.GetVodHash())
	}
}

// TestRetryDeletionToleratesDisconnectedNodeRecon locks the error-tolerance
// contract of the orphan sweep: when the owning node has no live control-stream
// connection (registry has no conn for it), the retry logs and returns without
// panicking or sending. A reconcile pass must survive a node being offline —
// the next tick retries — rather than crashing the whole sweep loop.
func TestRetryDeletionToleratesDisconnectedNodeRecon(t *testing.T) {
	// Registry exists but holds NO connection for the target node ->
	// control.Send*Delete returns ErrNotConnected.
	restore := control.SetupTestRegistry("", nil)
	defer restore()

	j := newOrphanJobRecon(t)
	ctx := context.Background()
	// None of these must panic; the warn-and-return branch is the contract.
	j.retryClipDeletion(ctx, orphanedClip{ClipHash: "h1", NodeID: "gone-node"})
	j.retryDVRDeletion(ctx, orphanedDVR{DVRHash: "h2", NodeID: "gone-node"})
	j.retryVODDeletion(ctx, orphanedVOD{VODHash: "h3", NodeID: "gone-node"})
}

// TestDispatchJobNoNodeRevertsRecon locks the routing decision on the
// no-capacity path: when routeProcessingJob finds no alive/capable node, the
// dispatcher must NOT strand the job. It reverts the job row to 'queued' AND
// projects the clip artifact back to 'queued' with the no-node reason — never
// leaving a job claimed against a node that can't run it. With a fresh state
// manager holding zero nodes, routing returns "" deterministically.
func TestDispatchJobNoNodeRevertsRecon(t *testing.T) {
	restore := control.SetupTestRegistry("", nil)
	defer restore()
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	// Intentionally seed NO nodes -> routeProcessingJob returns "no alive nodes".

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectExec("UPDATE foghorn.processing_jobs\\s+SET status = 'queued', processing_node_id = NULL").
		WithArgs("job-nonode-recon").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE foghorn.artifacts").
		WithArgs("hash-nonode-recon", "tenant-nonode", "queued").
		WillReturnResult(sqlmock.NewResult(0, 1))

	d := NewProcessingDispatcher(ProcessingDispatcherConfig{DB: db, Logger: logging.NewLogger()})
	d.dispatchJob(context.Background(), &processingJob{
		JobID:        "job-nonode-recon",
		TenantID:     "tenant-nonode",
		ArtifactHash: sql.NullString{String: "hash-nonode-recon", Valid: true},
		ArtifactType: sql.NullString{String: "clip", Valid: true},
		JobType:      "process",
		SourceURL:    sql.NullString{String: "https://origin.example/s.mp4", Valid: true},
	})

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestRevertToQueuedToleratesDBErrorRecon locks error-tolerance on the revert
// path: if the processing_jobs UPDATE fails, the dispatcher logs and returns
// rather than propagating — the recovery sweep will re-attempt. A single
// non-retryable DB failure here must not crash the dispatch loop. sqlmock
// returns a non-driver error so RetryPostgres (if any) treats it terminally.
func TestRevertToQueuedToleratesDBErrorRecon(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectExec("UPDATE foghorn.processing_jobs\\s+SET status = 'queued', processing_node_id = NULL").
		WithArgs("job-revert-err-recon").
		WillReturnError(errors.New("revert boom"))

	d := NewProcessingDispatcher(ProcessingDispatcherConfig{DB: db, Logger: logging.NewLogger()})
	d.revertToQueued(context.Background(), "job-revert-err-recon")

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestFailVODArtifactFiltersByTenantRecon locks tenant isolation on the
// exhausted-VOD failure projection: the UPDATE that marks an artifact 'failed'
// carries BOTH artifact_hash AND tenant_id, so a job exhaustion can never flip
// another tenant's artifact that shares a hash collision to 'failed'. This is
// the only test that drives failVODArtifact directly with an asserted tenant arg.
func TestFailVODArtifactFiltersByTenantRecon(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectExec("UPDATE foghorn.artifacts").
		WithArgs("vod-fail-hash-recon", "tenant-vodfail", "max retries exceeded").
		WillReturnResult(sqlmock.NewResult(0, 1))

	d := NewProcessingDispatcher(ProcessingDispatcherConfig{DB: db, Logger: logging.NewLogger()})
	d.failVODArtifact(context.Background(), "vod-fail-hash-recon", "tenant-vodfail", "max retries exceeded")

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestFailVODArtifactToleratesDBErrorRecon locks error-tolerance on the same
// projection: a failing UPDATE is logged and swallowed so the recovery sweep
// that drives failVODArtifact for a batch of exhausted jobs isn't aborted by
// one bad row.
func TestFailVODArtifactToleratesDBErrorRecon(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectExec("UPDATE foghorn.artifacts").
		WithArgs("vod-fail-hash-recon2", "tenant-vodfail2", "max retries exceeded").
		WillReturnError(errors.New("fail-vod boom"))

	d := NewProcessingDispatcher(ProcessingDispatcherConfig{DB: db, Logger: logging.NewLogger()})
	d.failVODArtifact(context.Background(), "vod-fail-hash-recon2", "tenant-vodfail2", "max retries exceeded")

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestPurgeStaleNodeRowsToleratesDeleteErrorRecon locks error-tolerance on the
// stale orphan-node reap: if the DELETE itself errors, the sweep logs and
// returns WITHOUT attempting to read RowsAffected (which would panic on a nil
// result). A DB hiccup must leave the registry untouched for the next cycle, not
// crash the purge job.
func TestPurgeStaleNodeRowsToleratesDeleteErrorRecon(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectExec("DELETE FROM foghorn.artifact_nodes").
		WillReturnError(errors.New("purge-node boom"))

	j := NewPurgeDeletedJob(PurgeDeletedConfig{DB: db, Logger: logging.NewLogger()})
	j.purgeStaleNodeRows(context.Background())

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
