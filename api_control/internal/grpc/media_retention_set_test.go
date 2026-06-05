package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

func retentionAuthCtx(tenant string) context.Context {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyUserID, "user-1")
	return context.WithValue(ctx, ctxkeys.KeyTenantID, tenant)
}

// policyReadbackRow is the row GetMediaRetentionPolicy reads after a write.
func expectPolicyReadback(mock sqlmock.Sqlmock, tenant string, dvr int32) {
	mock.ExpectQuery(`COALESCE\(updated_by`).
		WithArgs(tenant).
		WillReturnRows(sqlmock.NewRows([]string{
			"default_vod_retention_days", "default_dvr_retention_days",
			"default_clip_retention_days", "updated_by", "updated_at",
		}).AddRow(nil, dvr, nil, "user-1", time.Unix(1_700_000_000, 0)))
}

func TestSetMediaRetentionPolicy_SetUnderCap(t *testing.T) {
	s, mock, done := newRetentionServer(t)
	defer done()
	const tenant = "11111111-1111-1111-1111-111111111111"

	// nil purser → cap 30; days=7 is under cap, so the write proceeds.
	mock.ExpectExec(`INSERT INTO commodore\.tenant_media_retention_policies`).
		WithArgs(tenant, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	// Best-effort outbox enqueue (RETURNING id → QueryRow).
	mock.ExpectQuery(`INSERT INTO commodore\.service_event_outbox`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("evt-1"))
	// Final GetMediaRetentionPolicy re-read.
	expectPolicyReadback(mock, tenant, 7)

	resp, err := s.SetMediaRetentionPolicy(retentionAuthCtx(tenant), &commodorepb.SetMediaRetentionPolicyRequest{
		TargetType: tgtDVR,
		Days:       7,
	})
	if err != nil {
		t.Fatalf("SetMediaRetentionPolicy: %v", err)
	}
	if resp.GetPolicy().GetEffectiveDvrRetentionDays() != 7 {
		t.Errorf("effective dvr = %d, want 7", resp.GetPolicy().GetEffectiveDvrRetentionDays())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSetMediaRetentionPolicy_Clear(t *testing.T) {
	s, mock, done := newRetentionServer(t)
	defer done()
	const tenant = "22222222-2222-2222-2222-222222222222"

	// clear=true → no entitlement lookup, NULL upsert.
	mock.ExpectExec(`INSERT INTO commodore\.tenant_media_retention_policies`).
		WithArgs(tenant, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`INSERT INTO commodore\.service_event_outbox`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("evt-2"))
	expectPolicyReadback(mock, tenant, 30)

	_, err := s.SetMediaRetentionPolicy(retentionAuthCtx(tenant), &commodorepb.SetMediaRetentionPolicyRequest{
		TargetType: tgtDVR,
		Clear:      true,
	})
	if err != nil {
		t.Fatalf("SetMediaRetentionPolicy clear: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSetMediaRetentionPolicy_RejectsOverCap(t *testing.T) {
	s, _, done := newRetentionServer(t)
	defer done()
	const tenant = "33333333-3333-3333-3333-333333333333"
	// nil purser → cap 30; days=90 exceeds it → InvalidArgument, no DB writes
	// (no mock expectations registered).
	_, err := s.SetMediaRetentionPolicy(retentionAuthCtx(tenant), &commodorepb.SetMediaRetentionPolicyRequest{
		TargetType: tgtDVR,
		Days:       90,
	})
	if err == nil {
		t.Fatal("expected InvalidArgument when days exceeds tier cap")
	}
}

func TestSetMediaRetentionPolicy_RejectsTenantMismatch(t *testing.T) {
	s, _, done := newRetentionServer(t)
	defer done()
	_, err := s.SetMediaRetentionPolicy(retentionAuthCtx("tenant-a"), &commodorepb.SetMediaRetentionPolicyRequest{
		TenantId:   "tenant-b",
		TargetType: tgtDVR,
		Days:       7,
	})
	if err == nil {
		t.Fatal("expected PermissionDenied on tenant mismatch")
	}
}

func TestSetMediaRetentionPolicy_RejectsUnspecifiedTarget(t *testing.T) {
	s, _, done := newRetentionServer(t)
	defer done()
	_, err := s.SetMediaRetentionPolicy(retentionAuthCtx("tenant-a"), &commodorepb.SetMediaRetentionPolicyRequest{
		TargetType: commodorepb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_UNSPECIFIED,
		Days:       7,
	})
	if err == nil {
		t.Fatal("expected InvalidArgument for unspecified target_type")
	}
}
