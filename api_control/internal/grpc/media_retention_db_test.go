package grpc

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/sirupsen/logrus"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

// newRetentionServer builds a CommodoreServer wired to a mock DB and a nil
// purserClient. With purserClient nil, fetchEntitlementBound returns the
// safe fallback cap (safeFallbackRetentionDays = 30), so every test here runs
// under a finite 30-day tier cap — which is exactly the clamp behavior we want
// to pin.
func newRetentionServer(t *testing.T) (*CommodoreServer, sqlmock.Sqlmock, func()) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	s := &CommodoreServer{db: db, logger: logrus.New()}
	return s, mock, func() { _ = db.Close() }
}

const (
	tgtVOD  = commodorepb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_VOD
	tgtDVR  = commodorepb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_DVR
	tgtClip = commodorepb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_CLIP
)

func TestReadTenantPerClassDefault(t *testing.T) {
	const tenant = "11111111-1111-1111-1111-111111111111"

	t.Run("value present", func(t *testing.T) {
		s, mock, done := newRetentionServer(t)
		defer done()
		mock.ExpectQuery(`SELECT default_dvr_retention_days[\s\S]*FROM commodore\.tenant_media_retention_policies`).
			WithArgs(tenant).
			WillReturnRows(sqlmock.NewRows([]string{"default_dvr_retention_days"}).AddRow(int32(14)))

		days, set, err := s.readTenantPerClassDefault(context.Background(), tenant, tgtDVR)
		if err != nil || !set || days != 14 {
			t.Fatalf("got (%d, %v, %v), want (14, true, nil)", days, set, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("NULL column means unset", func(t *testing.T) {
		s, mock, done := newRetentionServer(t)
		defer done()
		mock.ExpectQuery(`FROM commodore\.tenant_media_retention_policies`).
			WithArgs(tenant).
			WillReturnRows(sqlmock.NewRows([]string{"default_clip_retention_days"}).AddRow(nil))

		_, set, err := s.readTenantPerClassDefault(context.Background(), tenant, tgtClip)
		if err != nil || set {
			t.Fatalf("NULL must map to unset; got set=%v err=%v", set, err)
		}
	})

	t.Run("no policy row means unset", func(t *testing.T) {
		s, mock, done := newRetentionServer(t)
		defer done()
		mock.ExpectQuery(`FROM commodore\.tenant_media_retention_policies`).
			WithArgs(tenant).
			WillReturnError(sql.ErrNoRows)

		_, set, err := s.readTenantPerClassDefault(context.Background(), tenant, tgtVOD)
		if err != nil || set {
			t.Fatalf("ErrNoRows must map to (unset, nil); got set=%v err=%v", set, err)
		}
	})

	t.Run("UNSPECIFIED target does not query", func(t *testing.T) {
		s, _, done := newRetentionServer(t)
		defer done()
		// No ExpectQuery registered: any DB hit fails the test.
		_, set, err := s.readTenantPerClassDefault(context.Background(), tenant,
			commodorepb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_UNSPECIFIED)
		if err != nil || set {
			t.Fatalf("unspecified target must short-circuit to unset; got set=%v err=%v", set, err)
		}
	})
}

func TestReadStreamRetentionOverride(t *testing.T) {
	const tenant = "22222222-2222-2222-2222-222222222222"
	const stream = "33333333-3333-3333-3333-333333333333"

	t.Run("DVR override present", func(t *testing.T) {
		s, mock, done := newRetentionServer(t)
		defer done()
		mock.ExpectQuery(`FROM commodore\.streams WHERE id`).
			WithArgs(stream, tenant).
			WillReturnRows(sqlmock.NewRows([]string{"dvr_retention_days_override"}).AddRow(int32(7)))

		days, set, err := s.readStreamRetentionOverride(context.Background(), tenant, stream, tgtDVR)
		if err != nil || !set || days != 7 {
			t.Fatalf("got (%d, %v, %v), want (7, true, nil)", days, set, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("empty stream short-circuits without query", func(t *testing.T) {
		s, _, done := newRetentionServer(t)
		defer done()
		_, set, err := s.readStreamRetentionOverride(context.Background(), tenant, "", tgtDVR)
		if err != nil || set {
			t.Fatalf("empty streamID must short-circuit; got set=%v err=%v", set, err)
		}
	})

	t.Run("VOD has no per-stream column", func(t *testing.T) {
		s, _, done := newRetentionServer(t)
		defer done()
		// VOD uploads aren't stream-bound → no column → no query.
		_, set, err := s.readStreamRetentionOverride(context.Background(), tenant, stream, tgtVOD)
		if err != nil || set {
			t.Fatalf("VOD must short-circuit to unset; got set=%v err=%v", set, err)
		}
	})
}

// TestResolveInitialRetention_Cascade pins the resolution precedence:
// per-stream override > tenant per-class default > system default, then the
// tier cap clamps. purserClient is nil so the cap is the 30-day safe fallback.
func TestResolveInitialRetention_Cascade(t *testing.T) {
	const tenant = "44444444-4444-4444-4444-444444444444"
	const stream = "55555555-5555-5555-5555-555555555555"

	t.Run("stream override wins and short-circuits", func(t *testing.T) {
		s, mock, done := newRetentionServer(t)
		defer done()
		// Only the stream-override read should fire; tenant default must not.
		mock.ExpectQuery(`FROM commodore\.streams WHERE id`).
			WithArgs(stream, tenant).
			WillReturnRows(sqlmock.NewRows([]string{"dvr_retention_days_override"}).AddRow(int32(7)))

		got, err := s.resolveInitialRetention(context.Background(), tgtDVR, tenant, stream)
		if err != nil || got != 7 {
			t.Fatalf("got (%d, %v), want (7, nil)", got, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("falls through to tenant default", func(t *testing.T) {
		s, mock, done := newRetentionServer(t)
		defer done()
		mock.ExpectQuery(`FROM commodore\.streams WHERE id`).
			WithArgs(stream, tenant).
			WillReturnRows(sqlmock.NewRows([]string{"dvr_retention_days_override"}).AddRow(nil))
		mock.ExpectQuery(`FROM commodore\.tenant_media_retention_policies`).
			WithArgs(tenant).
			WillReturnRows(sqlmock.NewRows([]string{"default_dvr_retention_days"}).AddRow(int32(20)))

		got, err := s.resolveInitialRetention(context.Background(), tgtDVR, tenant, stream)
		if err != nil || got != 20 {
			t.Fatalf("got (%d, %v), want (20, nil)", got, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("falls through to system default then clamps to cap", func(t *testing.T) {
		s, mock, done := newRetentionServer(t)
		defer done()
		// Both reads unset → DVR system default 30, cap 30 → 30.
		mock.ExpectQuery(`FROM commodore\.streams WHERE id`).
			WithArgs(stream, tenant).WillReturnError(sql.ErrNoRows)
		mock.ExpectQuery(`FROM commodore\.tenant_media_retention_policies`).
			WithArgs(tenant).WillReturnError(sql.ErrNoRows)

		got, err := s.resolveInitialRetention(context.Background(), tgtDVR, tenant, stream)
		if err != nil || got != 30 {
			t.Fatalf("got (%d, %v), want (30, nil)", got, err)
		}
	})

	t.Run("over-cap tenant default is clamped down", func(t *testing.T) {
		s, mock, done := newRetentionServer(t)
		defer done()
		mock.ExpectQuery(`FROM commodore\.streams WHERE id`).
			WithArgs(stream, tenant).
			WillReturnRows(sqlmock.NewRows([]string{"dvr_retention_days_override"}).AddRow(nil))
		mock.ExpectQuery(`FROM commodore\.tenant_media_retention_policies`).
			WithArgs(tenant).
			WillReturnRows(sqlmock.NewRows([]string{"default_dvr_retention_days"}).AddRow(int32(365)))

		got, err := s.resolveInitialRetention(context.Background(), tgtDVR, tenant, stream)
		if err != nil || got != 30 {
			t.Fatalf("365 should clamp to cap 30; got (%d, %v)", got, err)
		}
	})

	t.Run("VOD forever clamps to cap under nil-purser fallback", func(t *testing.T) {
		s, mock, done := newRetentionServer(t)
		defer done()
		// VOD has no stream column → stream read short-circuits (no query).
		// Tenant default unset → VOD system default 0 (forever) → clamp to 30.
		mock.ExpectQuery(`FROM commodore\.tenant_media_retention_policies`).
			WithArgs(tenant).WillReturnError(sql.ErrNoRows)

		got, err := s.resolveInitialRetention(context.Background(), tgtVOD, tenant, stream)
		if err != nil || got != 30 {
			t.Fatalf("VOD forever should clamp to cap 30; got (%d, %v)", got, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestGetMediaRetentionPolicy(t *testing.T) {
	const tenant = "66666666-6666-6666-6666-666666666666"
	authCtx := func() context.Context {
		ctx := context.WithValue(context.Background(), ctxkeys.KeyUserID, "user-1")
		return context.WithValue(ctx, ctxkeys.KeyTenantID, tenant)
	}

	t.Run("no policy row yields system-default effectives clamped to cap", func(t *testing.T) {
		s, mock, done := newRetentionServer(t)
		defer done()
		mock.ExpectQuery(`COALESCE\(updated_by`).
			WithArgs(tenant).
			WillReturnError(sql.ErrNoRows)

		resp, err := s.GetMediaRetentionPolicy(authCtx(), &commodorepb.GetMediaRetentionPolicyRequest{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// nil purser → cap 30. VOD forever(0)→30, DVR 30, CLIP 30.
		if resp.GetBounds().GetMaxRecordingRetentionDays() != 30 {
			t.Errorf("bound = %d, want 30", resp.GetBounds().GetMaxRecordingRetentionDays())
		}
		if resp.GetEffectiveVodRetentionDays() != 30 || resp.GetEffectiveDvrRetentionDays() != 30 ||
			resp.GetEffectiveClipRetentionDays() != 30 {
			t.Errorf("effectives = (vod %d, dvr %d, clip %d), want all 30",
				resp.GetEffectiveVodRetentionDays(), resp.GetEffectiveDvrRetentionDays(), resp.GetEffectiveClipRetentionDays())
		}
		// No explicit per-class defaults set → pointers stay nil.
		if resp.DefaultVodRetentionDays != nil || resp.DefaultDvrRetentionDays != nil || resp.DefaultClipRetentionDays != nil {
			t.Error("absent policy must leave per-class default pointers nil")
		}
	})

	t.Run("explicit defaults surface and clamp", func(t *testing.T) {
		s, mock, done := newRetentionServer(t)
		defer done()
		// vod=0 (forever), dvr=7, clip=NULL.
		mock.ExpectQuery(`COALESCE\(updated_by`).
			WithArgs(tenant).
			WillReturnRows(sqlmock.NewRows([]string{
				"default_vod_retention_days", "default_dvr_retention_days",
				"default_clip_retention_days", "updated_by", "updated_at",
			}).AddRow(int32(0), int32(7), nil, "admin", time.Unix(1_700_000_000, 0)))

		resp, err := s.GetMediaRetentionPolicy(authCtx(), &commodorepb.GetMediaRetentionPolicyRequest{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// VOD explicit 0 (forever) clamps to cap 30; DVR 7 stays; CLIP unset→30.
		if resp.GetEffectiveVodRetentionDays() != 30 {
			t.Errorf("vod effective = %d, want 30 (forever clamped)", resp.GetEffectiveVodRetentionDays())
		}
		if resp.GetEffectiveDvrRetentionDays() != 7 {
			t.Errorf("dvr effective = %d, want 7", resp.GetEffectiveDvrRetentionDays())
		}
		if resp.GetEffectiveClipRetentionDays() != 30 {
			t.Errorf("clip effective = %d, want 30 (unset→system default→cap)", resp.GetEffectiveClipRetentionDays())
		}
		if resp.DefaultVodRetentionDays == nil || *resp.DefaultVodRetentionDays != 0 {
			t.Error("explicit vod=0 should surface as a non-nil 0 pointer")
		}
		if resp.DefaultClipRetentionDays != nil {
			t.Error("NULL clip default must stay nil")
		}
		if resp.GetUpdatedBy() != "admin" {
			t.Errorf("updated_by = %q, want admin", resp.GetUpdatedBy())
		}
	})

	t.Run("missing user context is rejected", func(t *testing.T) {
		s, _, done := newRetentionServer(t)
		defer done()
		_, err := s.GetMediaRetentionPolicy(context.Background(), &commodorepb.GetMediaRetentionPolicyRequest{})
		if err == nil {
			t.Fatal("expected Unauthenticated without user context")
		}
	})
}
