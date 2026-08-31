package grpc

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// serviceCtx is what the shared interceptor leaves behind for a service-token
// call. It also accepts JWTs, which is why these mutations check the type.
func serviceCtx() context.Context {
	return context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")
}

func placementServer(t *testing.T) (*CommodoreServer, sqlmock.Sqlmock, func()) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	return &CommodoreServer{db: db, logger: logrus.New()}, mock, func() { _ = db.Close() }
}

// ingestStream names a stream and the connection that owns its claim. Both
// halves of a sync are owner-fenced, so a token is always part of the identity.
func ingestStream(tenantID, internalName string) *commodorepb.ActiveIngestStream {
	return &commodorepb.ActiveIngestStream{
		TenantId:     tenantID,
		InternalName: internalName,
		ClaimToken:   "conn-" + internalName,
	}
}

func expectPlacementSync(mock sqlmock.Sqlmock, renew bool, updatePattern string, affected int64, refused ...*commodorepb.ActiveIngestStream) {
	mock.ExpectBegin()
	exec := mock.ExpectExec(updatePattern)
	query := mock.ExpectQuery(`FROM unnest`)
	if renew {
		exec.WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), int64(activeIngestLease.Seconds()))
		query.WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), int64(activeIngestLease.Seconds()))
	} else {
		exec.WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg())
		query.WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg())
	}
	exec.WillReturnResult(sqlmock.NewResult(0, affected))
	rows := sqlmock.NewRows([]string{"tenant_id", "internal_name", "claim_token", "cluster_id"})
	for _, stream := range refused {
		rows.AddRow(stream.GetTenantId(), stream.GetInternalName(), stream.GetClaimToken(), stream.GetClusterId())
	}
	query.WillReturnRows(rows)
	mock.ExpectCommit()
}

// Renewal re-asserts the claim under the same contention rule PUSH_REWRITE
// applies: it refreshes this cluster's own claim, and may take one only when
// the row holds none or holds one that already lapsed. A fresh claim held by
// another cluster is never disturbed.
func TestSyncActiveIngestPlacement_RenewRespectsContentionGuard(t *testing.T) {
	s, mock, done := placementServer(t)
	defer done()

	expectPlacementSync(mock, true, `SET active_ingest_cluster_id = t\.cluster_id[\s\S]*active_ingest_cluster_updated_at < NOW\(\)[\s\S]*s\.active_ingest_claim_id = t\.claim_token`, 2)

	resp, err := s.SyncActiveIngestPlacement(serviceCtx(), &commodorepb.SyncActiveIngestPlacementRequest{
		ClusterId: "media-eu",
		Renew:     []*commodorepb.ActiveIngestStream{ingestStream("t1", "s1"), ingestStream("t1", "s2")},
	})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if resp.GetRenewed() != 2 {
		t.Fatalf("renewed = %d, want 2", resp.GetRenewed())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("renewal did not run: %v", err)
	}
}

// A release nulls the claim, again fenced on this cluster holding it, so a
// close arriving after the publisher moved to a peer cannot unpin them.
func TestSyncActiveIngestPlacement_ReleaseClearsOwnClaim(t *testing.T) {
	s, mock, done := placementServer(t)
	defer done()

	expectPlacementSync(mock, false, `SET active_ingest_cluster_id = NULL[\s\S]*s\.active_ingest_cluster_id = t\.cluster_id`, 1)

	resp, err := s.SyncActiveIngestPlacement(serviceCtx(), &commodorepb.SyncActiveIngestPlacementRequest{
		ClusterId: "media-eu",
		Release:   []*commodorepb.ActiveIngestStream{ingestStream("t1", "s1")},
	})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if resp.GetReleased() != 1 {
		t.Fatalf("released = %d, want 1", resp.GetReleased())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("release did not run: %v", err)
	}
}

// An empty sync issues no statement at all: the renewal job ticks on a cluster
// with no live pushes constantly.
func TestSyncActiveIngestPlacement_EmptyIssuesNoStatement(t *testing.T) {
	s, mock, done := placementServer(t)
	defer done()

	if _, err := s.SyncActiveIngestPlacement(serviceCtx(), &commodorepb.SyncActiveIngestPlacementRequest{
		ClusterId: "media-eu",
	}); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected statement: %v", err)
	}
}

func TestSyncActiveIngestPlacement_RejectsIncompleteInput(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  *commodorepb.SyncActiveIngestPlacementRequest
	}{
		{
			// An entry may name its own cluster, or inherit the request's — but
			// with neither there is nothing to place it in.
			name: "no cluster on the entry or the request",
			req:  &commodorepb.SyncActiveIngestPlacementRequest{Renew: []*commodorepb.ActiveIngestStream{ingestStream("t1", "s1")}},
		},
		{
			// Without a tenant the pair cannot scope the update to one row.
			name: "stream without tenant",
			req: &commodorepb.SyncActiveIngestPlacementRequest{
				ClusterId: "media-eu",
				Renew:     []*commodorepb.ActiveIngestStream{ingestStream("", "s1")},
			},
		},
		{
			name: "stream without internal name",
			req: &commodorepb.SyncActiveIngestPlacementRequest{
				ClusterId: "media-eu",
				Release:   []*commodorepb.ActiveIngestStream{ingestStream("t1", "")},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock, done := placementServer(t)
			defer done()

			if _, err := s.SyncActiveIngestPlacement(serviceCtx(), tc.req); status.Code(err) != codes.InvalidArgument {
				t.Fatalf("err = %v, want InvalidArgument", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("a statement ran for rejected input: %v", err)
			}
		})
	}
}

// One sync must not turn into an unbounded UPDATE; the caller batches instead.
func TestSyncActiveIngestPlacement_RejectsOversizedBatch(t *testing.T) {
	s, _, done := placementServer(t)
	defer done()

	streams := make([]*commodorepb.ActiveIngestStream, maxActiveIngestPlacementSync+1)
	for i := range streams {
		streams[i] = ingestStream("t1", "s")
	}

	if _, err := s.SyncActiveIngestPlacement(serviceCtx(), &commodorepb.SyncActiveIngestPlacementRequest{
		ClusterId: "media-eu",
		Renew:     streams,
	}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("err = %v, want InvalidArgument", err)
	}
}

// Placement is service-to-service state. The shared interceptor also accepts
// JWTs, so without an explicit check any logged-in user could renew or release
// any stream's ingest placement by naming its tenant and internal name.
func TestSyncActiveIngestPlacement_RejectsNonServiceAuth(t *testing.T) {
	for _, authType := range []string{"jwt", "api_token", ""} {
		t.Run("auth_"+authType, func(t *testing.T) {
			s, mock, done := placementServer(t)
			defer done()

			ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, authType)
			_, err := s.SyncActiveIngestPlacement(ctx, &commodorepb.SyncActiveIngestPlacementRequest{
				ClusterId: "media-eu",
				Renew:     []*commodorepb.ActiveIngestStream{ingestStream("t1", "s1")},
			})
			if status.Code(err) != codes.PermissionDenied {
				t.Fatalf("err = %v, want PermissionDenied", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("a statement ran for an unauthorized caller: %v", err)
			}
		})
	}
}

// Same for the managed-stream clear, which mutates the identical column.
func TestClearStreamActiveCluster_RejectsNonServiceAuth(t *testing.T) {
	s, mock, done := placementServer(t)
	defer done()

	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "jwt")
	_, err := s.ClearStreamActiveCluster(ctx, &commodorepb.ClearStreamActiveClusterRequest{
		StreamId:          "stream-1",
		ExpectedClusterId: "media-eu",
		TenantId:          "t1",
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("err = %v, want PermissionDenied", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("a statement ran for an unauthorized caller: %v", err)
	}
}

// The clear is a tenant-scoped mutation like every other placement write: a
// caller that names only a stream id must not be able to clear it.
func TestClearStreamActiveCluster_RequiresTenant(t *testing.T) {
	s, mock, done := placementServer(t)
	defer done()

	_, err := s.ClearStreamActiveCluster(serviceCtx(), &commodorepb.ClearStreamActiveClusterRequest{
		StreamId:          "stream-1",
		ExpectedClusterId: "media-eu",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("err = %v, want InvalidArgument", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("a statement ran without tenant scope: %v", err)
	}
}

// The clear's UPDATE must carry the tenant filter, not just the request field.
func TestClearStreamActiveCluster_ScopesUpdateByTenant(t *testing.T) {
	s, mock, done := placementServer(t)
	defer done()

	mock.ExpectExec(`ClearManagedStreamActiveCluster[\s\S]*tenant_id = \$2`).
		WithArgs("stream-1", "t1", "media-eu", managedClaimToken("stream-1")).
		WillReturnResult(sqlmock.NewResult(0, 1))

	resp, err := s.ClearStreamActiveCluster(serviceCtx(), &commodorepb.ClearStreamActiveClusterRequest{
		StreamId:          "stream-1",
		ExpectedClusterId: "media-eu",
		TenantId:          "t1",
	})
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if !resp.GetCleared() {
		t.Fatal("cleared = false")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("tenant-scoped clear did not run: %v", err)
	}
}

// A renewal establishes the claim when the row holds none, or holds one that
// has already lapsed — the same contention rule PUSH_REWRITE applies. Without
// it, a publisher admitted from the validation cache during a Commodore outage
// would stay unplaced for its whole session, since there would be no claim of
// this cluster's to refresh.
func TestSyncActiveIngestPlacement_RenewEstablishesLapsedClaim(t *testing.T) {
	s, mock, done := placementServer(t)
	defer done()

	expectPlacementSync(mock, true, `SET active_ingest_cluster_id = t\.cluster_id[\s\S]*active_ingest_cluster_id IS NULL[\s\S]*active_ingest_cluster_updated_at <`, 1)

	if _, err := s.SyncActiveIngestPlacement(serviceCtx(), &commodorepb.SyncActiveIngestPlacementRequest{
		ClusterId: "media-eu",
		Renew:     []*commodorepb.ActiveIngestStream{ingestStream("t1", "s1")},
	}); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("renewal did not use the contended-update guard: %v", err)
	}
}

// A release names the publisher connection that owns the claim, and clears
// nothing without it. This is what stops an attempt that never took the claim
// — a different node in the same cluster, or a Foghorn admitting from its
// validation cache while Commodore was unreachable — from unpinning a live
// publisher.
func TestSyncActiveIngestPlacement_ReleaseIsOwnerFenced(t *testing.T) {
	s, mock, done := placementServer(t)
	defer done()

	stream := ingestStream("t1", "s1")
	stream.ClaimToken = "someone-elses-connection"
	refused := &commodorepb.ActiveIngestStream{TenantId: stream.GetTenantId(), InternalName: stream.GetInternalName(), ClaimToken: stream.GetClaimToken(), ClusterId: "media-eu"}
	expectPlacementSync(mock, false, `SET active_ingest_cluster_id = NULL[\s\S]*s\.active_ingest_claim_id = t\.claim_token`, 0, refused)
	resp, err := s.SyncActiveIngestPlacement(serviceCtx(), &commodorepb.SyncActiveIngestPlacementRequest{
		ClusterId: "media-eu",
		Release:   []*commodorepb.ActiveIngestStream{stream},
	})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if resp.GetReleased() != 0 {
		t.Fatalf("released = %d, want 0 for a claim owned by another connection", resp.GetReleased())
	}
	if len(resp.GetReleaseRefused()) != 1 || resp.GetReleaseRefused()[0].GetClaimToken() != stream.GetClaimToken() {
		t.Fatalf("release refusal = %+v, want the non-owning claim", resp.GetReleaseRefused())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("owner-fenced release did not run: %v", err)
	}
}

// Both halves are owner-fenced, and an empty token matches no owner, so a
// tokenless entry could only be a caller acting on a claim it cannot name.
// Refused outright rather than silently matching nothing.
func TestSyncActiveIngestPlacement_RejectsTokenlessStreams(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  *commodorepb.SyncActiveIngestPlacementRequest
	}{
		{
			name: "renew without a token",
			req: &commodorepb.SyncActiveIngestPlacementRequest{
				ClusterId: "media-eu",
				Renew:     []*commodorepb.ActiveIngestStream{{TenantId: "t1", InternalName: "s1"}},
			},
		},
		{
			name: "release without a token",
			req: &commodorepb.SyncActiveIngestPlacementRequest{
				ClusterId: "media-eu",
				Release:   []*commodorepb.ActiveIngestStream{{TenantId: "t1", InternalName: "s1"}},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock, done := placementServer(t)
			defer done()

			if _, err := s.SyncActiveIngestPlacement(serviceCtx(), tc.req); status.Code(err) != codes.InvalidArgument {
				t.Fatalf("err = %v, want InvalidArgument", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("a statement ran for a tokenless stream: %v", err)
			}
		})
	}
}

// The claim a publisher takes is stamped with that connection's identity, and
// the response says so — which is the caller's only licence to release it.
func TestValidateStreamKey_ReportsClaimAcquisition(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{"id", "user_id", "tenant_id", "internal_name", "is_active", "is_recording_enabled", "playback_id", "ingest_mode"}).
		AddRow("stream-id", "user-id", "tenant-id", "internal", true, true, "pk", "push")
	mock.ExpectQuery("FROM commodore.streams").WithArgs("good-key").WillReturnRows(rows)
	mock.ExpectQuery("UPDATE commodore.streams").
		WithArgs("cluster-us", "trigger-uuid-1", int64(activeIngestLease.Seconds()), "good-key").
		WillReturnRows(claimReserved())

	server := &CommodoreServer{
		db:            db,
		logger:        logrus.New(),
		routeCache:    map[string]*clusterRoute{"tenant-id": admittingRoute("cluster-us")},
		routeCacheTTL: 5 * time.Minute,
	}

	resp, err := server.ValidateStreamKey(context.Background(), &commodorepb.ValidateStreamKeyRequest{
		StreamKey:  "good-key",
		ClusterId:  "cluster-us",
		ClaimToken: "trigger-uuid-1",
	})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !resp.GetClaimAcquired() {
		t.Fatal("claim was taken but not reported as acquired")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("claim was not stamped with its owner: %v", err)
	}
}

// Declaring a cluster is what takes the claim, so a caller that names one
// without naming the publisher connection is refused before any write. An
// unowned claim could never be released — release is owner-fenced — so it would
// hold the stream's placement until it expired.
func TestValidateStreamKey_ClusterClaimRequiresAnOwner(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	server := &CommodoreServer{db: db, logger: logrus.New()}
	_, err = server.ValidateStreamKey(context.Background(), &commodorepb.ValidateStreamKeyRequest{
		StreamKey: "good-key",
		ClusterId: "cluster-us",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("err = %v, want InvalidArgument", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("an unowned claim reached the database: %v", err)
	}
}

// A caller that names no cluster takes no claim, so it needs no owner — this is
// the GraphQL/MCP key check, which must keep working.
func TestValidateStreamKey_NoClusterNeedsNoOwner(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("FROM commodore.streams").WithArgs("good-key").WillReturnError(sql.ErrNoRows)

	server := &CommodoreServer{db: db, logger: logrus.New()}
	resp, err := server.ValidateStreamKey(context.Background(), &commodorepb.ValidateStreamKeyRequest{
		StreamKey: "good-key",
	})
	if err != nil {
		t.Fatalf("tokenless check without a cluster was refused: %v", err)
	}
	if resp.GetValid() {
		t.Fatal("unknown key reported valid")
	}
}

// A contended claim is not this caller's: the row already carries a fresher
// claim, so nothing was acquired and nothing may be released.
func TestValidateStreamKey_ContendedClaimIsNotAcquired(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{"id", "user_id", "tenant_id", "internal_name", "is_active", "is_recording_enabled", "playback_id", "ingest_mode"}).
		AddRow("stream-id", "user-id", "tenant-id", "internal", true, true, "pk", "push")
	mock.ExpectQuery("FROM commodore.streams").WithArgs("good-key").WillReturnRows(rows)
	// The guard matched nothing: someone else owns the live claim.
	mock.ExpectQuery("UPDATE commodore.streams").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("SELECT active_ingest_cluster_id").
		WillReturnRows(sqlmock.NewRows([]string{"active_ingest_cluster_id", "active_ingest_claim_id"}).AddRow("cluster-us", "other-owner"))

	server := &CommodoreServer{
		db:            db,
		logger:        logrus.New(),
		routeCache:    map[string]*clusterRoute{"tenant-id": admittingRoute("cluster-us")},
		routeCacheTTL: 5 * time.Minute,
	}

	resp, err := server.ValidateStreamKey(context.Background(), &commodorepb.ValidateStreamKeyRequest{
		StreamKey:  "good-key",
		ClusterId:  "cluster-us",
		ClaimToken: "trigger-uuid-2",
	})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if resp.GetClaimAcquired() {
		t.Fatal("a contended claim was reported as acquired")
	}
	if resp.GetValid() || resp.GetRejectionReason() != commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_DUPLICATE_INGEST {
		t.Fatalf("contended claim response = %+v, want duplicate-ingest denial", resp)
	}
}

func TestValidateStreamKey_ContendedClaimReadFailureDenies(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{"id", "user_id", "tenant_id", "internal_name", "is_active", "is_recording_enabled", "playback_id", "ingest_mode"}).
		AddRow("stream-id", "user-id", "tenant-id", "internal", true, true, "pk", "push")
	mock.ExpectQuery("FROM commodore.streams").WithArgs("good-key").WillReturnRows(rows)
	mock.ExpectQuery("UPDATE commodore.streams").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("SELECT active_ingest_cluster_id").WithArgs("good-key").WillReturnError(context.DeadlineExceeded)

	server := &CommodoreServer{
		db: db, logger: logrus.New(), routeCache: map[string]*clusterRoute{"tenant-id": admittingRoute("cluster-us")}, routeCacheTTL: 5 * time.Minute,
	}
	resp, err := server.ValidateStreamKey(context.Background(), &commodorepb.ValidateStreamKeyRequest{
		StreamKey: "good-key", ClusterId: "cluster-us", ClaimToken: "trigger-uuid-2",
	})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if resp.GetValid() || resp.GetRejectionReason() != commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_DUPLICATE_INGEST {
		t.Fatalf("ambiguous claim response = %+v, want fail-closed denial", resp)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
