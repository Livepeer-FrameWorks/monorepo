package grpc

import (
	"context"
	"errors"
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
			"keep_vod_originals"}).AddRow(nil, dvr, nil, "user-1", time.Unix(1_700_000_000, 0), nil))
}

func TestSetMediaRetentionPolicy_SetUnderCap(t *testing.T) {
	s, mock, done := newRetentionServer(t)
	defer done()
	const tenant = "11111111-1111-1111-1111-111111111111"

	// nil purser → cap 30; days=7 is under cap, so the write proceeds and its
	// event commits in the same transaction.
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO commodore\.tenant_media_retention_policies`).
		WithArgs(tenant, false, nil, true, int32(7), false, nil, "user-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectLegacyEventInsert(mock, eventRetentionPolicyChanged)
	mock.ExpectCommit()
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
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO commodore\.tenant_media_retention_policies`).
		WithArgs(tenant, false, nil, true, nil, false, nil, "user-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectLegacyEventInsert(mock, eventRetentionPolicyChanged)
	mock.ExpectCommit()
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

	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO commodore\.tenant_media_retention_policies`).
		WithArgs(tenant, false, nil, true, int32(7), false, nil, "user-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectLegacyEventInsert(mock, eventRetentionPolicyChanged)
	mock.ExpectCommit()
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

func TestRetentionOverrideWritersUseStreamsWrite(t *testing.T) {
	tests := []struct {
		name string
		call func(*CommodoreServer, context.Context) error
	}{
		{"stream", func(s *CommodoreServer, ctx context.Context) error {
			_, err := s.SetStreamRetentionOverrides(ctx, &commodorepb.SetStreamRetentionOverridesRequest{StreamId: "stream-1"})
			return err
		}},
	}
	for _, target := range []commodorepb.MediaRetentionTarget{tgtDVR, tgtVOD, tgtClip} {
		tests = append(tests, struct {
			name string
			call func(*CommodoreServer, context.Context) error
		}{"override/" + target.String(), func(s *CommodoreServer, ctx context.Context) error {
			_, err := s.UpdateAssetRetention(ctx, &commodorepb.UpdateAssetRetentionRequest{TargetType: target, TargetId: "asset-1"})
			return err
		}})
		tests = append(tests, struct {
			name string
			call func(*CommodoreServer, context.Context) error
		}{"reset/" + target.String(), func(s *CommodoreServer, ctx context.Context) error {
			_, err := s.ResetAssetRetention(ctx, &commodorepb.ResetAssetRetentionRequest{TargetType: target, TargetId: "asset-1"})
			return err
		}})
	}
	for _, scope := range []string{"streams:write", "billing:write"} {
		for _, role := range []string{"member", "owner"} {
			for _, test := range tests {
				t.Run(scope+"/"+role+"/"+test.name, func(t *testing.T) {
					server, mock, done := newRetentionServer(t)
					defer done()
					ctx := context.WithValue(retentionAuthCtx("tenant-a"), ctxkeys.KeyAuthType, "api_token")
					ctx = context.WithValue(ctx, ctxkeys.KeyRole, role)
					ctx = context.WithValue(ctx, ctxkeys.KeyPermissions, []string{scope})
					if scope == "streams:write" {
						mock.ExpectQuery("SELECT").WillReturnError(errors.New("catalog proof"))
					}
					err := test.call(server, ctx)
					if scope == "streams:write" {
						if status.Code(err) != codes.Internal {
							t.Fatalf("media token did not reach catalog: %v", err)
						}
					} else if status.Code(err) != codes.PermissionDenied {
						t.Fatalf("billing-only token authorized media write: %v", err)
					}
					if err := mock.ExpectationsWereMet(); err != nil {
						t.Fatal(err)
					}
				})
			}
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
