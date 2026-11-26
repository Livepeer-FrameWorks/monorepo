//go:build schema_verify

package jobs

import (
	"context"
	"database/sql"
	"frameworks/api_balancing/internal/database/foghorndb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
	"testing"
	"time"
)

type committedRecoveryFixture struct{ cutoff time.Time }

func (f committedRecoveryFixture) GetMediaRecoveryStatus(context.Context, string) (*purserpb.MediaRecoveryStatus, error) {
	return &purserpb.MediaRecoveryStatus{CommittedCutoffAt: timestamppb.New(f.cutoff)}, nil
}
func (f committedRecoveryFixture) CommitMediaRecoveryExpiry(context.Context, *purserpb.CommitMediaRecoveryExpiryRequest) (*purserpb.MediaRecoveryStatus, error) {
	panic("already committed")
}
func TestMediaRecoveryExpiryOwnership_RealPG(t *testing.T) {
	testMediaRecoveryExpiryOwnership(t, startRealPGForCleanup(t))
}
func TestMediaRecoveryExpiryOwnership_RealYugabyte(t *testing.T) {
	db, ok := dockerpg.OpenSharedYugabyteBaseline(t, "media_recovery", "foghorn")
	if !ok {
		t.Skip("requires shared Yugabyte fixture")
	}
	testMediaRecoveryExpiryOwnership(t, db)
}
func testMediaRecoveryExpiryOwnership(t *testing.T, db *sql.DB) {
	tenant, other := uuid.NewString(), uuid.NewString()
	cutoff := time.Now().UTC().Add(-time.Hour)
	for _, row := range []struct {
		hash, owner, status string
		old, warm           bool
	}{
		{"recovery-old", tenant, "ready", true, false}, {"recovery-new", tenant, "ready", false, false},
		{"recovery-young", tenant, "ready", false, false}, {"recovery-foreign", other, "ready", true, false}, {"recovery-warm", tenant, "ready", true, true}, {"recovery-active", tenant, "processing", true, false},
	} {
		created := time.Now().UTC()
		if row.old {
			created = time.Now().UTC().Add(-40 * 24 * time.Hour)
		}
		if row.hash == "recovery-young" {
			created = cutoff.Add(-time.Hour)
		}
		if _, err := db.Exec(`INSERT INTO foghorn.artifacts(artifact_hash,tenant_id,artifact_type,status,size_bytes,created_at,federated_pointer)VALUES($1,$2,'vod',$3,1,$4,$5)`, row.hash, row.owner, row.status, created, row.warm); err != nil {
			t.Fatal(err)
		}
	}
	j := NewRetentionJob(RetentionConfig{DB: db, Logger: logging.NewLogger(), RecoveryClient: committedRecoveryFixture{cutoff: cutoff}})
	j.expireMediaRecoveryTenant(t.Context(), tenant)
	for _, hash := range []string{"recovery-old", "recovery-new", "recovery-young", "recovery-foreign", "recovery-warm", "recovery-active"} {
		var state string
		if err := db.QueryRow(`SELECT status FROM foghorn.artifacts WHERE artifact_hash=$1`, hash).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if (state == "deleted") != (hash == "recovery-old") {
			t.Fatalf("wrong recovery expiry for %s: %s", hash, state)
		}
	}
	// The same cutoff cannot delete newly created media or another tenant even if an old worker retries directly.
	q := foghorndb.New(db)
	if _, err := q.ExpireMediaRecoveryArtifact(t.Context(), foghorndb.ExpireMediaRecoveryArtifactParams{TenantID: tenant, ArtifactHash: "recovery-foreign", CutoffAt: cutoff}); err != sql.ErrNoRows {
		t.Fatalf("foreign tenant was expired: %v", err)
	}
}
