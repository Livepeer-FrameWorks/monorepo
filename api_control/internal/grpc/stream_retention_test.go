package grpc

import (
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

func i32ptr(v int32) *int32 { return &v }

const (
	streamOwnerSQL    = `SELECT id::text FROM commodore\.streams WHERE id = \$1::uuid AND tenant_id = \$2::uuid`
	streamUpdateSQL   = `UPDATE commodore\.streams SET`
	streamReadbackSQL = `SELECT dvr_retention_days_override, clip_retention_days_override\s+FROM commodore\.streams\s+WHERE id = \$1::uuid`
)

func expectStreamOwned(mock sqlmock.Sqlmock, streamID, tenant string) {
	mock.ExpectQuery(streamOwnerSQL).
		WithArgs(streamID, tenant).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(streamID))
}

func TestSetStreamRetentionOverrides_SetDVRUnderCap(t *testing.T) {
	s, mock, done := newRetentionServer(t)
	defer done()
	const tenant = "11111111-1111-1111-1111-111111111111"
	const stream = "22222222-2222-2222-2222-222222222222"

	expectStreamOwned(mock, stream, tenant)
	// nil purser → cap 30; 7 is under cap → written as-is. Third arg is the value.
	mock.ExpectExec(streamUpdateSQL).
		WithArgs(stream, tenant, int32(7)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(streamReadbackSQL).
		WithArgs(stream).
		WillReturnRows(sqlmock.NewRows([]string{"dvr_retention_days_override", "clip_retention_days_override"}).
			AddRow(int32(7), nil))

	resp, err := s.SetStreamRetentionOverrides(retentionAuthCtx(tenant), &commodorepb.SetStreamRetentionOverridesRequest{
		StreamId:                 stream,
		DvrRetentionDaysOverride: i32ptr(7),
	})
	if err != nil {
		t.Fatalf("SetStreamRetentionOverrides: %v", err)
	}
	if resp.DvrRetentionDaysOverride == nil || *resp.DvrRetentionDaysOverride != 7 {
		t.Errorf("dvr override = %v, want 7", resp.DvrRetentionDaysOverride)
	}
	if resp.ClipRetentionDaysOverride != nil {
		t.Error("clip override should be nil (NULL readback)")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSetStreamRetentionOverrides_ClampsOverCap(t *testing.T) {
	s, mock, done := newRetentionServer(t)
	defer done()
	const tenant = "11111111-1111-1111-1111-111111111111"
	const stream = "22222222-2222-2222-2222-222222222222"

	expectStreamOwned(mock, stream, tenant)
	// 90 > cap 30 → clamped to 30 in the UPDATE arg (the contract under test).
	mock.ExpectExec(streamUpdateSQL).
		WithArgs(stream, tenant, int32(30)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(streamReadbackSQL).
		WithArgs(stream).
		WillReturnRows(sqlmock.NewRows([]string{"dvr_retention_days_override", "clip_retention_days_override"}).
			AddRow(int32(30), nil))

	resp, err := s.SetStreamRetentionOverrides(retentionAuthCtx(tenant), &commodorepb.SetStreamRetentionOverridesRequest{
		StreamId:                 stream,
		DvrRetentionDaysOverride: i32ptr(90),
	})
	if err != nil {
		t.Fatalf("SetStreamRetentionOverrides: %v", err)
	}
	if resp.DvrRetentionDaysOverride == nil || *resp.DvrRetentionDaysOverride != 30 {
		t.Errorf("dvr override = %v, want 30 (clamped to cap)", resp.DvrRetentionDaysOverride)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSetStreamRetentionOverrides_ClearClip(t *testing.T) {
	s, mock, done := newRetentionServer(t)
	defer done()
	const tenant = "11111111-1111-1111-1111-111111111111"
	const stream = "22222222-2222-2222-2222-222222222222"

	expectStreamOwned(mock, stream, tenant)
	// Clear sets the column to NULL — no value arg, only stream + tenant.
	mock.ExpectExec(streamUpdateSQL).
		WithArgs(stream, tenant).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(streamReadbackSQL).
		WithArgs(stream).
		WillReturnRows(sqlmock.NewRows([]string{"dvr_retention_days_override", "clip_retention_days_override"}).
			AddRow(nil, nil))

	resp, err := s.SetStreamRetentionOverrides(retentionAuthCtx(tenant), &commodorepb.SetStreamRetentionOverridesRequest{
		StreamId:                   stream,
		ClearClipRetentionOverride: true,
	})
	if err != nil {
		t.Fatalf("SetStreamRetentionOverrides: %v", err)
	}
	if resp.DvrRetentionDaysOverride != nil || resp.ClipRetentionDaysOverride != nil {
		t.Errorf("both overrides should be nil after clear, got dvr=%v clip=%v",
			resp.DvrRetentionDaysOverride, resp.ClipRetentionDaysOverride)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSetStreamRetentionOverrides_NotFound(t *testing.T) {
	s, mock, done := newRetentionServer(t)
	defer done()
	const tenant = "11111111-1111-1111-1111-111111111111"
	const stream = "22222222-2222-2222-2222-222222222222"

	mock.ExpectQuery(streamOwnerSQL).
		WithArgs(stream, tenant).
		WillReturnError(sql.ErrNoRows)

	_, err := s.SetStreamRetentionOverrides(retentionAuthCtx(tenant), &commodorepb.SetStreamRetentionOverridesRequest{
		StreamId:                 stream,
		DvrRetentionDaysOverride: i32ptr(7),
	})
	if err == nil {
		t.Fatal("expected NotFound for a stream the tenant does not own")
	}
}

func TestSetStreamRetentionOverrides_NoFields(t *testing.T) {
	s, mock, done := newRetentionServer(t)
	defer done()
	const tenant = "11111111-1111-1111-1111-111111111111"
	const stream = "22222222-2222-2222-2222-222222222222"

	// Ownership still checked; then no assignments → InvalidArgument (no UPDATE).
	expectStreamOwned(mock, stream, tenant)

	_, err := s.SetStreamRetentionOverrides(retentionAuthCtx(tenant), &commodorepb.SetStreamRetentionOverridesRequest{
		StreamId: stream,
	})
	if err == nil {
		t.Fatal("expected InvalidArgument when no override fields are set")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
