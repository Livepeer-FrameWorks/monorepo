//go:build schema_verify

package grpc

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"testing"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	clusterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	"google.golang.org/protobuf/proto"
)

const (
	stickyStreamID  = "75000000-0000-0000-0000-000000000001"
	stickyStreamKey = "sticky-origin-key"
)

// stickyOriginFixture is a tenant granted three media clusters in one cell,
// preferring media-a, with one push stream whose authority has been published
// once, before any ingest.
type stickyOriginFixture struct {
	t        *testing.T
	ctx      context.Context
	db       *sql.DB
	compiler *CommodoreServer
	target   commodoredb.MediaAuthorityTarget
}

func newStickyOriginFixture(t *testing.T, db *sql.DB) *stickyOriginFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	f := &stickyOriginFixture{t: t, ctx: ctx, db: db, target: commodoredb.LiveStreamMediaAuthorityTarget(stickyStreamID),
		compiler: &CommodoreServer{db: db, logger: logging.NewLogger(), mediaAuthorityKeyID: "sticky-origin", mediaAuthorityPrivateKey: ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))}}
	tenant := obligationTenantPayload()
	tenant.PreferredClusterId = "media-a"
	grant := tenant.EffectiveClusterGrants[0]
	tenant.EffectiveClusterGrants = nil
	for _, clusterID := range []string{"media-a", "media-b", "media-c"} {
		clusterGrant := proto.CloneOf(grant)
		clusterGrant.ClusterId = clusterID
		clusterGrant.AccessSource = clusterpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_PLATFORM_TIER
		tenant.EffectiveClusterGrants = append(tenant.EffectiveClusterGrants, clusterGrant)
	}
	now := time.Now().UTC()
	revisions := []*mediapb.AuthoritySourceRevision{{Service: "commodore", Revision: "sticky-origin"}}
	if err := f.compiler.persistTenantAuthority(withMediaAuthorityInUse(ctx, true), tenant, []string{"cell-a"}, revisions, now, now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO commodore.streams (id, tenant_id, user_id, stream_key, playback_id, internal_name, title, ingest_mode, requires_auth, created_at)
		VALUES ($1, $2, $2, $3, 'sticky-playback', 'sticky-internal', 'sticky', 'push', FALSE, NOW() - INTERVAL '60 days')`,
		stickyStreamID, obligationTenantID, stickyStreamKey); err != nil {
		t.Fatal(err)
	}
	f.compile()
	if origin, outage := f.origin(); origin != "media-a" || outage != "media-a" {
		t.Fatalf("before any ingest: origin %q outage owner %q, want the preferred cluster media-a", origin, outage)
	}
	return f
}

// compile runs the stream's compile as the obligation worker does for a
// refresh, whatever enqueued it.
func (f *stickyOriginFixture) compile() {
	f.t.Helper()
	if err := f.compiler.compileLiveStreamAuthority(withMediaAuthorityInUse(f.ctx, true), stickyStreamID); err != nil {
		f.t.Fatalf("compile: %v", err)
	}
}

// refreshes is how many times anything asked for the stream's authority to be
// recompiled.
func (f *stickyOriginFixture) refreshes() int64 {
	f.t.Helper()
	row, _ := readObligation(f.t, f.ctx, f.db, commodoredb.MediaAuthorityLaneEvent, f.target.Key)
	return row.revision
}

func (f *stickyOriginFixture) version() int64 {
	f.t.Helper()
	current, err := commodoredb.New(f.db).GetCurrentMediaAuthorityPublication(f.ctx, commodoredb.GetCurrentMediaAuthorityPublicationParams{AuthorityKind: "media_object", AuthorityID: sharedauthority.LiveStreamAuthorityID(stickyStreamID)})
	if err != nil {
		f.t.Fatal(err)
	}
	return current.AuthorityVersion
}

func (f *stickyOriginFixture) origin() (origin, outageOwner string) {
	f.t.Helper()
	current, err := commodoredb.New(f.db).GetCurrentMediaAuthorityPayload(f.ctx, commodoredb.GetCurrentMediaAuthorityPayloadParams{AuthorityKind: "media_object", AuthorityID: sharedauthority.LiveStreamAuthorityID(stickyStreamID)})
	if err != nil {
		f.t.Fatal(err)
	}
	payload := &mediapb.MediaObjectAuthority{}
	if err := proto.Unmarshal(current.Payload, payload); err != nil {
		f.t.Fatal(err)
	}
	if len(payload.GetLiveStream().GetPublishingCredentialSha256()) == 0 {
		f.t.Fatal("signed authority carries no publishing credential for its outage owner")
	}
	return payload.GetOriginClusterId(), payload.GetLiveStream().GetOutageIngestClusterId()
}

// startIngest takes the push claim the way ValidateStreamKey does.
func (f *stickyOriginFixture) startIngest(clusterID, claimToken string) {
	f.t.Helper()
	if _, err := commodoredb.New(f.db).AcquireIngestClaim(f.ctx, commodoredb.AcquireIngestClaimParams{
		ClusterID: sql.NullString{String: clusterID, Valid: true}, ClaimToken: sql.NullString{String: claimToken, Valid: true},
		LeaseSeconds: int64(activeIngestLease.Seconds()), LookupStreamKey: stickyStreamKey,
	}); err != nil {
		f.t.Fatalf("claim ingest on %s: %v", clusterID, err)
	}
}

// stopIngest releases the claim the way Foghorn's placement sync does when the
// publisher disconnects.
func (f *stickyOriginFixture) stopIngest(clusterID, claimToken string) {
	f.t.Helper()
	released, _, err := commodoredb.New(f.db).ApplyActiveIngestPlacementBatch(f.ctx, commodoredb.ActiveIngestPlacementBatchParams{
		TenantIDs: []string{obligationTenantID}, InternalNames: []string{"sticky-internal"}, ClaimTokens: []string{claimToken}, ClusterIDs: []string{clusterID},
		LeaseSeconds: int64(activeIngestLease.Seconds()),
	})
	if err != nil || released != 1 {
		f.t.Fatalf("release ingest on %s: released %d, err %v", clusterID, released, err)
	}
}

// A publisher that stops and reconnects to the cluster it last ingested on
// changes nothing the stream's authority says, so neither the stop nor the
// reconnect asks for a recompile or publishes a version to every cell.
func TestMediaAuthorityIngestRestartSameClusterNoRepublish_RealPG(t *testing.T) {
	testMediaAuthorityIngestRestartSameClusterNoRepublish(t, startCommodoreRealPG(t))
}

func TestMediaAuthorityIngestRestartSameClusterNoRepublish_RealYugabyte(t *testing.T) {
	testMediaAuthorityIngestRestartSameClusterNoRepublish(t, startPlacementDeliveryYugabyte(t, "sticky_origin"))
}

func testMediaAuthorityIngestRestartSameClusterNoRepublish(t *testing.T, db *sql.DB) {
	f := newStickyOriginFixture(t, db)
	f.startIngest("media-b", "publisher-1")
	f.compile()
	refreshes, version := f.refreshes(), f.version()
	previous := "publisher-1"
	for _, next := range []string{"publisher-2", "publisher-3"} {
		f.stopIngest("media-b", previous)
		f.compile()
		f.startIngest("media-b", next)
		f.compile()
		previous = next
	}
	f.stopIngest("media-b", previous)
	f.compile()
	if got := f.refreshes(); got != refreshes {
		t.Errorf("stop/reconnect on media-b asked for %d recompiles, want none", got-refreshes)
	}
	if got := f.version(); got != version {
		t.Errorf("stop/reconnect on media-b published %d versions, want none", got-version)
	}
}

// Moving ingest to another cluster moves the origin once: one recompile and one
// published version, and the stop that follows publishes nothing.
func TestMediaAuthorityIngestMoveClusterRepublishesOnce_RealPG(t *testing.T) {
	testMediaAuthorityIngestMoveClusterRepublishesOnce(t, startCommodoreRealPG(t))
}

func TestMediaAuthorityIngestMoveClusterRepublishesOnce_RealYugabyte(t *testing.T) {
	testMediaAuthorityIngestMoveClusterRepublishesOnce(t, startPlacementDeliveryYugabyte(t, "sticky_origin"))
}

func testMediaAuthorityIngestMoveClusterRepublishesOnce(t *testing.T, db *sql.DB) {
	f := newStickyOriginFixture(t, db)
	f.startIngest("media-b", "publisher-1")
	f.compile()
	f.stopIngest("media-b", "publisher-1")
	f.compile()
	refreshes, version := f.refreshes(), f.version()

	f.startIngest("media-c", "publisher-2")
	f.compile()
	f.stopIngest("media-c", "publisher-2")
	f.compile()
	if got := f.refreshes() - refreshes; got != 1 {
		t.Errorf("moving ingest to media-c asked for %d recompiles, want 1", got)
	}
	if got := f.version() - version; got != 1 {
		t.Errorf("moving ingest to media-c published %d versions, want 1", got)
	}
	if origin, outage := f.origin(); origin != "media-c" || outage != "media-c" {
		t.Errorf("after the move: origin %q outage owner %q, want media-c", origin, outage)
	}
}

// After the publisher disconnects, the cell's signed outage ingest owner is the
// cluster it was ingesting on, not the tenant's preferred cluster: a reconnect
// during a control-plane outage is admitted where the stream actually lives.
func TestMediaAuthorityOutageOwnerIsStickyOriginAfterDisconnect_RealPG(t *testing.T) {
	testMediaAuthorityOutageOwnerIsStickyOriginAfterDisconnect(t, startCommodoreRealPG(t))
}

func TestMediaAuthorityOutageOwnerIsStickyOriginAfterDisconnect_RealYugabyte(t *testing.T) {
	testMediaAuthorityOutageOwnerIsStickyOriginAfterDisconnect(t, startPlacementDeliveryYugabyte(t, "sticky_origin"))
}

func testMediaAuthorityOutageOwnerIsStickyOriginAfterDisconnect(t *testing.T, db *sql.DB) {
	f := newStickyOriginFixture(t, db)
	f.startIngest("media-b", "publisher-1")
	f.compile()
	if origin, outage := f.origin(); origin != "media-b" || outage != "media-b" {
		t.Fatalf("while ingesting: origin %q outage owner %q, want media-b", origin, outage)
	}
	f.stopIngest("media-b", "publisher-1")
	f.compile()
	if origin, outage := f.origin(); origin != "media-b" || outage != "media-b" {
		t.Fatalf("after the publisher left: origin %q outage owner %q, want the sticky origin media-b", origin, outage)
	}
}
