//go:build schema_verify

package grpc

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"
	"testing"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	media "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
)

func TestMediaAuthorityDVRChapterPolicyRefresh_RealPG(t *testing.T) {
	testDVRChapterPolicyRefresh(t, startCommodoreRealPG(t))
}

func TestMediaAuthorityDVRChapterPolicyRefresh_RealYugabyte(t *testing.T) {
	testDVRChapterPolicyRefresh(t, startPlacementDeliveryYugabyte(t, "dvr_chapter_policy"))
}

func testDVRChapterPolicyRefresh(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	seedClaimStream(t, db, "sk_dvr_chapter_policy")
	if _, err := db.ExecContext(ctx, `UPDATE commodore.streams SET dvr_chapter_interval_seconds=3600,is_recording_enabled=true
		WHERE tenant_id=$1::uuid AND id=$2::uuid`, claimTenantID, claimStreamID); err != nil {
		t.Fatal(err)
	}
	target := commodoredb.LiveStreamMediaAuthorityTarget(claimStreamID)
	q := commodoredb.New(db)
	read := func() obligationRow {
		t.Helper()
		var row obligationRow
		if err := db.QueryRowContext(ctx, `SELECT status,revision,COALESCE(lease_expires_at > NOW(), FALSE)
   FROM commodore.media_authority_refresh_obligations WHERE tenant_id=$1::uuid AND target_key=$2 AND lane='event'`, claimTenantID, target.Key).Scan(&row.status, &row.revision, &row.leased); err != nil {
			t.Fatal(err)
		}
		return row
	}
	update := func(statement string, value any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, statement, value, claimTenantID, claimStreamID); err != nil {
			t.Fatal(err)
		}
	}
	claim := func() commodoredb.ClaimMediaAuthorityObligationsRow {
		t.Helper()
		rows, err := q.ClaimMediaAuthorityObligations(ctx, commodoredb.ClaimMediaAuthorityObligationsParams{LeaseMs: 60000, Lane: commodoredb.MediaAuthorityLaneEvent, BatchSize: 8})
		if err != nil || len(rows) != 1 || rows[0].TargetKey != target.Key {
			t.Fatalf("claim = %+v, err=%v", rows, err)
		}
		return rows[0]
	}
	// The creation compile is already in flight when chapter policy changes.
	first := claim()
	update(`UPDATE commodore.streams SET dvr_chapter_mode=$1 WHERE tenant_id=$2::uuid AND id=$3::uuid`, "fixed_interval")
	afterMode := read()
	if afterMode.revision != first.Revision+1 || !afterMode.leased {
		t.Fatalf("mode-only update lost refresh during creation compile: revision=%d lease=%v, want revision=%d with lease retained", afterMode.revision, afterMode.leased, first.Revision+1)
	}
	update(`UPDATE commodore.streams SET dvr_chapter_interval_seconds=$1 WHERE tenant_id=$2::uuid AND id=$3::uuid`, 86400)
	afterInterval := read()
	if afterInterval.revision != afterMode.revision+1 || !afterInterval.leased {
		t.Fatalf("interval-only update lost refresh: before=%+v after=%+v", afterMode, afterInterval)
	}
	// Mentioning unchanged columns must not recompile or move the revision.
	update(`UPDATE commodore.streams SET dvr_chapter_interval_seconds=$1, is_recording_enabled=is_recording_enabled WHERE tenant_id=$2::uuid AND id=$3::uuid`, 86400)
	if unchanged := read(); unchanged.revision != afterInterval.revision {
		t.Fatalf("no-op changed authority: %+v", unchanged)
	}
	settled, err := q.CompleteMediaAuthorityObligation(ctx, commodoredb.CompleteMediaAuthorityObligationParams{TargetKey: target.Key, Lane: commodoredb.MediaAuthorityLaneEvent, Revision: first.Revision, ClaimToken: first.ClaimToken})
	if err != nil || settled != 0 {
		t.Fatalf("creation compile swallowed policy update: settled=%d err=%v", settled, err)
	}
	if _, err = q.ReleaseSupersededMediaAuthorityObligation(ctx, commodoredb.ReleaseSupersededMediaAuthorityObligationParams{TargetKey: target.Key, Lane: commodoredb.MediaAuthorityLaneEvent, Revision: first.Revision, ClaimToken: first.ClaimToken}); err != nil {
		t.Fatal(err)
	}
	if err = q.ReleaseMediaAuthorityTargetClaim(ctx, commodoredb.ReleaseMediaAuthorityTargetClaimParams{TargetKey: target.Key, Lane: commodoredb.MediaAuthorityLaneEvent, ClaimToken: first.ClaimToken}); err != nil {
		t.Fatal(err)
	}
	second := claim()
	if second.Revision != afterInterval.revision {
		t.Fatalf("next compile claimed revision %d, want %d", second.Revision, afterInterval.revision)
	}
	source, err := q.GetLiveStreamMediaAuthoritySource(ctx, claimStreamID)
	if err != nil {
		t.Fatal(err)
	}
	_, interval := streamDVRChapterPolicy(source.DvrChapterMode, source.DvrChapterIntervalSeconds)
	if interval != 86400 {
		t.Fatalf("next authority has interval %d, want 86400", interval)
	}
	// The trigger fix alone cannot refresh bundles missed before the upgrade.
	// The upgrade requeues recording-enabled streams through the same queue.
	migration, err := dbsql.Content.ReadFile("migrations/commodore/v0.3.16/expand/010_dvr_chapter_authority_refresh.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, string(migration)); err != nil {
		t.Fatal(err)
	}
	if repaired := read(); repaired.revision != second.Revision+1 || !repaired.leased {
		t.Fatalf("upgrade did not durably repair existing authority: %+v", repaired)
	}
	t.Run("publication cannot sign superseded chapter settings", func(t *testing.T) {
		s := &CommodoreServer{db: db, logger: logging.NewLogger(), mediaAuthorityKeyID: "chapter-race",
			mediaAuthorityPrivateKey: ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))}
		object := useStreamPayload("claim-internal")
		object.TenantId, object.UserId = claimTenantID, claimUserID
		live := object.GetLiveStream()
		live.StreamId, live.PublisherConfigurationVersion, live.RecordingEnabled = claimStreamID, 1, true
		live.DvrChapterMode, live.DvrChapterIntervalSeconds = "window_sized_chapters", 0
		id := sharedauthority.LiveStreamAuthorityID(claimStreamID)
		now := time.Now().UTC()
		revisions := []*media.AuthoritySourceRevision{{Service: "commodore", Revision: "creation-before-chapter-save"}}
		publish := func() error {
			return s.persistMediaObjectAuthority(withMediaAuthorityInUse(ctx, true), id, object, nil, revisions, now, now.Add(time.Hour))
		}
		readPublished := func() (int64, error) {
			row, err := q.GetCurrentMediaAuthorityPublication(ctx, commodoredb.GetCurrentMediaAuthorityPublicationParams{AuthorityKind: "media_object", AuthorityID: id})
			return row.AuthorityVersion, err
		}
		if err := publish(); !errors.Is(err, errMediaAuthorityCompileSuperseded) {
			t.Fatalf("creation compile published old chapter settings after save: %v", err)
		}
		if _, err := readPublished(); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("superseded first compile advanced publication: %v", err)
		}
		live.DvrChapterMode, live.DvrChapterIntervalSeconds = "fixed_interval", 86400
		if err := publish(); err != nil {
			t.Fatal(err)
		}
		version, err := readPublished()
		if err != nil {
			t.Fatal(err)
		}
		update(`UPDATE commodore.streams SET dvr_chapter_interval_seconds=$1 WHERE tenant_id=$2::uuid AND id=$3::uuid`, 7200)
		if err := publish(); !errors.Is(err, errMediaAuthorityCompileSuperseded) {
			t.Fatalf("an older compile replaced publication after a later save: %v", err)
		}
		if after, err := readPublished(); err != nil || after != version {
			t.Fatalf("superseded compile changed publication: before=%d after=%d err=%v", version, after, err)
		}
		live.DvrChapterIntervalSeconds = 7200
		if err := publish(); err != nil {
			t.Fatal(err)
		}
		if after, err := readPublished(); err != nil || after <= version {
			t.Fatalf("fresh compile did not advance publication: before=%d after=%d err=%v", version, after, err)
		}
	})
	t.Run("upgrade also repairs manual-recording policies", func(t *testing.T) {
		update(`UPDATE commodore.streams SET is_recording_enabled=$1 WHERE tenant_id=$2::uuid AND id=$3::uuid`, false)
		if _, err := q.ReleaseSupersededMediaAuthorityObligation(ctx, commodoredb.ReleaseSupersededMediaAuthorityObligationParams{
			TargetKey: target.Key, Lane: commodoredb.MediaAuthorityLaneEvent, Revision: second.Revision, ClaimToken: second.ClaimToken,
		}); err != nil {
			t.Fatal(err)
		}
		if err := q.ReleaseMediaAuthorityTargetClaim(ctx, commodoredb.ReleaseMediaAuthorityTargetClaimParams{
			TargetKey: target.Key, Lane: commodoredb.MediaAuthorityLaneEvent, ClaimToken: second.ClaimToken,
		}); err != nil {
			t.Fatal(err)
		}
		// A pre-upgrade compiler may have finished before the missed chapter edit;
		// a completed obligation must be rearmed even with auto-record disabled.
		if _, err := db.ExecContext(ctx, `UPDATE commodore.media_authority_refresh_obligations
   SET status='completed',lease_expires_at=NULL,claim_token=NULL
   WHERE tenant_id=$1::uuid AND target_key=$2 AND lane='event'`, claimTenantID, target.Key); err != nil {
			t.Fatal(err)
		}
		before := read()
		if _, err := db.ExecContext(ctx, string(migration)); err != nil {
			t.Fatal(err)
		}
		if repaired := read(); repaired.status != "pending" || repaired.revision != before.revision+1 {
			t.Fatalf("upgrade skipped manual-recording chapter policy: before=%+v after=%+v", before, repaired)
		}
	})
}
