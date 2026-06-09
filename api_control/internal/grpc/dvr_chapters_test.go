package grpc

import (
	"context"
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	"google.golang.org/grpc/codes"
)

// assertDVRTenant is the ownership gate for the DVR-chapter pass-through: it
// must reject artifacts not owned by the caller's tenant (NotFound), demand a
// tenant + identifier up front, refuse a row with no origin cluster
// (FailedPrecondition), and on success return the dvr_hash Foghorn keys on —
// NOT the user-supplied identifier.
func TestAssertDVRTenant(t *testing.T) {
	const (
		tenant = "tenant-1"
		ident  = "abc123"
	)

	t.Run("missing_tenant_denied", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, _, err := s.assertDVRTenant(context.Background(), ident, "")
		wantCode(t, err, codes.PermissionDenied)
	})

	t.Run("missing_identifier_invalid", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, _, err := s.assertDVRTenant(context.Background(), "", tenant)
		wantCode(t, err, codes.InvalidArgument)
	})

	t.Run("wrong_tenant_not_found", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		// Tenant-scoped WHERE means a foreign artifact simply returns no rows.
		mock.ExpectQuery("FROM commodore.dvr_recordings").
			WithArgs(ident, tenant).
			WillReturnError(sql.ErrNoRows)
		_, _, err := s.assertDVRTenant(context.Background(), ident, tenant)
		wantCode(t, err, codes.NotFound)
	})

	t.Run("missing_origin_cluster_failed_precondition", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectQuery("FROM commodore.dvr_recordings").
			WithArgs(ident, tenant).
			WillReturnRows(sqlmock.NewRows([]string{"origin_cluster_id", "dvr_hash"}).
				AddRow("", "hash-xyz"))
		_, _, err := s.assertDVRTenant(context.Background(), ident, tenant)
		wantCode(t, err, codes.FailedPrecondition)
	})

	t.Run("happy_returns_cluster_and_canonical_hash", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectQuery("FROM commodore.dvr_recordings").
			WithArgs(ident, tenant).
			WillReturnRows(sqlmock.NewRows([]string{"origin_cluster_id", "dvr_hash"}).
				AddRow("cluster-eu", "hash-xyz"))
		cluster, hash, err := s.assertDVRTenant(context.Background(), ident, tenant)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cluster != "cluster-eu" {
			t.Errorf("origin cluster = %q, want cluster-eu", cluster)
		}
		// Foghorn keys on dvr_hash; the handler must forward THIS, not `ident`.
		if hash != "hash-xyz" {
			t.Errorf("dvr_hash = %q, want hash-xyz", hash)
		}
	})
}

// The pass-through handlers must reject unauthenticated callers before any DB
// or Foghorn work, and surface a tenant-boundary miss as NotFound.
func TestDVRChapterHandlersAuthGate(t *testing.T) {
	t.Run("retrieve_unauthenticated", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, err := s.RetrieveDVRChapter(context.Background(), &foghorncontrolpb.RetrieveDVRChapterRequest{DvrArtifactId: "x"})
		wantCode(t, err, codes.Unauthenticated)
	})

	t.Run("list_unauthenticated", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, err := s.ListDVRChapters(context.Background(), &foghorncontrolpb.ListDVRChaptersRequest{DvrArtifactId: "x"})
		wantCode(t, err, codes.Unauthenticated)
	})

	t.Run("retrieve_wrong_tenant_not_found", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectQuery("FROM commodore.dvr_recordings").
			WithArgs("x", "t1").
			WillReturnError(sql.ErrNoRows)
		_, err := s.RetrieveDVRChapter(ctxAs("u1", "t1", "owner"), &foghorncontrolpb.RetrieveDVRChapterRequest{DvrArtifactId: "x"})
		wantCode(t, err, codes.NotFound)
	})
}
