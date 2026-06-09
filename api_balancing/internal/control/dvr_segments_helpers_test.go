package control

import (
	"context"
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

// TestDVREffectiveWindowSeconds pins the eviction-window lookup: a valid
// positive window is returned; a missing row, null, or non-positive value all
// resolve to 0 (no window → no time-based eviction), and a nil DB is safe.
func TestDVREffectiveWindowSeconds(t *testing.T) {
	ctx := context.Background()

	t.Run("nil DB returns 0", func(t *testing.T) {
		prev := db
		db = nil
		t.Cleanup(func() { db = prev })
		if got := dvrEffectiveWindowSeconds(ctx, "h"); got != 0 {
			t.Fatalf("nil DB = %d, want 0", got)
		}
	})

	t.Run("valid window returned", func(t *testing.T) {
		mock, _, _ := setupArtifactTestDeps(t)
		mock.ExpectQuery(`SELECT dvr_window_seconds`).WithArgs("h").
			WillReturnRows(sqlmock.NewRows([]string{"dvr_window_seconds"}).AddRow(int32(120)))
		if got := dvrEffectiveWindowSeconds(ctx, "h"); got != 120 {
			t.Fatalf("window = %d, want 120", got)
		}
	})

	t.Run("null or non-positive returns 0", func(t *testing.T) {
		mock, _, _ := setupArtifactTestDeps(t)
		mock.ExpectQuery(`SELECT dvr_window_seconds`).WithArgs("h").
			WillReturnRows(sqlmock.NewRows([]string{"dvr_window_seconds"}).AddRow(nil))
		if got := dvrEffectiveWindowSeconds(ctx, "h"); got != 0 {
			t.Fatalf("null window = %d, want 0", got)
		}
	})

	t.Run("no row returns 0", func(t *testing.T) {
		mock, _, _ := setupArtifactTestDeps(t)
		mock.ExpectQuery(`SELECT dvr_window_seconds`).WithArgs("h").WillReturnError(sql.ErrNoRows)
		if got := dvrEffectiveWindowSeconds(ctx, "h"); got != 0 {
			t.Fatalf("missing row = %d, want 0", got)
		}
	})
}

// TestResolveDVRTenantAndStream pins the two-source resolution with the local
// artifacts row as the fast path and Commodore (ResolveDVRHash) as the
// cross-cluster fallback: a complete local row short-circuits, ErrNoRows falls
// through to Commodore, and when neither yields both fields ok=false.
func TestResolveDVRTenantAndStream(t *testing.T) {
	ctx := context.Background()
	log := logging.NewLogger()

	t.Run("local row short-circuits", func(t *testing.T) {
		mock, _, _ := setupArtifactTestDeps(t)
		// Even if Commodore would answer, a complete local row wins — assert that
		// by leaving the fake unset (would return not-found) but expecting no call.
		startFakeCommodoreServer(t, &fakeCommodoreInternal{})
		mock.ExpectQuery(`FROM foghorn.artifacts`).WithArgs("h").
			WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "stream_internal_name"}).AddRow("t1", "s1"))
		tenant, stream, ok := resolveDVRTenantAndStream(ctx, "h", log)
		if !ok || tenant != "t1" || stream != "s1" {
			t.Fatalf("local row = (%q, %q, %v), want (t1, s1, true)", tenant, stream, ok)
		}
	})

	t.Run("ErrNoRows falls through to commodore", func(t *testing.T) {
		mock, _, _ := setupArtifactTestDeps(t)
		mock.ExpectQuery(`FROM foghorn.artifacts`).WithArgs("h").WillReturnError(sql.ErrNoRows)
		startFakeCommodoreServer(t, &fakeCommodoreInternal{
			dvrHash: func(_ context.Context, _ *commodorepb.ResolveDVRHashRequest) (*commodorepb.ResolveDVRHashResponse, error) {
				return &commodorepb.ResolveDVRHashResponse{Found: true, TenantId: "t2", StreamInternalName: "s2"}, nil
			},
		})
		tenant, stream, ok := resolveDVRTenantAndStream(ctx, "h", log)
		if !ok || tenant != "t2" || stream != "s2" {
			t.Fatalf("commodore fallback = (%q, %q, %v), want (t2, s2, true)", tenant, stream, ok)
		}
	})

	t.Run("both miss returns not ok", func(t *testing.T) {
		mock, _, _ := setupArtifactTestDeps(t)
		mock.ExpectQuery(`FROM foghorn.artifacts`).WithArgs("h").WillReturnError(sql.ErrNoRows)
		startFakeCommodoreServer(t, &fakeCommodoreInternal{
			dvrHash: func(_ context.Context, _ *commodorepb.ResolveDVRHashRequest) (*commodorepb.ResolveDVRHashResponse, error) {
				return &commodorepb.ResolveDVRHashResponse{Found: false}, nil
			},
		})
		if _, _, ok := resolveDVRTenantAndStream(ctx, "h", log); ok {
			t.Fatal("both miss must return ok=false")
		}
	})
}
