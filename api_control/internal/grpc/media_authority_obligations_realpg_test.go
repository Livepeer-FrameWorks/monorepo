//go:build schema_verify

package grpc

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"testing"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	clusterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const obligationTenantID = "70000000-0000-0000-0000-000000000001"

func TestMediaAuthorityObligationQueue_RealPG(t *testing.T) {
	testMediaAuthorityObligationQueue(t, startCommodoreRealPG(t))
}

func TestMediaAuthorityObligationQueue_RealYugabyte(t *testing.T) {
	testMediaAuthorityObligationQueue(t, startPlacementDeliveryYugabyte(t, "authority_obligations"))
}

func TestMediaAuthorityPublishesOnlyOnChange_RealPG(t *testing.T) {
	testMediaAuthorityPublishesOnlyOnChange(t, startCommodoreRealPG(t))
}

func TestMediaAuthorityPublishesOnlyOnChange_RealYugabyte(t *testing.T) {
	testMediaAuthorityPublishesOnlyOnChange(t, startPlacementDeliveryYugabyte(t, "authority_publish"))
}

type obligationRow struct {
	status     string
	revision   int64
	attempts   int32
	parkReason sql.NullString
	leased     bool
}

func readObligation(t *testing.T, ctx context.Context, db *sql.DB, lane, targetKey string) (obligationRow, bool) {
	t.Helper()
	var row obligationRow
	err := db.QueryRowContext(ctx, `
		SELECT status, revision, attempts, park_reason, COALESCE(lease_expires_at > NOW(), FALSE)
		FROM commodore.media_authority_refresh_obligations WHERE lane = $1 AND target_key = $2`, lane, targetKey).
		Scan(&row.status, &row.revision, &row.attempts, &row.parkReason, &row.leased)
	if err == sql.ErrNoRows {
		return obligationRow{}, false
	}
	if err != nil {
		t.Fatalf("read obligation %s/%s: %v", lane, targetKey, err)
	}
	return row, true
}

func testMediaAuthorityObligationQueue(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	queries := commodoredb.New(db)
	stream := commodoredb.LiveStreamMediaAuthorityTarget("71000000-0000-0000-0000-000000000001")
	claim := func(lane string) []commodoredb.ClaimMediaAuthorityObligationsRow {
		t.Helper()
		rows, err := queries.ClaimMediaAuthorityObligations(ctx, commodoredb.ClaimMediaAuthorityObligationsParams{LeaseMs: 60_000, Lane: lane, BatchSize: 8})
		if err != nil {
			t.Fatalf("claim %s: %v", lane, err)
		}
		return rows
	}
	enqueue := func(eventID string) {
		t.Helper()
		if err := queries.EnqueueMediaAuthorityEvent(ctx, stream, obligationTenantID, "media_object:live_stream:"+stream.ObjectID()+":stream_changed", "commodore", eventID); err != nil {
			t.Fatalf("enqueue %s: %v", eventID, err)
		}
	}
	// Settlement in the worker ends by releasing the lane's claim on the target;
	// this test settles through the queries, so it releases the same way.
	releaseClaim := func(targetKey, lane, token string) {
		t.Helper()
		if err := queries.ReleaseMediaAuthorityTargetClaim(ctx, commodoredb.ReleaseMediaAuthorityTargetClaimParams{TargetKey: targetKey, Lane: lane, ClaimToken: token}); err != nil {
			t.Fatal(err)
		}
	}

	// Any number of events for one target is one unit of work.
	for _, eventID := range []string{"e1", "e2", "e3", "e4", "e5"} {
		enqueue(eventID)
	}
	var rows int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM commodore.media_authority_refresh_obligations`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("obligation rows = %d (err %v), want 1 for five events on one target", rows, err)
	}
	claimed := claim(commodoredb.MediaAuthorityLaneEvent)
	if len(claimed) != 1 || claimed[0].Revision != 5 || claimed[0].Attempts != 1 {
		t.Fatalf("claimed = %+v, want one row at revision 5, attempt 1", claimed)
	}
	if again := claim(commodoredb.MediaAuthorityLaneEvent); len(again) != 0 {
		t.Fatalf("a leased obligation was claimed twice: %+v", again)
	}

	// A release that arrives late must not clear a lease it does not own: the
	// first worker's lease ran out, a second worker claimed the newer revision,
	// and only then does the first worker get round to releasing. The stream
	// target above stays leased throughout, so these claims see only this target.
	var (
		settled int64
		err     error
	)
	late := commodoredb.LiveStreamMediaAuthorityTarget("71000000-0000-0000-0000-000000000002")
	enqueueLate := func(eventID string) {
		t.Helper()
		if enqueueErr := queries.EnqueueMediaAuthorityEvent(ctx, late, obligationTenantID, "media_object:live_stream:"+late.ObjectID()+":stream_changed", "commodore", eventID); enqueueErr != nil {
			t.Fatalf("enqueue %s: %v", eventID, enqueueErr)
		}
	}
	claimLate := func() commodoredb.ClaimMediaAuthorityObligationsRow {
		t.Helper()
		for _, row := range claim(commodoredb.MediaAuthorityLaneEvent) {
			if row.TargetKey == late.Key {
				return row
			}
		}
		t.Fatal("late-release target was not claimable")
		return commodoredb.ClaimMediaAuthorityObligationsRow{}
	}
	enqueueLate("l1")
	first := claimLate()
	enqueueLate("l2")
	if _, err = db.ExecContext(ctx, `UPDATE commodore.media_authority_refresh_obligations SET lease_expires_at = NOW() - INTERVAL '1 second' WHERE target_key = $1`, late.Key); err != nil {
		t.Fatal(err)
	}
	second := claimLate()
	if released, releaseErr := queries.ReleaseSupersededMediaAuthorityObligation(ctx, commodoredb.ReleaseSupersededMediaAuthorityObligationParams{TargetKey: late.Key, Lane: commodoredb.MediaAuthorityLaneEvent, Revision: first.Revision, ClaimToken: first.ClaimToken}); releaseErr != nil || released != 0 {
		t.Fatalf("late release cleared %d rows (err %v); the lease belongs to the second worker", released, releaseErr)
	}
	if row, _ := readObligation(t, ctx, db, commodoredb.MediaAuthorityLaneEvent, late.Key); row.status != "processing" || !row.leased {
		t.Fatalf("second worker's claim after a late release: %+v, want processing with a live lease", row)
	}
	if settled, err = queries.CompleteMediaAuthorityObligation(ctx, commodoredb.CompleteMediaAuthorityObligationParams{TargetKey: late.Key, Lane: commodoredb.MediaAuthorityLaneEvent, Revision: second.Revision, ClaimToken: second.ClaimToken}); err != nil || settled != 1 {
		t.Fatalf("complete second claim settled %d rows (err %v)", settled, err)
	}

	// A waiting event keeps its place in the queue when the target changes again;
	// a renewal's schedule is replaced by the newer one.
	enqueueLate("l3")
	if _, err = db.ExecContext(ctx, `UPDATE commodore.media_authority_refresh_obligations SET next_attempt_at = NOW() - INTERVAL '10 minutes' WHERE target_key = $1 AND lane = 'event'`, late.Key); err != nil {
		t.Fatal(err)
	}
	enqueueLate("l4")
	var waitedSeconds float64
	if err = db.QueryRowContext(ctx, `SELECT EXTRACT(EPOCH FROM (NOW() - next_attempt_at)) FROM commodore.media_authority_refresh_obligations WHERE target_key = $1 AND lane = 'event'`, late.Key).Scan(&waitedSeconds); err != nil {
		t.Fatal(err)
	}
	if waitedSeconds < 590 {
		t.Fatalf("a changed target lost its place: due %.0fs ago, want the original ten minutes", waitedSeconds)
	}
	for _, renewAt := range []time.Duration{-time.Hour, time.Hour} {
		if err = queries.ScheduleMediaAuthorityRenewal(ctx, late, obligationTenantID, 1, time.Now().Add(renewAt)); err != nil {
			t.Fatal(err)
		}
	}
	if err = db.QueryRowContext(ctx, `SELECT EXTRACT(EPOCH FROM (NOW() - next_attempt_at)) FROM commodore.media_authority_refresh_obligations WHERE target_key = $1 AND lane = 'object_deadline'`, late.Key).Scan(&waitedSeconds); err != nil {
		t.Fatal(err)
	}
	if waitedSeconds > -3500 {
		t.Fatalf("a rescheduled renewal kept its old due time: due in %.0fs, want about an hour", -waitedSeconds)
	}
	keptPlace := claimLate()
	if settled, err = queries.CompleteMediaAuthorityObligation(ctx, commodoredb.CompleteMediaAuthorityObligationParams{TargetKey: late.Key, Lane: commodoredb.MediaAuthorityLaneEvent, Revision: keptPlace.Revision, ClaimToken: keptPlace.ClaimToken}); err != nil || settled != 1 {
		t.Fatalf("complete kept-place claim settled %d rows (err %v)", settled, err)
	}
	// A renewal of the same authority must wait for the in-flight event compile.
	if err := queries.ScheduleMediaAuthorityRenewal(ctx, stream, obligationTenantID, 1, time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if renewal := claim(commodoredb.MediaAuthorityLaneObjectDeadline); len(renewal) != 0 {
		t.Fatalf("renewal claimed while the event lane compiles the same target: %+v", renewal)
	}

	// An event that arrives mid-compile must survive that compile's completion.
	// It flips the row to pending, and a second one must not drop the lease:
	// the compile is still running, so the renewal keeps waiting.
	enqueue("e6a")
	enqueue("e6")
	if renewal := claim(commodoredb.MediaAuthorityLaneObjectDeadline); len(renewal) != 0 {
		t.Fatalf("renewal claimed while a folded-over event compile still holds its lease: %+v", renewal)
	}
	if again := claim(commodoredb.MediaAuthorityLaneEvent); len(again) != 0 {
		t.Fatalf("a folded-over obligation was claimed while its compile still holds the lease: %+v", again)
	}
	settled, err = queries.CompleteMediaAuthorityObligation(ctx, commodoredb.CompleteMediaAuthorityObligationParams{TargetKey: stream.Key, Lane: commodoredb.MediaAuthorityLaneEvent, Revision: claimed[0].Revision, ClaimToken: claimed[0].ClaimToken})
	if err != nil || settled != 0 {
		t.Fatalf("stale-revision complete settled %d rows (err %v); it would erase event e6", settled, err)
	}
	if _, err = queries.ReleaseSupersededMediaAuthorityObligation(ctx, commodoredb.ReleaseSupersededMediaAuthorityObligationParams{TargetKey: stream.Key, Lane: commodoredb.MediaAuthorityLaneEvent, Revision: claimed[0].Revision, ClaimToken: claimed[0].ClaimToken}); err != nil {
		t.Fatal(err)
	}
	releaseClaim(stream.Key, commodoredb.MediaAuthorityLaneEvent, claimed[0].ClaimToken)
	if row, _ := readObligation(t, ctx, db, commodoredb.MediaAuthorityLaneEvent, stream.Key); row.status != "pending" || row.revision != 7 || row.leased {
		t.Fatalf("after fold-during-compile: %+v, want pending revision 7 with the lease cleared", row)
	}

	renewal := claim(commodoredb.MediaAuthorityLaneObjectDeadline)
	if len(renewal) != 1 || !renewal[0].BoundVersion.Valid || renewal[0].BoundVersion.Int64 != 1 {
		t.Fatalf("renewal not claimable once the event compile released: %+v", renewal)
	}
	// The guard is symmetric: the event lane waits for the in-flight renewal.
	if blocked := claim(commodoredb.MediaAuthorityLaneEvent); len(blocked) != 0 {
		t.Fatalf("event compile claimed while the renewal lane compiles the same target: %+v", blocked)
	}
	if settled, err = queries.CompleteMediaAuthorityObligation(ctx, commodoredb.CompleteMediaAuthorityObligationParams{TargetKey: stream.Key, Lane: commodoredb.MediaAuthorityLaneObjectDeadline, Revision: renewal[0].Revision, ClaimToken: renewal[0].ClaimToken}); err != nil || settled != 1 {
		t.Fatalf("complete renewal settled %d rows (err %v)", settled, err)
	}
	releaseClaim(stream.Key, commodoredb.MediaAuthorityLaneObjectDeadline, renewal[0].ClaimToken)

	// A permanently failing target parks after one attempt and stays out of the
	// queue until its source state changes or the reconciler re-arms it.
	claimed = claim(commodoredb.MediaAuthorityLaneEvent)
	if len(claimed) != 1 {
		t.Fatalf("re-armed obligation not claimable: %+v", claimed)
	}
	if _, err = queries.ParkMediaAuthorityObligation(ctx, commodoredb.ParkMediaAuthorityObligationParams{
		ParkReason: sql.NullString{String: "malformed_authority", Valid: true}, LastError: sql.NullString{String: "cannot compile", Valid: true},
		TargetKey: stream.Key, Lane: commodoredb.MediaAuthorityLaneEvent, Revision: claimed[0].Revision, ClaimToken: claimed[0].ClaimToken,
	}); err != nil {
		t.Fatal(err)
	}
	if parked := claim(commodoredb.MediaAuthorityLaneEvent); len(parked) != 0 {
		t.Fatalf("a parked obligation was claimed: %+v", parked)
	}
	enqueue("e7")
	if row, _ := readObligation(t, ctx, db, commodoredb.MediaAuthorityLaneEvent, stream.Key); row.status != "pending" || row.attempts != 0 || row.parkReason.Valid {
		t.Fatalf("a new event did not re-arm the parked target: %+v", row)
	}
	claimed = claim(commodoredb.MediaAuthorityLaneEvent)
	if _, err = queries.ParkMediaAuthorityObligation(ctx, commodoredb.ParkMediaAuthorityObligationParams{
		ParkReason: sql.NullString{String: "exhausted", Valid: true}, LastError: sql.NullString{String: "still failing", Valid: true},
		TargetKey: stream.Key, Lane: commodoredb.MediaAuthorityLaneEvent, Revision: claimed[0].Revision, ClaimToken: claimed[0].ClaimToken,
	}); err != nil {
		t.Fatal(err)
	}
	if rearmed, rearmErr := queries.RearmParkedMediaAuthorityObligations(ctx); rearmErr != nil || rearmed != 1 {
		t.Fatalf("reconciler re-armed %d parked targets (err %v), want 1", rearmed, rearmErr)
	}
	if len(claim(commodoredb.MediaAuthorityLaneEvent)) != 1 {
		t.Fatal("re-armed target is not claimable")
	}

	// Source-table triggers fold into the same obligation model.
	seedClaimStream(t, db, "sk_obligation_trigger")
	for _, requiresAuth := range []bool{true, false, true} {
		if _, err = db.ExecContext(ctx, `UPDATE commodore.streams SET requires_auth = $1 WHERE id = $2::uuid`, requiresAuth, claimStreamID); err != nil {
			t.Fatalf("update stream: %v", err)
		}
	}
	triggered := commodoredb.LiveStreamMediaAuthorityTarget(claimStreamID)
	if row, ok := readObligation(t, ctx, db, commodoredb.MediaAuthorityLaneEvent, triggered.Key); !ok || row.status != "pending" || row.revision < 3 {
		t.Fatalf("stream trigger obligation = %+v (found %v), want one pending row folded across every change", row, ok)
	}

	// Unfinished rows of the legacy inbox are handed over; rows another replica
	// holds a lease on are left to it.
	for _, legacy := range []struct {
		eventID, reason string
		leased          bool
	}{
		{"tenant-fanout:aa:live:one", "media_object:live_stream:72000000-0000-0000-0000-000000000001:tenant_authority_changed", false},
		{"tenant-fanout:bb:live:one", "media_object:live_stream:72000000-0000-0000-0000-000000000001:tenant_authority_changed", false},
		{"reconcile:1:tenant", "periodic_reconciliation", true},
	} {
		lease := "NULL"
		if legacy.leased {
			lease = "NOW() + INTERVAL '5 minutes'"
		}
		if _, err = db.ExecContext(ctx, `
			INSERT INTO commodore.media_authority_refresh_inbox (source_service, source_event_id, tenant_id, reason, status, lease_expires_at)
			VALUES ('commodore', $1, $2::uuid, $3, 'pending', `+lease+`)`, legacy.eventID, obligationTenantID, legacy.reason); err != nil {
			t.Fatalf("seed legacy inbox: %v", err)
		}
	}
	owner := &tenantPlacementOwnerFixture{}
	server := &CommodoreServer{db: db, logger: logging.NewLogger(), mediaAuthorityKeyID: "k", mediaAuthorityPrivateKey: ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)),
		authorityTenantSource: owner, authorityBillingSource: owner}
	server.adoptLegacyMediaAuthorityRefreshInbox(ctx)
	var unfinished, leasedLeft int
	if err = db.QueryRowContext(ctx, `
		SELECT count(*) FILTER (WHERE status <> 'completed'), count(*) FILTER (WHERE status <> 'completed' AND lease_expires_at > NOW())
		FROM commodore.media_authority_refresh_inbox`).Scan(&unfinished, &leasedLeft); err != nil {
		t.Fatal(err)
	}
	if unfinished != 1 || leasedLeft != 1 {
		t.Fatalf("legacy inbox unfinished=%d leased=%d, want only the leased row left", unfinished, leasedLeft)
	}
	adopted := commodoredb.LiveStreamMediaAuthorityTarget("72000000-0000-0000-0000-000000000001")
	if row, ok := readObligation(t, ctx, db, commodoredb.MediaAuthorityLaneEvent, adopted.Key); !ok || row.revision != 2 {
		t.Fatalf("adopted obligation = %+v (found %v), want two legacy rows folded into one", row, ok)
	}
}

// obligationGrantExpiry is fixed: a grant expiry is owner state, and a payload
// that moved with the wall clock would be a content change on every compile.
var obligationGrantExpiry = time.Now().Add(30 * 24 * time.Hour).UTC().Truncate(time.Second)

func obligationTenantPayload() *mediapb.TenantAuthority {
	return &mediapb.TenantAuthority{
		SchemaVersion: sharedauthority.SchemaVersion, TenantId: obligationTenantID,
		Lifecycle:          mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
		BillingDecision:    mediapb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW,
		BillingModel:       mediapb.TenantBillingModel_TENANT_BILLING_MODEL_POSTPAID,
		OfficialClusterId:  "cell-a",
		PreferredClusterId: "cell-a",
		EffectiveClusterGrants: []*mediapb.TenantClusterGrant{{
			ClusterId: "cell-a", AccessSource: clusterpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_PLATFORM_TIER,
			SubscriptionStatus: "active", ClusterClass: "platform_official", ControlCellId: "cell-a",
			EligibleServingCellIds: []string{"cell-a"}, ExpiresAt: timestamppb.New(obligationGrantExpiry),
		}},
	}
}

func testMediaAuthorityPublishesOnlyOnChange(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	server := &CommodoreServer{db: db, logger: logging.NewLogger(), mediaAuthorityKeyID: "obligation-key", mediaAuthorityPrivateKey: private}
	revisions := []*mediapb.AuthoritySourceRevision{{Service: "purser", Revision: "1"}}
	publish := func(payload *mediapb.TenantAuthority, issuedAt time.Time) {
		t.Helper()
		if publishErr := server.persistTenantAuthority(ctx, payload, []string{"cell-a"}, revisions, issuedAt, issuedAt.Add(mediaAuthorityValidity)); publishErr != nil {
			t.Fatalf("persistTenantAuthority: %v", publishErr)
		}
	}
	versions := func() int {
		t.Helper()
		var count int
		if countErr := db.QueryRowContext(ctx, `SELECT count(*) FROM commodore.media_authority_versions WHERE authority_kind = 'tenant' AND authority_id = $1`, obligationTenantID).Scan(&count); countErr != nil {
			t.Fatal(countErr)
		}
		return count
	}
	fanout := commodoredb.TenantMediaObjectsAuthorityTarget(obligationTenantID)
	fanoutRevision := func() int64 {
		row, _ := readObligation(t, ctx, db, commodoredb.MediaAuthorityLaneEvent, fanout.Key)
		return row.revision
	}
	now := time.Now().UTC()

	publish(obligationTenantPayload(), now)
	if versions() != 1 || fanoutRevision() != 1 {
		t.Fatalf("first publication: versions=%d fanout revision=%d, want 1 and 1", versions(), fanoutRevision())
	}
	tenantTarget := commodoredb.TenantMediaAuthorityTarget(obligationTenantID)
	if renewal, ok := readObligation(t, ctx, db, commodoredb.MediaAuthorityLaneTenantDeadline, tenantTarget.Key); !ok || renewal.status != "pending" {
		t.Fatalf("publication scheduled no renewal: %+v (found %v)", renewal, ok)
	}

	// Recompiling unchanged state, as reconciliation and redundant events do,
	// publishes nothing: no version, no deliveries, no object fan-out.
	for range 3 {
		publish(obligationTenantPayload(), now.Add(time.Minute))
	}
	var deliveries int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM commodore.media_authority_deliveries WHERE authority_id = $1`, obligationTenantID).Scan(&deliveries); err != nil {
		t.Fatal(err)
	}
	if versions() != 1 || deliveries != 1 || fanoutRevision() != 1 {
		t.Fatalf("unchanged recompiles: versions=%d deliveries=%d fanout revision=%d, want 1/1/1", versions(), deliveries, fanoutRevision())
	}

	// A metering-only change is a new tenant version that touches no object.
	metered := obligationTenantPayload()
	metered.DecisionReason = "allowance updated"
	publish(metered, now.Add(2*time.Minute))
	if versions() != 2 || fanoutRevision() != 1 {
		t.Fatalf("metering change: versions=%d fanout revision=%d, want 2 and 1", versions(), fanoutRevision())
	}

	// A change objects derive from publishes and fans out in the same commit.
	moved := proto.CloneOf(metered)
	moved.AllowPlatformSharedPlayback = true
	publish(moved, now.Add(3*time.Minute))
	if versions() != 3 || fanoutRevision() != 2 {
		t.Fatalf("serving policy change: versions=%d fanout revision=%d, want 3 and 2", versions(), fanoutRevision())
	}

	// Renewal: unchanged content publishes again once a third of validity has
	// passed and the new validity extends the current one, and not before.
	publish(moved, now.Add(3*time.Minute+mediaAuthorityValidity/4))
	if versions() != 3 {
		t.Fatalf("renewed before it was due: versions=%d, want 3", versions())
	}
	var issuedAt time.Time
	if err = db.QueryRowContext(ctx, `
		UPDATE commodore.media_authority_versions SET issued_at = issued_at - INTERVAL '9 hours',
		       refresh_after = refresh_after - INTERVAL '9 hours', valid_until = valid_until - INTERVAL '9 hours'
		WHERE authority_kind = 'tenant' AND authority_id = $1 AND authority_version = 3
		RETURNING issued_at`, obligationTenantID).Scan(&issuedAt); err != nil {
		t.Fatalf("age current version: %v", err)
	}
	publish(moved, time.Now().UTC())
	if versions() != 4 || fanoutRevision() != 2 {
		t.Fatalf("due renewal: versions=%d fanout revision=%d, want a 4th version and no object fan-out", versions(), fanoutRevision())
	}

	// Validity capped at a fixed instant, as a grant expiry does. Shrinking
	// validity publishes and refreshes the objects it caps. After that a due
	// renewal cannot extend anything, so it must publish nothing, even though
	// the computed instant carries nanoseconds the stored one does not.
	capped := time.Now().UTC().Add(5 * time.Hour).Truncate(time.Microsecond).Add(789 * time.Nanosecond)
	publishCapped := func() {
		t.Helper()
		issued := time.Now().UTC()
		if publishErr := server.persistTenantAuthority(ctx, moved, []string{"cell-a"}, revisions, issued, capped); publishErr != nil {
			t.Fatalf("persistTenantAuthority (capped): %v", publishErr)
		}
	}
	publishCapped()
	if versions() != 5 || fanoutRevision() != 3 {
		t.Fatalf("shrunk validity: versions=%d fanout revision=%d, want 5 and 3", versions(), fanoutRevision())
	}
	if _, err = db.ExecContext(ctx, `
		UPDATE commodore.media_authority_versions
		SET issued_at = issued_at - INTERVAL '9 hours', refresh_after = refresh_after - INTERVAL '9 hours'
		WHERE authority_kind = 'tenant' AND authority_id = $1 AND authority_version = 5`, obligationTenantID); err != nil {
		t.Fatalf("age capped version: %v", err)
	}
	publishCapped()
	if versions() != 5 {
		t.Fatalf("a due renewal that cannot extend validity published version %d", versions())
	}

	// A live authority never rests on a settled renewal: a cell refuses a
	// hard-expired authority outright. Any compile that publishes nothing puts
	// the schedule back, and so does the reconciler's repair pass, but neither
	// moves a schedule that is already live.
	settleRenewal := func() {
		t.Helper()
		if _, settleErr := db.ExecContext(ctx, `
			UPDATE commodore.media_authority_refresh_obligations SET status = 'completed', lease_expires_at = NULL
			WHERE lane = 'tenant_deadline' AND target_key = $1`, tenantTarget.Key); settleErr != nil {
			t.Fatal(settleErr)
		}
	}
	renewalDue := func() (string, time.Time) {
		t.Helper()
		var status string
		var due time.Time
		if readErr := db.QueryRowContext(ctx, `
			SELECT status, next_attempt_at FROM commodore.media_authority_refresh_obligations
			WHERE lane = 'tenant_deadline' AND target_key = $1`, tenantTarget.Key).Scan(&status, &due); readErr != nil {
			t.Fatal(readErr)
		}
		return status, due
	}
	withoutRenewal := func() int {
		t.Helper()
		rows, listErr := commodoredb.New(db).ListCurrentMediaAuthoritiesWithoutRenewal(ctx, 100)
		if listErr != nil {
			t.Fatal(listErr)
		}
		return len(rows)
	}
	settleRenewal()
	if withoutRenewal() != 1 {
		t.Fatalf("a live authority resting on a settled renewal is not listed for repair: %d", withoutRenewal())
	}
	publishCapped()
	status, due := renewalDue()
	if versions() != 5 || status != "pending" || withoutRenewal() != 0 {
		t.Fatalf("a no-op compile left the renewal %q (versions=%d, unrepaired=%d), want it pending again", status, versions(), withoutRenewal())
	}
	publishCapped()
	if _, again := renewalDue(); !again.Equal(due) {
		t.Fatalf("a no-op compile moved a live renewal from %s to %s", due, again)
	}
	settleRenewal()
	server.repairMediaAuthorityRenewals(ctx, commodoredb.New(db))
	if status, _ = renewalDue(); status != "pending" {
		t.Fatalf("renewal repair left the renewal %q", status)
	}

	// A tombstone every cell has acknowledged is terminal. It publishes once, is
	// never re-issued however old it gets, and is not something the repair pass
	// schedules. (One a cell may have missed, while that cell may still hold an
	// earlier valid version, is re-issued: see the invariants test.)
	tombstone := deletedTenantAuthorityPayload(obligationTenantID)
	publishTombstone := func() {
		t.Helper()
		issued := time.Now().UTC()
		if publishErr := server.persistTenantAuthority(ctx, tombstone, []string{"cell-a"}, revisions, issued, issued.Add(mediaAuthorityValidity)); publishErr != nil {
			t.Fatalf("persistTenantAuthority (tombstone): %v", publishErr)
		}
	}
	publishTombstone()
	if versions() != 6 {
		t.Fatalf("tombstone: versions=%d, want 6", versions())
	}
	if err = commodoredb.New(db).UpsertMediaAuthorityDistribution(ctx, commodoredb.UpsertMediaAuthorityDistributionParams{
		AuthorityKind: "tenant", AuthorityID: obligationTenantID, CellID: "cell-a", AuthorityVersion: 6,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `
		UPDATE commodore.media_authority_versions
		SET issued_at = issued_at - INTERVAL '23 hours', refresh_after = refresh_after - INTERVAL '23 hours', valid_until = valid_until - INTERVAL '23 hours'
		WHERE authority_kind = 'tenant' AND authority_id = $1 AND authority_version = 6`, obligationTenantID); err != nil {
		t.Fatalf("age tombstone: %v", err)
	}
	settleRenewal()
	publishTombstone()
	status, _ = renewalDue()
	if versions() != 6 || status != "completed" || withoutRenewal() != 0 {
		t.Fatalf("an aged tombstone: versions=%d renewal=%q unrepaired=%d, want 6, completed, 0", versions(), status, withoutRenewal())
	}
}
