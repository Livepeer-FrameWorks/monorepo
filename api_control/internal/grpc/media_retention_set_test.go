package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func retentionAuthCtx(tenant string) context.Context {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyUserID, "user-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, tenant)
	ctx = context.WithValue(ctx, ctxkeys.KeyAuthType, "jwt")
	return context.WithValue(ctx, ctxkeys.KeyRole, "owner")
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
		WithArgs(tenant, false, nil, true, int32(7), false, nil, "user-1").
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
		WithArgs(tenant, false, nil, true, nil, false, nil, "user-1").
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

func TestSetMediaRetentionPolicy_RejectsTenantMember(t *testing.T) {
	s, _, done := newRetentionServer(t)
	defer done()
	ctx := context.WithValue(retentionAuthCtx("tenant-a"), ctxkeys.KeyRole, "member")

	_, err := s.SetMediaRetentionPolicy(ctx, &commodorepb.SetMediaRetentionPolicyRequest{
		TargetType: tgtDVR,
		Days:       7,
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("status = %v, want PermissionDenied", status.Code(err))
	}
}

func TestSetMediaRetentionPolicy_RejectsOwnerAPITokenWithoutBillingWrite(t *testing.T) {
	s, _, done := newRetentionServer(t)
	defer done()
	ctx := context.WithValue(retentionAuthCtx("tenant-a"), ctxkeys.KeyAuthType, "api_token")
	ctx = context.WithValue(ctx, ctxkeys.KeyPermissions, []string{"billing:read"})

	_, err := s.SetMediaRetentionPolicy(ctx, &commodorepb.SetMediaRetentionPolicyRequest{
		TargetType: tgtDVR,
		Days:       7,
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("status = %v, want PermissionDenied", status.Code(err))
	}
}

func TestSetMediaRetentionPolicy_AcceptsOwnerAPITokenWithBillingWrite(t *testing.T) {
	s, mock, done := newRetentionServer(t)
	defer done()
	const tenant = "44444444-4444-4444-4444-444444444444"
	ctx := context.WithValue(retentionAuthCtx(tenant), ctxkeys.KeyAuthType, "api_token")
	ctx = context.WithValue(ctx, ctxkeys.KeyPermissions, []string{"billing:write"})

	mock.ExpectExec(`INSERT INTO commodore\.tenant_media_retention_policies`).
		WithArgs(tenant, false, nil, true, int32(7), false, nil, "user-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`INSERT INTO commodore\.service_event_outbox`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("evt-3"))
	expectPolicyReadback(mock, tenant, 7)

	_, err := s.SetMediaRetentionPolicy(ctx, &commodorepb.SetMediaRetentionPolicyRequest{
		TargetType: tgtDVR,
		Days:       7,
	})
	if err != nil {
		t.Fatalf("SetMediaRetentionPolicy: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRetentionOverrideWritersRequireBillingManagement(t *testing.T) {
	tests := []struct {
		name string
		call func(*CommodoreServer, context.Context) error
	}{
		{
			name: "stream overrides",
			call: func(s *CommodoreServer, ctx context.Context) error {
				_, err := s.SetStreamRetentionOverrides(ctx, &commodorepb.SetStreamRetentionOverridesRequest{StreamId: "stream-1"})
				return err
			},
		},
		{
			name: "asset override",
			call: func(s *CommodoreServer, ctx context.Context) error {
				_, err := s.UpdateAssetRetention(ctx, &commodorepb.UpdateAssetRetentionRequest{TargetType: tgtDVR, TargetId: "dvr-1"})
				return err
			},
		},
		{
			name: "asset reset",
			call: func(s *CommodoreServer, ctx context.Context) error {
				_, err := s.ResetAssetRetention(ctx, &commodorepb.ResetAssetRetentionRequest{TargetType: tgtDVR, TargetId: "dvr-1"})
				return err
			},
		},
	}
	actors := []struct {
		name string
		ctx  context.Context
	}{
		{
			name: "tenant member",
			ctx:  context.WithValue(retentionAuthCtx("tenant-a"), ctxkeys.KeyRole, "member"),
		},
		{
			name: "owner token without billing write",
			ctx: context.WithValue(
				context.WithValue(retentionAuthCtx("tenant-a"), ctxkeys.KeyAuthType, "api_token"),
				ctxkeys.KeyPermissions,
				[]string{"streams:write"},
			),
		},
	}
	for _, actor := range actors {
		for _, tt := range tests {
			t.Run(actor.name+"/"+tt.name, func(t *testing.T) {
				s, _, done := newRetentionServer(t)
				defer done()
				if err := tt.call(s, actor.ctx); status.Code(err) != codes.PermissionDenied {
					t.Fatalf("status = %v, want PermissionDenied", status.Code(err))
				}
			})
		}
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
