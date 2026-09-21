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
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
)

func TestMediaAuthorityFollowsUse_RealPG(t *testing.T) {
	testMediaAuthorityFollowsUse(t, startCommodoreRealPG(t))
}

func TestMediaAuthorityFollowsUse_RealYugabyte(t *testing.T) {
	testMediaAuthorityFollowsUse(t, startPlacementDeliveryYugabyte(t, "authority_use"))
}

func TestMediaAuthorityInUseDecision_RealPG(t *testing.T) {
	testMediaAuthorityInUseDecision(t, startCommodoreRealPG(t))
}

func TestMediaAuthorityInUseDecision_RealYugabyte(t *testing.T) {
	testMediaAuthorityInUseDecision(t, startPlacementDeliveryYugabyte(t, "authority_in_use"))
}

// Whether an authority is in use is decided before anything expensive is
// compiled, from the database alone. These are the cases that decide what a
// compile costs and whether a new object of a tenant nobody uses can ever be
// served.
func testMediaAuthorityInUseDecision(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	server := &CommodoreServer{db: db, logger: logging.NewLogger()}
	queries := commodoredb.New(db)
	authorityID := sharedauthority.LiveStreamAuthorityID(useStreamID)
	old := 90 * 24 * time.Hour
	stale := func(kind, id string) {
		t.Helper()
		if _, err := queries.RecordMediaAuthorityUse(ctx, commodoredb.RecordMediaAuthorityUseParams{AuthorityKind: kind, AuthorityID: id, TenantID: obligationTenantID}); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `UPDATE commodore.media_authority_use SET last_used_at = NOW() - INTERVAL '45 days' WHERE authority_kind = $1 AND authority_id = $2`, kind, id); err != nil {
			t.Fatal(err)
		}
	}
	attest := func(cell string, reports bool) {
		t.Helper()
		if _, err := queries.UpsertMediaCellPlacementCapability(ctx, commodoredb.UpsertMediaCellPlacementCapabilityParams{CellID: cell, MaxSchemaVersion: 1, LiveReplicas: 1, UseReportsReady: reports}); err != nil {
			t.Fatal(err)
		}
	}
	used := func(kind, id string) bool {
		t.Helper()
		recently, err := mediaAuthorityUsedRecently(ctx, queries, kind, id, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		return recently
	}

	// A tenant that was never published is new to the compiler, not unused.
	tenantCtx, skip, err := server.decideTenantInUse(ctx, queries, obligationTenantID)
	if err != nil || skip || !mediaAuthorityInUse(tenantCtx) {
		t.Fatalf("a tenant never published: in use=%v skip=%v err=%v", mediaAuthorityInUse(tenantCtx), skip, err)
	}

	// An old object nobody has decided on, never published, in cells that all
	// report use: nothing to publish, and nothing expensive is compiled.
	stale("media_object", authorityID)
	stale("tenant", obligationTenantID)
	attest("cell-a", true)
	objectCtx, skip, err := server.decideMediaObjectInUse(ctx, queries, authorityID, obligationTenantID, old, false, []string{"cell-a"})
	if err != nil || !skip || mediaAuthorityInUse(objectCtx) {
		t.Fatalf("an unused, unpublished object: in use=%v skip=%v err=%v, want skipped", mediaAuthorityInUse(objectCtx), skip, err)
	}

	// A cell that does not report use cannot say the object is unused, so the
	// object stays in use: an older replica must not let an object in use go cold.
	attest("cell-b", false)
	objectCtx, skip, err = server.decideMediaObjectInUse(ctx, queries, authorityID, obligationTenantID, old, false, []string{"cell-a", "cell-b"})
	if err != nil || skip || !mediaAuthorityInUse(objectCtx) {
		t.Fatalf("an object delivered to a cell that does not report use: in use=%v skip=%v err=%v", mediaAuthorityInUse(objectCtx), skip, err)
	}

	// The tenant of that object has no authority and nobody wants the object, so
	// a change to it does not wake the tenant.
	if correct, waitErr := server.mediaObjectWithoutTenantAuthority(ctx, queries, authorityID, obligationTenantID, old, false, false, errTenantAuthorityMissing); correct || waitErr != nil {
		t.Fatalf("an unwanted object whose tenant has no authority returned correct=%v %v, want nothing to do", correct, waitErr)
	}
	if used("tenant", obligationTenantID) {
		t.Fatal("a change to an object nobody uses recorded use of its tenant")
	}
	// A deleted object still waits: its tombstone has to reach cells.
	if _, err = server.mediaObjectWithoutTenantAuthority(ctx, queries, authorityID, obligationTenantID, old, false, true, errTenantAuthorityMissing); err == nil {
		t.Fatal("a deleted object whose tenant has no authority was dropped instead of waiting")
	}

	// A new object of that tenant is wanted. It waits for the tenant's authority,
	// and being new is use of the tenant too, which is what lets the tenant
	// compile publish instead of finding a tenant nobody uses.
	const newStream = "73000000-0000-0000-0000-000000000009"
	newID := sharedauthority.LiveStreamAuthorityID(newStream)
	if _, err = server.mediaObjectWithoutTenantAuthority(ctx, queries, newID, obligationTenantID, time.Hour, false, false, errTenantAuthorityMissing); err == nil {
		t.Fatal("a new object whose tenant has no authority was dropped instead of waiting")
	}
	if !used("tenant", obligationTenantID) || !used("media_object", newID) {
		t.Fatalf("a new object: object in use=%v tenant in use=%v, want both", used("media_object", newID), used("tenant", obligationTenantID))
	}
	tenantCtx, skip, err = server.decideTenantInUse(ctx, queries, obligationTenantID)
	if err != nil || skip || !mediaAuthorityInUse(tenantCtx) {
		t.Fatalf("the tenant of a new object: in use=%v skip=%v err=%v", mediaAuthorityInUse(tenantCtx), skip, err)
	}

	// Ingest is use, and it is seen nowhere else.
	const liveStream = "73000000-0000-0000-0000-000000000008"
	liveID := sharedauthority.LiveStreamAuthorityID(liveStream)
	objectCtx, skip, err = server.decideMediaObjectInUse(ctx, queries, liveID, obligationTenantID, old, true, []string{"cell-a"})
	if err != nil || skip || !mediaAuthorityInUse(objectCtx) || !used("media_object", liveID) {
		t.Fatalf("an ingesting stream: in use=%v skip=%v recorded=%v err=%v", mediaAuthorityInUse(objectCtx), skip, used("media_object", liveID), err)
	}

	// A caller that already decided (a fetch is use by definition) is not
	// second-guessed.
	decided := withMediaAuthorityInUse(ctx, true)
	if got, gotSkip, decideErr := server.decideMediaObjectInUse(decided, queries, authorityID, obligationTenantID, old, false, []string{"cell-a"}); decideErr != nil || gotSkip || !mediaAuthorityInUse(got) {
		t.Fatalf("a fetched object was decided again: in use=%v skip=%v err=%v", mediaAuthorityInUse(got), gotSkip, decideErr)
	}
}

func TestMediaAuthorityReconcileReissuesOncePerCompilerChange_RealPG(t *testing.T) {
	testMediaAuthorityReconcileReissues(t, startCommodoreRealPG(t))
}

func TestMediaAuthorityReconcileReissuesOncePerCompilerChange_RealYugabyte(t *testing.T) {
	testMediaAuthorityReconcileReissues(t, startPlacementDeliveryYugabyte(t, "authority_compiler_change"))
}

// An unchanged compile publishes nothing, so nothing re-issues authorities when
// the compiler itself changes (a new signing key, a new payload shape) unless
// reconciliation does. It does so once per change, for the tenants in use, in
// the bulk lane, and leaves a tenant nobody uses alone.
func testMediaAuthorityReconcileReissues(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	owner := &tenantPlacementOwnerFixture{}
	server := &CommodoreServer{db: db, logger: logging.NewLogger(), mediaAuthorityKeyID: "key-1",
		mediaAuthorityPrivateKey: ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)),
		authorityTenantSource:    owner, authorityBillingSource: owner}
	queries := commodoredb.New(db)
	const dormantTenant = "70000000-0000-0000-0000-000000000002"
	for _, tenantID := range []string{obligationTenantID, dormantTenant} {
		if err := queries.ScheduleMediaAuthorityRenewal(ctx, commodoredb.TenantMediaAuthorityTarget(tenantID), tenantID, 1, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `UPDATE commodore.media_authority_refresh_obligations SET status = 'dormant' WHERE tenant_id = $1::uuid`, dormantTenant); err != nil {
		t.Fatal(err)
	}
	bulk := func(targetKey string) (revision int64, found bool) {
		t.Helper()
		err := db.QueryRowContext(ctx, `SELECT revision FROM commodore.media_authority_refresh_obligations WHERE lane = 'bulk' AND target_key = $1`, targetKey).Scan(&revision)
		if err == sql.ErrNoRows {
			return 0, false
		}
		if err != nil {
			t.Fatal(err)
		}
		return revision, true
	}
	tenantKey := commodoredb.TenantMediaAuthorityTarget(obligationTenantID).Key
	objectsKey := commodoredb.TenantMediaObjectsAuthorityTarget(obligationTenantID).Key

	server.reconcileMediaAuthorities(ctx)
	if _, ok := bulk(tenantKey); !ok {
		t.Fatal("a tenant in use was not reconciled")
	}
	if revision, ok := bulk(objectsKey); !ok || revision != 1 {
		t.Fatalf("first run of a compiler: objects re-issue revision=%d found=%v, want one", revision, ok)
	}
	for _, key := range []string{commodoredb.TenantMediaAuthorityTarget(dormantTenant).Key, commodoredb.TenantMediaObjectsAuthorityTarget(dormantTenant).Key} {
		if _, ok := bulk(key); ok {
			t.Fatalf("a tenant nobody uses was reconciled: %s", key)
		}
	}
	var events int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM commodore.media_authority_refresh_obligations WHERE lane = 'event'`).Scan(&events); err != nil || events != 0 {
		t.Fatalf("reconciliation put %d rows in the event lane (err %v)", events, err)
	}

	// The same compiler again: tenants are reconciled, objects are not re-issued.
	server.reconcileMediaAuthorities(ctx)
	if revision, _ := bulk(tenantKey); revision != 2 {
		t.Fatalf("tenant reconcile revision = %d, want 2", revision)
	}
	if revision, _ := bulk(objectsKey); revision != 1 {
		t.Fatalf("objects were re-issued again by the same compiler (revision %d)", revision)
	}

	// A new signing key is a different compiler: one more re-issue, then none.
	server.mediaAuthorityKeyID = "key-2"
	server.reconcileMediaAuthorities(ctx)
	server.reconcileMediaAuthorities(ctx)
	if revision, _ := bulk(objectsKey); revision != 2 {
		t.Fatalf("a new signing key re-issued objects %d time(s) in total, want 2", revision)
	}
}

const useStreamID = "73000000-0000-0000-0000-000000000001"

func useStreamPayload(internalName string) *mediapb.MediaObjectAuthority {
	return &mediapb.MediaObjectAuthority{
		SchemaVersion: sharedauthority.SchemaVersion, ObjectKind: mediapb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM,
		TenantId: obligationTenantID, InternalName: internalName, PlaybackId: "playback-" + internalName,
		Lifecycle:      mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
		PlaybackPolicy: &mediapb.PlaybackPolicy{Kind: mediapb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_PUBLIC},
		Object:         &mediapb.MediaObjectAuthority_LiveStream{LiveStream: &mediapb.LiveStreamAuthority{StreamId: useStreamID, IngestMode: "push"}},
	}
}

// An authority is kept in cells and renewed only while it is in use. These are
// the SQL-level rules that make that safe: use is recorded to the day and
// revives a renewal that went dormant, a change to an unused authority never
// lengthens the copies cells hold, an unused authority with no copy left
// publishes nothing, and a tenant change reaches only objects cells still hold.
func testMediaAuthorityFollowsUse(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	server := &CommodoreServer{db: db, logger: logging.NewLogger(), mediaAuthorityKeyID: "use-key", mediaAuthorityPrivateKey: private}
	queries := commodoredb.New(db)
	authorityID := sharedauthority.LiveStreamAuthorityID(useStreamID)
	target := mediaObjectAuthorityTarget(authorityID)
	revisions := []*mediapb.AuthoritySourceRevision{{Service: "commodore", Revision: "1"}}

	// Use is kept to the day, and an authority without a row counts as used when
	// use started being recorded, not as never used.
	epochUse, err := queries.GetMediaAuthorityLastUse(ctx, commodoredb.GetMediaAuthorityLastUseParams{AuthorityKind: "media_object", AuthorityID: authorityID})
	if err != nil || time.Since(epochUse) > time.Hour {
		t.Fatalf("an authority with no recorded use reads as used at %s (err %v), want the recording epoch", epochUse, err)
	}
	for attempt, want := range []int64{1, 0} {
		advanced, recordErr := queries.RecordMediaAuthorityUse(ctx, commodoredb.RecordMediaAuthorityUseParams{AuthorityKind: "media_object", AuthorityID: authorityID, TenantID: obligationTenantID})
		if recordErr != nil || advanced != want {
			t.Fatalf("use report %d advanced %d rows (err %v), want %d", attempt, advanced, recordErr, want)
		}
	}

	versions := func() (count int, validUntil time.Time) {
		t.Helper()
		if scanErr := db.QueryRowContext(ctx, `
			SELECT count(*), COALESCE(max(valid_until) FILTER (WHERE authority_version = (SELECT max(authority_version) FROM commodore.media_authority_versions WHERE authority_id = $1)), 'epoch')
			FROM commodore.media_authority_versions WHERE authority_kind = 'media_object' AND authority_id = $1`, authorityID).Scan(&count, &validUntil); scanErr != nil {
			t.Fatal(scanErr)
		}
		return count, validUntil
	}
	publish := func(publishCtx context.Context, payload *mediapb.MediaObjectAuthority, cells []string, validity time.Duration) {
		t.Helper()
		issued := time.Now().UTC()
		if publishErr := server.persistMediaObjectAuthority(publishCtx, authorityID, payload, cells, revisions, issued, issued.Add(validity)); publishErr != nil {
			t.Fatalf("persistMediaObjectAuthority: %v", publishErr)
		}
	}
	renewal := func() (status string, expiresAt sql.NullTime) {
		t.Helper()
		if scanErr := db.QueryRowContext(ctx, `
			SELECT status, expires_at FROM commodore.media_authority_refresh_obligations
			WHERE lane = 'object_deadline' AND target_key = $1`, target.Key).Scan(&status, &expiresAt); scanErr != nil {
			t.Fatal(scanErr)
		}
		return status, expiresAt
	}
	attest := func(cell string, longValidity bool) {
		t.Helper()
		if _, attestErr := queries.UpsertMediaCellPlacementCapability(ctx, commodoredb.UpsertMediaCellPlacementCapabilityParams{
			CellID: cell, MaxSchemaVersion: 1, LiveReplicas: 1, LongValidityReady: longValidity, UseReportsReady: true,
		}); attestErr != nil {
			t.Fatal(attestErr)
		}
	}

	// In use: published with the validity it was compiled with, and the renewal
	// carries the instant its version stops being valid.
	inUse := withMediaAuthorityInUse(ctx, true)
	publish(inUse, useStreamPayload("first"), []string{"cell-a"}, mediaAuthorityValidity)
	count, firstValidUntil := versions()
	status, expiresAt := renewal()
	if count != 1 || status != "pending" || !expiresAt.Valid || !expiresAt.Time.Equal(firstValidUntil) {
		t.Fatalf("in-use publication: versions=%d renewal=%s expires_at=%v, want 1, pending, %s", count, status, expiresAt, firstValidUntil)
	}

	// The longer validity is issued only when every cell the version goes to
	// accepts it: a replica without that capability rejects the envelope outright.
	long := withMediaAuthorityLongValidity(inUse, time.Now().UTC().Add(sharedauthority.MaxMediaObjectValidity))
	attest("cell-a", true)
	attest("cell-b", false)
	publish(long, useStreamPayload("second"), []string{"cell-a", "cell-b"}, mediaAuthorityValidity)
	if _, validUntil := versions(); time.Until(validUntil) > 2*mediaAuthorityValidity {
		t.Fatalf("a cell that does not accept long validity was sent one valid until %s", validUntil)
	}
	attest("cell-b", true)
	publish(long, useStreamPayload("third"), []string{"cell-a", "cell-b"}, mediaAuthorityValidity)
	count, longValidUntil := versions()
	if count != 3 || time.Until(longValidUntil) < sharedauthority.MaxMediaObjectValidity-time.Hour {
		t.Fatalf("every cell accepts long validity: versions=%d valid until %s, want 3 and about thirty days", count, longValidUntil)
	}

	// Not in use, copies still valid: a change is published so the copies are
	// corrected, with the validity they already have. It is not renewed. The
	// compile offers the long validity, as the worker does once every cell
	// attests; the version is still capped at the copies it corrects.
	unused := withMediaAuthorityLongValidity(withMediaAuthorityInUse(ctx, false), time.Now().UTC().Add(sharedauthority.MaxMediaObjectValidity))
	publish(unused, useStreamPayload("fourth"), []string{"cell-a", "cell-b"}, mediaAuthorityValidity)
	count, inherited := versions()
	if count != 4 || !inherited.Equal(longValidUntil) {
		t.Fatalf("a change to an unused authority: versions=%d valid until %s, want 4 and the inherited %s", count, inherited, longValidUntil)
	}
	publish(unused, useStreamPayload("fourth"), []string{"cell-a", "cell-b"}, mediaAuthorityValidity)
	if count, _ = versions(); count != 4 {
		t.Fatalf("an unchanged unused authority published version %d", count)
	}

	// Its renewal goes dormant: not claimable, not re-armed by reconciliation, and
	// not something the repair pass schedules again. A fold that lands while the
	// renewal is compiling wins over the dormant settle.
	claim := func() commodoredb.ClaimMediaAuthorityObligationsRow {
		t.Helper()
		if _, dueErr := db.ExecContext(ctx, `UPDATE commodore.media_authority_refresh_obligations SET next_attempt_at = NOW() WHERE lane = 'object_deadline' AND target_key = $1`, target.Key); dueErr != nil {
			t.Fatal(dueErr)
		}
		rows, claimErr := queries.ClaimMediaAuthorityObligations(ctx, commodoredb.ClaimMediaAuthorityObligationsParams{LeaseMs: 60_000, Lane: commodoredb.MediaAuthorityLaneObjectDeadline, BatchSize: 8})
		if claimErr != nil || len(rows) != 1 {
			t.Fatalf("claim renewal: %d rows (err %v)", len(rows), claimErr)
		}
		return rows[0]
	}
	claimed := claim()
	if err = queries.ScheduleMediaAuthorityRenewal(ctx, target, obligationTenantID, 4, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if settled, settleErr := queries.SettleDormantMediaAuthorityObligation(ctx, commodoredb.SettleDormantMediaAuthorityObligationParams{TargetKey: target.Key, Lane: claimed.Lane, Revision: claimed.Revision, ClaimToken: claimed.ClaimToken}); settleErr != nil || settled != 0 {
		t.Fatalf("a dormant settle at a stale revision settled %d rows (err %v)", settled, settleErr)
	}
	if _, err = queries.ReleaseSupersededMediaAuthorityObligation(ctx, commodoredb.ReleaseSupersededMediaAuthorityObligationParams{TargetKey: target.Key, Lane: claimed.Lane, Revision: claimed.Revision, ClaimToken: claimed.ClaimToken}); err != nil {
		t.Fatal(err)
	}
	if err = queries.ReleaseMediaAuthorityTargetClaim(ctx, commodoredb.ReleaseMediaAuthorityTargetClaimParams{TargetKey: target.Key, Lane: claimed.Lane, ClaimToken: claimed.ClaimToken}); err != nil {
		t.Fatal(err)
	}
	claimed = claim()
	if settled, settleErr := queries.SettleDormantMediaAuthorityObligation(ctx, commodoredb.SettleDormantMediaAuthorityObligationParams{TargetKey: target.Key, Lane: claimed.Lane, Revision: claimed.Revision, ClaimToken: claimed.ClaimToken}); settleErr != nil || settled != 1 {
		t.Fatalf("dormant settle settled %d rows (err %v)", settled, settleErr)
	}
	if _, err = queries.RearmParkedMediaAuthorityObligations(ctx); err != nil {
		t.Fatal(err)
	}
	server.repairMediaAuthorityRenewals(ctx, queries)
	if status, _ = renewal(); status != "dormant" {
		t.Fatalf("reconciliation or repair moved a dormant renewal to %q", status)
	}
	if rows, claimErr := queries.ClaimMediaAuthorityObligations(ctx, commodoredb.ClaimMediaAuthorityObligationsParams{LeaseMs: 60_000, Lane: commodoredb.MediaAuthorityLaneObjectDeadline, BatchSize: 8}); claimErr != nil || len(rows) != 0 {
		t.Fatalf("a dormant renewal was claimed: %+v (err %v)", rows, claimErr)
	}

	// Use that arrives while the copy is still valid has to extend it before it
	// runs out, so it revives the renewal at once.
	if _, err = db.ExecContext(ctx, `UPDATE commodore.media_authority_use SET last_used_at = NOW() - INTERVAL '40 days' WHERE authority_id = $1`, authorityID); err != nil {
		t.Fatal(err)
	}
	if used, useErr := mediaAuthorityUsedRecently(ctx, queries, "media_object", authorityID, time.Now().UTC()); useErr != nil || used {
		t.Fatalf("forty days without use still reads as in use (err %v)", useErr)
	}
	if err = server.recordMediaAuthorityUse(ctx, "media_object", authorityID, obligationTenantID); err != nil {
		t.Fatal(err)
	}
	if status, _ = renewal(); status != "pending" {
		t.Fatalf("use left the renewal %q, want pending", status)
	}
	if used, useErr := mediaAuthorityUsedRecently(ctx, queries, "tenant", obligationTenantID, time.Now().UTC()); useErr != nil || !used {
		t.Fatalf("an object's use did not reach its tenant (err %v)", useErr)
	}

	// A tenant change refreshes the objects cells still hold, in the bulk lane,
	// and nothing else: not an object whose copies ran out, not a tombstone.
	publishOther := func(streamID string, payload *mediapb.MediaObjectAuthority, expired bool) {
		t.Helper()
		id := sharedauthority.LiveStreamAuthorityID(streamID)
		issued := time.Now().UTC()
		payload.Object = &mediapb.MediaObjectAuthority_LiveStream{LiveStream: &mediapb.LiveStreamAuthority{StreamId: streamID, IngestMode: "push"}}
		if publishErr := server.persistMediaObjectAuthority(inUse, id, payload, []string{"cell-a"}, revisions, issued, issued.Add(mediaAuthorityValidity)); publishErr != nil {
			t.Fatalf("publish %s: %v", streamID, publishErr)
		}
		if !expired {
			return
		}
		if _, ageErr := db.ExecContext(ctx, `
			UPDATE commodore.media_authority_versions
			SET issued_at = issued_at - INTERVAL '3 days', refresh_after = refresh_after - INTERVAL '3 days', valid_until = valid_until - INTERVAL '3 days'
			WHERE authority_id = $1`, id); ageErr != nil {
			t.Fatal(ageErr)
		}
	}
	const expiredStream, deletedStream = "73000000-0000-0000-0000-000000000002", "73000000-0000-0000-0000-000000000003"
	publishOther(expiredStream, useStreamPayload("expired"), true)
	publishOther(deletedStream, useStreamPayload("deleted"), false)
	tombstone := useStreamPayload("deleted")
	tombstone.Lifecycle = mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_TOMBSTONE
	tombstone.PlaybackPolicy = denyPlaybackPolicy()
	publishOther(deletedStream, tombstone, false)
	if err = server.enqueueTenantMediaObjectRefreshes(ctx, obligationTenantID); err != nil {
		t.Fatal(err)
	}
	var bulkTargets []string
	rows, err := db.QueryContext(ctx, `SELECT target_key FROM commodore.media_authority_refresh_obligations WHERE lane = 'bulk' ORDER BY target_key`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var key string
		if err = rows.Scan(&key); err != nil {
			t.Fatal(err)
		}
		bulkTargets = append(bulkTargets, key)
	}
	_ = rows.Close()
	if len(bulkTargets) != 1 || bulkTargets[0] != target.Key {
		t.Fatalf("tenant fanout enqueued %v in the bulk lane, want only %s", bulkTargets, target.Key)
	}
	var eventFanout int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM commodore.media_authority_refresh_obligations WHERE lane = 'event' AND target_kind = 'live_stream'`).Scan(&eventFanout); err != nil || eventFanout != 0 {
		t.Fatalf("fanout put %d objects in the event lane (err %v)", eventFanout, err)
	}

	// A cell whose every copy has expired has nothing left to correct and drops
	// out of what a change is sent to. A tombstone still reaches it.
	holding, err := queries.ListMediaAuthorityHoldingCells(ctx, commodoredb.ListMediaAuthorityHoldingCellsParams{AuthorityKind: "media_object", AuthorityID: sharedauthority.LiveStreamAuthorityID(expiredStream)})
	if err != nil || len(holding) != 0 {
		t.Fatalf("cells holding an expired authority = %v (err %v), want none", holding, err)
	}
	prior, err := queries.ListMediaAuthorityPriorCells(ctx, commodoredb.ListMediaAuthorityPriorCellsParams{AuthorityKind: "media_object", AuthorityID: sharedauthority.LiveStreamAuthorityID(expiredStream)})
	if err != nil || len(prior) != 1 {
		t.Fatalf("cells that ever held the authority = %v (err %v), want cell-a for its tombstone", prior, err)
	}

	// Unused with no copy left: a change publishes nothing. Unused with a copy
	// about to run out: nothing either, because it could not arrive in time and
	// that expiry already bounds how long the old content is honoured.
	expiredID := sharedauthority.LiveStreamAuthorityID(expiredStream)
	countFor := func(id string) int {
		t.Helper()
		var n int
		if scanErr := db.QueryRowContext(ctx, `SELECT count(*) FROM commodore.media_authority_versions WHERE authority_id = $1`, id).Scan(&n); scanErr != nil {
			t.Fatal(scanErr)
		}
		return n
	}
	changed := useStreamPayload("expired-changed")
	changed.Object = &mediapb.MediaObjectAuthority_LiveStream{LiveStream: &mediapb.LiveStreamAuthority{StreamId: expiredStream, IngestMode: "push"}}
	issued := time.Now().UTC()
	if err = server.persistMediaObjectAuthority(unused, expiredID, changed, []string{"cell-a"}, revisions, issued, issued.Add(mediaAuthorityValidity)); err != nil {
		t.Fatal(err)
	}
	if got := countFor(expiredID); got != 1 {
		t.Fatalf("an unused authority with no copy left published version %d", got)
	}
	if _, err = db.ExecContext(ctx, `
		UPDATE commodore.media_authority_versions SET valid_until = NOW() + INTERVAL '30 seconds', refresh_after = NOW() - INTERVAL '1 hour', issued_at = NOW() - INTERVAL '2 hours'
		WHERE authority_id = $1`, expiredID); err != nil {
		t.Fatal(err)
	}
	if err = server.persistMediaObjectAuthority(unused, expiredID, changed, []string{"cell-a"}, revisions, issued, issued.Add(mediaAuthorityValidity)); err != nil {
		t.Fatal(err)
	}
	if got := countFor(expiredID); got != 1 {
		t.Fatalf("a change to an unused authority with seconds left published version %d", got)
	}
	// Being used again publishes it, with a full validity.
	if err = server.persistMediaObjectAuthority(inUse, expiredID, changed, []string{"cell-a"}, revisions, issued, issued.Add(mediaAuthorityValidity)); err != nil {
		t.Fatal(err)
	}
	if got := countFor(expiredID); got != 2 {
		t.Fatalf("an authority in use again has %d versions, want 2", got)
	}
}
