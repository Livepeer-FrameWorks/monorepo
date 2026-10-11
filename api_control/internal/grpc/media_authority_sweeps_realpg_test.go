//go:build schema_verify

package grpc

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"fmt"
	"sort"
	"testing"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
)

func TestMediaAuthorityReconcileSingleReplicaPerTick_RealPG(t *testing.T) {
	testMediaAuthorityReconcileSingleReplicaPerTick(t, startCommodoreRealPG(t))
}

func TestMediaAuthorityReconcileSingleReplicaPerTick_RealYugabyte(t *testing.T) {
	testMediaAuthorityReconcileSingleReplicaPerTick(t, startPlacementDeliveryYugabyte(t, "authority_sweep_leader"))
}

func TestMediaAuthorityTenantFanoutChunks_RealPG(t *testing.T) {
	testMediaAuthorityTenantFanoutChunks(t, startCommodoreRealPG(t))
}

func TestMediaAuthorityTenantFanoutChunks_RealYugabyte(t *testing.T) {
	testMediaAuthorityTenantFanoutChunks(t, startPlacementDeliveryYugabyte(t, "authority_fanout_chunks"))
}

func TestMediaAuthorityQueueSweepDrainsInBoundedBatches_RealPG(t *testing.T) {
	testMediaAuthorityQueueSweepDrainsInBoundedBatches(t, startCommodoreRealPG(t))
}

func TestMediaAuthorityQueueSweepDrainsInBoundedBatches_RealYugabyte(t *testing.T) {
	testMediaAuthorityQueueSweepDrainsInBoundedBatches(t, startPlacementDeliveryYugabyte(t, "authority_queue_sweep"))
}

func TestMediaAuthorityParkedRearmBudget_RealPG(t *testing.T) {
	testMediaAuthorityParkedRearmBudget(t, startCommodoreRealPG(t))
}

func TestMediaAuthorityParkedRearmBudget_RealYugabyte(t *testing.T) {
	testMediaAuthorityParkedRearmBudget(t, startPlacementDeliveryYugabyte(t, "authority_rearm_budget"))
}

func TestMediaAuthorityPlaybackPolicyChange_OnlyStreamAndChildrenRepublish_RealPG(t *testing.T) {
	testMediaAuthorityPlaybackPolicyChangeScope(t, startCommodoreRealPG(t))
}

func TestMediaAuthorityPlaybackPolicyChange_OnlyStreamAndChildrenRepublish_RealYugabyte(t *testing.T) {
	testMediaAuthorityPlaybackPolicyChangeScope(t, startPlacementDeliveryYugabyte(t, "authority_policy_scope"))
}

func TestMediaAuthorityCellReachableMakesDeliveriesDue_RealPG(t *testing.T) {
	testMediaAuthorityCellReachableMakesDeliveriesDue(t, startCommodoreRealPG(t))
}

func TestMediaAuthorityCellReachableMakesDeliveriesDue_RealYugabyte(t *testing.T) {
	testMediaAuthorityCellReachableMakesDeliveriesDue(t, startPlacementDeliveryYugabyte(t, "authority_cell_reachable"))
}

// insertHeldMediaObjectAuthority records a current, valid version of an
// object's authority, which is what makes it one a cell may hold.
func insertHeldMediaObjectAuthority(t *testing.T, ctx context.Context, q *commodoredb.Queries, authorityID string, tombstone bool) {
	t.Helper()
	now := time.Now().UTC()
	if err := q.InsertMediaAuthorityVersion(ctx, commodoredb.InsertMediaAuthorityVersionParams{
		AuthorityKind: "media_object", AuthorityID: authorityID, AuthorityVersion: 1, PayloadSchemaVersion: 1,
		Payload: []byte{}, PayloadSha256: make([]byte, 32), SourceRevisions: []byte("[]"), Tombstone: tombstone,
		IssuedAt: now, RefreshAfter: now.Add(30 * time.Minute), ValidUntil: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if rows, err := q.UpsertCurrentMediaAuthority(ctx, commodoredb.UpsertCurrentMediaAuthorityParams{AuthorityKind: "media_object", AuthorityID: authorityID, AuthorityVersion: 1}); err != nil || rows != 1 {
		t.Fatalf("set current %s: rows=%d err=%v", authorityID, rows, err)
	}
}

// The hourly reconciliation, retention and activation sweeps cover the whole
// catalog. Every replica ticks, and only the elected one sweeps; when it steps
// down the next replica to tick takes over.
func testMediaAuthorityReconcileSingleReplicaPerTick(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	replica := func() *CommodoreServer {
		owner := &tenantPlacementOwnerFixture{}
		server := &CommodoreServer{db: db, logger: logging.NewLogger(), mediaAuthorityKeyID: "key-1",
			mediaAuthorityPrivateKey: ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)),
			authorityTenantSource:    owner, authorityBillingSource: owner}
		t.Cleanup(server.releaseMediaAuthoritySweeps)
		return server
	}
	first, second := replica(), replica()
	queries := commodoredb.New(db)
	if err := queries.ScheduleMediaAuthorityRenewal(ctx, commodoredb.TenantMediaAuthorityTarget(obligationTenantID), obligationTenantID, 1, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	tenantKey := commodoredb.TenantMediaAuthorityTarget(obligationTenantID).Key
	reconciled := func() int64 {
		t.Helper()
		var revision int64
		err := db.QueryRowContext(ctx, `SELECT revision FROM commodore.media_authority_refresh_obligations WHERE lane = 'bulk' AND target_key = $1`, tenantKey).Scan(&revision)
		if err == sql.ErrNoRows {
			return 0
		}
		if err != nil {
			t.Fatal(err)
		}
		return revision
	}

	first.reconcileMediaAuthoritiesOnLeader(ctx)
	second.reconcileMediaAuthoritiesOnLeader(ctx)
	if got := reconciled(); got != 1 {
		t.Fatalf("one reconcile tick on two replicas enqueued the tenant %d time(s), want 1", got)
	}
	if !first.leadsMediaAuthoritySweeps(ctx) || second.leadsMediaAuthoritySweeps(ctx) {
		t.Fatal("both replicas, or neither, lead the sweeps")
	}

	first.releaseMediaAuthoritySweeps()
	second.reconcileMediaAuthoritiesOnLeader(ctx)
	first.reconcileMediaAuthoritiesOnLeader(ctx)
	if got := reconciled(); got != 2 {
		t.Fatalf("after the leader stepped down the next tick reconciled %d time(s) in total, want 2", got)
	}
}

// A tenant fanout enqueues the tenant's held objects a bounded number per
// statement and still reaches every one of them.
func testMediaAuthorityTenantFanoutChunks(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	q := commodoredb.New(db)
	const otherTenant = "70000000-0000-0000-0000-000000000009"
	var held []string
	for i := range 5 {
		id := fmt.Sprintf("74000000-0000-0000-0000-%012d", i+1)
		target := commodoredb.ArtifactMediaAuthorityTarget(id)
		insertHeldMediaObjectAuthority(t, ctx, q, sharedauthority.ArtifactAuthorityID(id), false)
		if err := q.ScheduleMediaAuthorityRenewal(ctx, target, obligationTenantID, 1, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		held = append(held, target.Key)
	}
	// A tombstone and another tenant's object are not part of the fanout.
	for id, tenant := range map[string]string{"74000000-0000-0000-0000-000000000098": obligationTenantID, "74000000-0000-0000-0000-000000000099": otherTenant} {
		insertHeldMediaObjectAuthority(t, ctx, q, sharedauthority.ArtifactAuthorityID(id), tenant == obligationTenantID)
		if err := q.ScheduleMediaAuthorityRenewal(ctx, commodoredb.ArtifactMediaAuthorityTarget(id), tenant, 1, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	server := &CommodoreServer{db: db, logger: logging.NewLogger()}

	chunks, err := server.enqueueTenantMediaObjectRefreshesInChunks(ctx, obligationTenantID, 2)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, enqueued := range chunks {
		if enqueued > 2 {
			t.Fatalf("one fanout statement enqueued %d objects, want at most 2 (chunks %v)", enqueued, chunks)
		}
		total += enqueued
	}
	if total != int64(len(held)) {
		t.Fatalf("fanout enqueued %d objects across chunks %v, want %d", total, chunks, len(held))
	}
	rows, err := db.QueryContext(ctx, `SELECT target_key FROM commodore.media_authority_refresh_obligations WHERE lane = 'bulk' ORDER BY target_key`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var got []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			t.Fatal(err)
		}
		got = append(got, key)
	}
	sort.Strings(held)
	if fmt.Sprint(got) != fmt.Sprint(held) {
		t.Fatalf("fanout enqueued %v, want exactly the tenant's held objects %v", got, held)
	}
}

// The minute sweep settles a backlog larger than one batch in one pass, a batch
// per statement.
func testMediaAuthorityQueueSweepDrainsInBoundedBatches(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	q := commodoredb.New(db)
	insert := func(id string, version int64, validity time.Duration) {
		t.Helper()
		issuedAt, validUntil := time.Now().UTC().Add(-2*time.Hour), time.Now().UTC().Add(validity)
		if err := q.InsertMediaAuthorityVersion(ctx, commodoredb.InsertMediaAuthorityVersionParams{
			AuthorityKind: "tenant", AuthorityID: id, AuthorityVersion: version, PayloadSchemaVersion: 1,
			Payload: []byte{}, PayloadSha256: make([]byte, 32), SourceRevisions: []byte("[]"),
			IssuedAt: issuedAt, RefreshAfter: issuedAt.Add(validUntil.Sub(issuedAt) / 2), ValidUntil: validUntil,
		}); err != nil {
			t.Fatal(err)
		}
		if rows, err := q.UpsertCurrentMediaAuthority(ctx, commodoredb.UpsertCurrentMediaAuthorityParams{AuthorityKind: "tenant", AuthorityID: id, AuthorityVersion: version}); err != nil || rows != 1 {
			t.Fatalf("set current %s/%d: rows=%d err=%v", id, version, rows, err)
		}
	}
	enqueue := func(id string, version int64, cell string) {
		t.Helper()
		if _, err := q.EnqueueMediaAuthorityDelivery(ctx, commodoredb.EnqueueMediaAuthorityDeliveryParams{AuthorityKind: "tenant", AuthorityID: id, AuthorityVersion: version, CellID: cell, SignedEnvelope: []byte{}}); err != nil {
			t.Fatal(err)
		}
	}
	cells := []string{"cell-a", "cell-b", "cell-c"}
	// Obsolete: version 1 is still queued for three cells after version 2 became current.
	insert("obsolete", 1, time.Hour)
	for _, cell := range cells {
		enqueue("obsolete", 1, cell)
	}
	insert("obsolete", 2, time.Hour)
	// Expired: the current version ran out before any cell got it.
	insert("expired", 1, -time.Minute)
	for _, cell := range cells {
		enqueue("expired", 1, cell)
	}

	server := &CommodoreServer{db: db, logger: logging.NewLogger()}
	server.sweepMediaAuthorityDeliveryQueue(ctx, 2)
	var queued int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM commodore.media_authority_deliveries WHERE status IN ('pending', 'delivering')`).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 0 {
		t.Fatalf("one sweep with a batch of 2 left %d of 6 undeliverable deliveries queued", queued)
	}
}

// A target parked after exhausting its transient attempts gets a few more when
// reconciliation re-arms it, not a fresh budget: a long outage then costs a
// few attempts per reconcile pass. A target parked for any other reason is
// re-armed with its attempts reset, since only its source changing can fix it.
func testMediaAuthorityParkedRearmBudget(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	q := commodoredb.New(db)
	exhausted := commodoredb.LiveStreamMediaAuthorityTarget("75000000-0000-0000-0000-000000000001")
	malformed := commodoredb.LiveStreamMediaAuthorityTarget("75000000-0000-0000-0000-000000000002")
	for _, target := range []commodoredb.MediaAuthorityTarget{exhausted, malformed} {
		if err := q.EnqueueMediaAuthorityEvent(ctx, target, obligationTenantID, "media_object:live_stream:"+target.ObjectID()+":stream_changed", "commodore", "e1"); err != nil {
			t.Fatal(err)
		}
	}
	park := func(target commodoredb.MediaAuthorityTarget, reason string, attempts int) {
		t.Helper()
		if _, err := db.ExecContext(ctx, `UPDATE commodore.media_authority_refresh_obligations SET status = 'parked', park_reason = $2, attempts = $3 WHERE target_key = $1`, target.Key, reason, attempts); err != nil {
			t.Fatal(err)
		}
	}
	park(exhausted, "exhausted", mediaAuthorityMaxTransientAttempts)
	park(malformed, "malformed_authority", 1)

	server := &CommodoreServer{db: db, logger: logging.NewLogger()}
	if rearmed, err := server.rearmParkedMediaAuthorityObligations(ctx); err != nil || rearmed != 2 {
		t.Fatalf("re-armed %d parked targets (err %v), want 2", rearmed, err)
	}
	for target, want := range map[commodoredb.MediaAuthorityTarget]int32{
		exhausted: mediaAuthorityMaxTransientAttempts - mediaAuthorityRearmAttempts,
		malformed: 0,
	} {
		row, ok := readObligation(t, ctx, db, commodoredb.MediaAuthorityLaneEvent, target.Key)
		if !ok || row.status != "pending" || row.parkReason.Valid || row.attempts != want {
			t.Fatalf("%s after re-arm = %+v (found %v), want pending with %d attempts used", target.Key, row, ok, want)
		}
	}
}

// A stream's playback policy is part of the stream's authority and of the
// authorities of its recordings, their chapters and its clips. Changing it
// refreshes those and nothing else of the tenant; changing a VOD's refreshes
// only the VOD.
func testMediaAuthorityPlaybackPolicyChangeScope(t *testing.T, db *sql.DB) {
	t.Helper()
	const (
		tenantID    = "10000000-0000-4000-8000-0000000000a1"
		userID      = "20000000-0000-4000-8000-0000000000a1"
		streamID    = "30000000-0000-4000-8000-0000000000a1"
		otherStream = "30000000-0000-4000-8000-0000000000a2"
		vodHash     = "scopevodhash0000000000000000001"
		chapterHash = "scopechapterhash000000000000001"
		dvrHash     = "scopedvrhash0000000000000000001"
	)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, ctxkeys.KeyUserID, userID)
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, tenantID)
	q := commodoredb.New(db)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	insertID := func(query string, args ...any) string {
		t.Helper()
		var id string
		if err := db.QueryRowContext(ctx, query, args...).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	exec(`INSERT INTO commodore.streams (id, tenant_id, user_id, stream_key, playback_id, internal_name, title)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'scope-key', 'ScopeStream', 'scope-stream', 'title')`, streamID, tenantID, userID)
	exec(`INSERT INTO commodore.streams (id, tenant_id, user_id, stream_key, playback_id, internal_name, title)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'scope-other-key', 'ScopeOther', 'scope-other', 'other')`, otherStream, tenantID, userID)
	dvrID := insertID(`INSERT INTO commodore.dvr_recordings (tenant_id, user_id, stream_id, dvr_hash, internal_name, playback_id, stream_internal_name)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, 'scope-dvr', 'ScopeDVR', 'scope-stream') RETURNING id::text`, tenantID, userID, streamID, dvrHash)
	exec(`INSERT INTO commodore.dvr_chapter_playback (chapter_id, tenant_id, playback_id, artifact_hash, dvr_hash)
		VALUES ('scopechapter00000000000000000001', $1::uuid, 'ScopeChapter', $2, $3)`, tenantID, chapterHash, dvrHash)
	chapterID := insertID(`INSERT INTO commodore.vod_assets (tenant_id, user_id, stream_id, vod_hash, internal_name, playback_id, filename,
		    origin_type, origin_id, library_visible, requires_auth, playback_policy)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, 'scope-chapter', 'ScopeChapter', 'chapter.mkv',
		        'dvr_chapter', 'scopechapter00000000000000000001', false, false, '{"type":"public"}'::jsonb) RETURNING id::text`, tenantID, userID, streamID, chapterHash)
	clipID := insertID(`INSERT INTO commodore.clips (tenant_id, user_id, stream_id, clip_hash, internal_name, playback_id, start_time, duration)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'scopecliphash000000000000000001', 'scope-clip', 'ScopeClip', 0, 1000) RETURNING id::text`, tenantID, userID, streamID)
	otherClipID := insertID(`INSERT INTO commodore.clips (tenant_id, user_id, stream_id, clip_hash, internal_name, playback_id, start_time, duration)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'scopecliphash000000000000000002', 'scope-other-clip', 'ScopeOtherClip', 0, 1000) RETURNING id::text`, tenantID, userID, otherStream)
	vodID := insertID(`INSERT INTO commodore.vod_assets (tenant_id, user_id, vod_hash, internal_name, playback_id, filename)
		VALUES ($1::uuid, $2::uuid, $3, 'scope-vod', 'ScopeVOD', 'video.mp4') RETURNING id::text`, tenantID, userID, vodHash)
	exec(`INSERT INTO commodore.signing_keys (tenant_id, kid, name, public_key_pem, status)
		VALUES ($1::uuid, 'scope-kid', 'active', 'public-pem', 'active')`, tenantID)
	insertHeldMediaObjectAuthority(t, ctx, q, sharedauthority.LiveStreamAuthorityID(streamID), false)
	insertHeldMediaObjectAuthority(t, ctx, q, sharedauthority.LiveStreamAuthorityID(otherStream), false)
	for _, id := range []string{dvrID, chapterID, clipID, otherClipID, vodID} {
		insertHeldMediaObjectAuthority(t, ctx, q, sharedauthority.ArtifactAuthorityID(id), false)
	}
	refreshed := func() []string {
		t.Helper()
		rows, err := db.QueryContext(ctx, `SELECT target_key FROM commodore.media_authority_refresh_obligations ORDER BY target_key`)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rows.Close() }()
		var keys []string
		for rows.Next() {
			var key string
			if err := rows.Scan(&key); err != nil {
				t.Fatal(err)
			}
			keys = append(keys, key)
		}
		return keys
	}
	clear := func() { exec(`DELETE FROM commodore.media_authority_refresh_obligations`) }
	want := func(ids ...commodoredb.MediaAuthorityTarget) []string {
		keys := make([]string, 0, len(ids))
		for _, id := range ids {
			keys = append(keys, id.Key)
		}
		sort.Strings(keys)
		return keys
	}
	server := &CommodoreServer{db: db, logger: logging.NewLogger(), tokenHasher: testTokenHasher(t)}

	clear()
	if _, err := server.SetPlaybackPolicy(ctx, &commodorepb.SetPlaybackPolicyRequest{
		StreamId: streamID, Type: "jwt", Jwt: &commodorepb.PlaybackJwtPolicy{AllowedKids: []string{"scope-kid"}},
	}); err != nil {
		t.Fatalf("set stream policy: %v", err)
	}
	if got, expected := refreshed(), want(commodoredb.LiveStreamMediaAuthorityTarget(streamID), commodoredb.ArtifactMediaAuthorityTarget(dvrID),
		commodoredb.ArtifactMediaAuthorityTarget(chapterID), commodoredb.ArtifactMediaAuthorityTarget(clipID)); fmt.Sprint(got) != fmt.Sprint(expected) {
		t.Fatalf("a stream policy change refreshed %v, want the stream and its children %v", got, expected)
	}

	clear()
	if _, err := server.SetPlaybackPolicy(ctx, &commodorepb.SetPlaybackPolicyRequest{VodAssetId: vodHash, Type: "jwt", Jwt: &commodorepb.PlaybackJwtPolicy{AllowedKids: []string{"scope-kid"}}}); err != nil {
		t.Fatalf("set VOD policy: %v", err)
	}
	if got, expected := refreshed(), want(commodoredb.ArtifactMediaAuthorityTarget(vodID)); fmt.Sprint(got) != fmt.Sprint(expected) {
		t.Fatalf("a VOD policy change refreshed %v, want only the VOD %v", got, expected)
	}
}

// A cell's Foghorn answering the capability read-back proves the cell
// reachable. Deliveries to it that were backed off while it was not are made
// due at once instead of waiting out their backoff; another cell's are not.
func testMediaAuthorityCellReachableMakesDeliveriesDue(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	q := commodoredb.New(db)
	now := time.Now().UTC()
	for _, cell := range []string{"cell-a", "cell-b"} {
		id := "reachable-" + cell
		if err := q.InsertMediaAuthorityVersion(ctx, commodoredb.InsertMediaAuthorityVersionParams{
			AuthorityKind: "tenant", AuthorityID: id, AuthorityVersion: 1, PayloadSchemaVersion: 1,
			Payload: []byte{}, PayloadSha256: make([]byte, 32), SourceRevisions: []byte("[]"),
			IssuedAt: now, RefreshAfter: now.Add(30 * time.Minute), ValidUntil: now.Add(time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := q.UpsertCurrentMediaAuthority(ctx, commodoredb.UpsertCurrentMediaAuthorityParams{AuthorityKind: "tenant", AuthorityID: id, AuthorityVersion: 1}); err != nil {
			t.Fatal(err)
		}
		if _, err := q.EnqueueMediaAuthorityDelivery(ctx, commodoredb.EnqueueMediaAuthorityDeliveryParams{AuthorityKind: "tenant", AuthorityID: id, AuthorityVersion: 1, CellID: cell, SignedEnvelope: []byte{}}); err != nil {
			t.Fatal(err)
		}
		claimed, err := q.ClaimMediaAuthorityDeliveries(ctx, commodoredb.ClaimMediaAuthorityDeliveriesParams{CellID: cell, LeaseMs: 60000, BatchSize: 10})
		if err != nil || len(claimed) != 1 {
			t.Fatalf("claim %s: %d rows, err %v", cell, len(claimed), err)
		}
		if _, err := q.RecordMediaAuthorityDeliveryFailure(ctx, commodoredb.RecordMediaAuthorityDeliveryFailureParams{
			NextAttemptAt: now.Add(time.Hour), LastError: sql.NullString{String: "no foghorn instance discovered for cluster " + cell, Valid: true},
			AuthorityKind: "tenant", AuthorityID: id, AuthorityVersion: 1, CellID: cell,
		}); err != nil {
			t.Fatal(err)
		}
	}
	due := func(cell string) bool {
		t.Helper()
		var isDue bool
		if err := db.QueryRowContext(ctx, `SELECT next_attempt_at <= NOW() FROM commodore.media_authority_deliveries WHERE cell_id = $1 AND status = 'pending'`, cell).Scan(&isDue); err != nil {
			t.Fatal(err)
		}
		return isDue
	}
	if due("cell-a") || due("cell-b") {
		t.Fatal("backed-off deliveries are due before any cell was proven reachable")
	}

	cell := &capabilityCellFake{capability: &foghornpb.MediaCellPlacementCapability{SupportedSchemaVersions: []uint32{1, 2}, LiveReplicas: 1}}
	server := newCapabilityProbeServer(t, db, map[string]*capabilityCellFake{"cell-a": cell})
	serviceCtx := context.WithValue(ctx, ctxkeys.KeyAuthType, "service")
	if _, err := server.ReportMediaCellPlacementCapability(serviceCtx, &commodorepb.ReportMediaCellPlacementCapabilityRequest{ControlCellId: "cell-a"}); err != nil {
		t.Fatal(err)
	}
	if !due("cell-a") {
		t.Fatal("a delivery backed off for cell-a is still waiting out its backoff after cell-a answered")
	}
	if due("cell-b") {
		t.Fatal("cell-a answering made cell-b's backed-off delivery due")
	}
}
