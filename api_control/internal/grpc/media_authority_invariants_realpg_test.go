//go:build schema_verify

package grpc

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Invariants that keep media authority correct when an object is not kept in
// every cell. Each subtest fails on the implementation that preceded its fix.
func TestMediaAuthorityConvergenceInvariants_RealPG(t *testing.T) {
	testMediaAuthorityConvergenceInvariants(t, startCommodoreRealPG(t), false)
}

func TestMediaAuthorityConvergenceInvariants_RealYugabyte(t *testing.T) {
	testMediaAuthorityConvergenceInvariants(t, startPlacementDeliveryYugabyte(t, "authority_convergence"), true)
}

func testMediaAuthorityConvergenceInvariants(t *testing.T, db *sql.DB, yugabyte bool) {
	ctx := context.Background()
	q := commodoredb.New(db)
	s := &CommodoreServer{db: db, logger: logging.NewLogger(), mediaAuthorityKeyID: "invariants",
		mediaAuthorityPrivateKey: ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))}
	revisions := []*mediapb.AuthoritySourceRevision{{Service: "commodore", Revision: "invariants"}}

	// A placement policy without commercial predicates binds the object to its
	// captured tenant, and nothing more: it gets the long validity every cell
	// accepts. A quoted one stays bounded by its quotes.
	t.Run("placement object without quotes gets long validity", func(t *testing.T) {
		if _, err := q.UpsertMediaCellPlacementCapability(ctx, commodoredb.UpsertMediaCellPlacementCapabilityParams{
			CellID: "cell-a", MaxSchemaVersion: 2, EnforcementReady: true, LiveReplicas: 1, LongValidityReady: true, UseReportsReady: true,
		}); err != nil {
			t.Fatal(err)
		}
		lifetime := func(tenant *mediapb.TenantAuthority, object *mediapb.MediaObjectAuthority, authorityID string) time.Duration {
			t.Helper()
			now := time.Now().UTC()
			if err := s.persistTenantAuthority(ctx, tenant, []string{"cell-a"}, revisions, now, now.Add(24*time.Hour)); err != nil {
				t.Fatal(err)
			}
			long := withMediaAuthorityLongValidity(ctx, now.Add(sharedauthority.MaxMediaObjectValidity))
			if err := s.persistMediaObjectAuthority(long, authorityID, object, []string{"cell-a"}, revisions, now, now.Add(24*time.Hour)); err != nil {
				t.Fatal(err)
			}
			current, err := q.GetCurrentMediaAuthorityPublication(ctx, commodoredb.GetCurrentMediaAuthorityPublicationParams{AuthorityKind: "media_object", AuthorityID: authorityID})
			if err != nil {
				t.Fatal(err)
			}
			return current.ValidUntil.Sub(current.IssuedAt)
		}
		tenant, object := commercialAuthorityFixture()
		tenant.BillingModel = mediapb.TenantBillingModel_TENANT_BILLING_MODEL_POSTPAID
		if got := lifetime(tenant, object, "live_stream:stream"); got < sharedauthority.MaxMediaObjectValidity-time.Hour {
			t.Fatalf("unquoted schema-2 object lifetime = %s, want about thirty days", got)
		}

		quotedTenant, quotedObject := quotedCommercialAuthorityFixture()
		quotedTenant.BillingModel = mediapb.TenantBillingModel_TENANT_BILLING_MODEL_POSTPAID
		quotedTenant.TenantId, quotedObject.TenantId = "81000000-0000-4000-8000-000000000002", "81000000-0000-4000-8000-000000000002"
		quotedObject.InternalName, quotedObject.PlaybackId = "quoted-internal", "quoted-playback"
		quotedObject.GetLiveStream().StreamId = "quoted"
		s.authorityCommercialSource = commercialSourceFunc(func(_ context.Context, request *pb.CommercialQuoteRequest) (*pb.CommercialQuoteResponse, error) {
			return commercialResponse(quotedTenant, request)
		})
		defer func() { s.authorityCommercialSource = nil }()
		if got := lifetime(quotedTenant, quotedObject, "live_stream:quoted"); got > time.Minute {
			t.Fatalf("quoted object lifetime = %s, want bounded by its thirty-second quote", got)
		}
	})

	// The object above holds a thirty-day unquoted version. It goes unused and
	// its policy becomes quoted: the correction must be signable, so it cannot
	// outlive its thirty-second quote, and it is renewed until it lasts as long
	// as the thirty-day copy a cell may still hold.
	t.Run("a cooling correction keeps a shorter quote", func(t *testing.T) {
		tenant, object := quotedCommercialAuthorityFixture()
		tenant.BillingModel = mediapb.TenantBillingModel_TENANT_BILLING_MODEL_POSTPAID
		// The quoted rules are a new tenant policy revision; the object binds to it.
		tenant.MediaPlacement.Revision, object.PlacementTenantRevision = 4, 4
		s.authorityCommercialSource = commercialSourceFunc(func(_ context.Context, request *pb.CommercialQuoteRequest) (*pb.CommercialQuoteResponse, error) {
			return commercialResponse(tenant, request)
		})
		defer func() { s.authorityCommercialSource = nil }()
		now := time.Now().UTC()
		if err := s.persistTenantAuthority(ctx, tenant, []string{"cell-a"}, revisions, now, now.Add(24*time.Hour)); err != nil {
			t.Fatal(err)
		}
		unused := withMediaAuthorityLongValidity(withMediaAuthorityInUse(ctx, false), now.Add(sharedauthority.MaxMediaObjectValidity))
		if err := s.persistMediaObjectAuthority(unused, "live_stream:stream", object, []string{"cell-a"}, revisions, now, now.Add(24*time.Hour)); err != nil {
			t.Fatalf("cooling correction under a new quote: %v", err)
		}
		current, err := q.GetCurrentMediaAuthorityPublication(ctx, commodoredb.GetCurrentMediaAuthorityPublicationParams{AuthorityKind: "media_object", AuthorityID: "live_stream:stream"})
		if err != nil || current.ValidUntil.Sub(current.IssuedAt) > time.Minute {
			t.Fatalf("correction lifetime = %s (err %v), want bounded by the quote", current.ValidUntil.Sub(current.IssuedAt), err)
		}
		if bound := renewalBoundVersion(t, ctx, db, "live_stream:stream"); bound != current.AuthorityVersion {
			t.Fatalf("renewal bound to version %d, want the correction %d kept renewed", bound, current.AuthorityVersion)
		}
	})

	// A placement object is compiled through commercial preparation, which
	// captures its tenant. A correction on a lapsed tenant must get through it,
	// quoted or not; a quoted one is still bounded by its quote.
	t.Run("a placement correction gets through on a lapsed tenant", func(t *testing.T) {
		// The quoted case is an unquoted object whose own placement rules become
		// quoted: a quote-bound original would itself run out within a minute,
		// leaving nothing to correct.
		quotedRules := &pb.Rules{SchemaVersion: 1, Constraints: &pb.Constraints{Allow: &pb.SelectorSet{Any: []*pb.Selector{{Charging: []pb.Charging{pb.Charging_CHARGING_RATED}}}}}}
		for i, quoted := range []bool{false, true} {
			tenant, object := commercialAuthorityFixture()
			tenant.BillingModel = mediapb.TenantBillingModel_TENANT_BILLING_MODEL_POSTPAID
			tenant.TenantId = fmt.Sprintf("81000000-0000-4000-8000-0000000000a%d", i)
			object.TenantId = tenant.TenantId
			object.InternalName, object.PlaybackId = fmt.Sprintf("lapsed-internal-%d", i), fmt.Sprintf("lapsed-playback-%d", i)
			object.GetLiveStream().StreamId = fmt.Sprintf("lapsed-%d", i)
			authorityID := "live_stream:" + object.GetLiveStream().StreamId
			s.authorityCommercialSource = commercialSourceFunc(func(_ context.Context, request *pb.CommercialQuoteRequest) (*pb.CommercialQuoteResponse, error) {
				return commercialResponse(tenant, request)
			})
			now := time.Now().UTC()
			if err := s.persistTenantAuthority(ctx, tenant, []string{"cell-a"}, revisions, now, now.Add(24*time.Hour)); err != nil {
				t.Fatal(err)
			}
			if err := s.persistMediaObjectAuthority(withMediaAuthorityLongValidity(ctx, now.Add(sharedauthority.MaxMediaObjectValidity)), authorityID, object, []string{"cell-a"}, revisions, now, now.Add(24*time.Hour)); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(ctx, `
				UPDATE commodore.media_authority_versions SET issued_at = issued_at - INTERVAL '3 days', refresh_after = refresh_after - INTERVAL '3 days', valid_until = valid_until - INTERVAL '3 days'
				WHERE authority_kind = 'tenant' AND authority_id = $1`, tenant.TenantId); err != nil {
				t.Fatal(err)
			}
			if quoted {
				object.MediaPlacement.Revision++
				object.MediaPlacement.Serve, object.MediaPlacement.Ingest = quotedRules, quotedRules
			} else {
				object.PlaybackPolicy.Kind = mediapb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_DENY
			}
			issued := time.Now().UTC()
			correction := withMediaAuthorityLapsedParent(withMediaAuthorityLongValidity(withMediaAuthorityInUse(ctx, false), issued.Add(sharedauthority.MaxMediaObjectValidity)))
			if err := s.persistMediaObjectAuthority(correction, authorityID, object, []string{"cell-a"}, revisions, issued, issued.Add(24*time.Hour)); err != nil {
				t.Fatalf("quoted=%v: correction on a lapsed tenant: %v", quoted, err)
			}
			current, err := q.GetCurrentMediaAuthorityPublication(ctx, commodoredb.GetCurrentMediaAuthorityPublicationParams{AuthorityKind: "media_object", AuthorityID: authorityID})
			if err != nil || current.AuthorityVersion < 2 || !current.ValidUntil.After(time.Now()) {
				t.Fatalf("quoted=%v: correction version %d valid until %s (err %v)", quoted, current.AuthorityVersion, current.ValidUntil, err)
			}
			if quoted && current.ValidUntil.Sub(current.IssuedAt) > time.Minute {
				t.Fatalf("quoted correction lasts %s, want bounded by its quote", current.ValidUntil.Sub(current.IssuedAt))
			}
		}
		s.authorityCommercialSource = nil
	})

	// A shorter-lived replacement can run out while a cell that never received
	// it still holds the longer version it replaced. Such a cell still needs a
	// correction, so nothing may conclude that no copy is left.
	t.Run("an expired replacement does not hide a valid predecessor", func(t *testing.T) {
		object := useStreamPayload("history")
		id := sharedauthority.LiveStreamAuthorityID(useStreamID)
		now := time.Now().UTC()
		if err := s.persistMediaObjectAuthority(withMediaAuthorityLongValidity(ctx, now.Add(sharedauthority.MaxMediaObjectValidity)), id, object, []string{"cell-a"}, revisions, now, now.Add(24*time.Hour)); err != nil {
			t.Fatal(err)
		}
		object.PlaybackPolicy.Kind = mediapb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_DENY
		if err := s.persistMediaObjectAuthority(ctx, id, object, []string{"cell-a"}, revisions, now, now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `
			UPDATE commodore.media_authority_versions
			SET issued_at = issued_at - INTERVAL '2 hours', refresh_after = refresh_after - INTERVAL '2 hours', valid_until = valid_until - INTERVAL '2 hours'
			WHERE authority_kind = 'media_object' AND authority_id = $1 AND authority_version = 2`, id); err != nil {
			t.Fatal(err)
		}
		gone, err := mediaAuthorityHasNoCopyLeft(ctx, q, "media_object", id, time.Now().UTC())
		if err != nil || gone {
			t.Fatalf("no copy left = %v (err %v) while cell-a may still hold the thirty-day predecessor", gone, err)
		}
		// An unused authority in that state still gets its correction, with the
		// validity the longest copy has (the compile offers the long validity, as
		// the worker does).
		issued := time.Now().UTC()
		unused := withMediaAuthorityLongValidity(withMediaAuthorityInUse(ctx, false), issued.Add(sharedauthority.MaxMediaObjectValidity))
		object.PlaybackPolicy.Kind = mediapb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_PUBLIC
		if err = s.persistMediaObjectAuthority(unused, id, object, []string{"cell-a"}, revisions, issued, issued.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		current, err := q.GetCurrentMediaAuthorityPublication(ctx, commodoredb.GetCurrentMediaAuthorityPublicationParams{AuthorityKind: "media_object", AuthorityID: id})
		if err != nil || current.AuthorityVersion != 3 || time.Until(current.ValidUntil) < sharedauthority.MaxMediaObjectValidity-2*time.Hour {
			t.Fatalf("correction: version %d valid until %s (err %v), want version 3 lasting as long as the predecessor", current.AuthorityVersion, current.ValidUntil, err)
		}
	})

	// The same state, but the compile finds the expired replacement unchanged.
	// Delivery never sends an expired version, so the correction must be issued
	// again rather than treated as already published.
	t.Run("an expired unchanged correction is issued again", func(t *testing.T) {
		const streamID = "73000000-0000-0000-0000-000000000002"
		object := useStreamPayload("unchanged")
		object.GetLiveStream().StreamId = streamID
		id := sharedauthority.LiveStreamAuthorityID(streamID)
		now := time.Now().UTC()
		if err := s.persistMediaObjectAuthority(withMediaAuthorityLongValidity(ctx, now.Add(sharedauthority.MaxMediaObjectValidity)), id, object, []string{"cell-a"}, revisions, now, now.Add(24*time.Hour)); err != nil {
			t.Fatal(err)
		}
		object.PlaybackPolicy.Kind = mediapb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_DENY
		if err := s.persistMediaObjectAuthority(ctx, id, object, []string{"cell-a"}, revisions, now, now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `
			UPDATE commodore.media_authority_versions
			SET issued_at = issued_at - INTERVAL '2 hours', refresh_after = refresh_after - INTERVAL '2 hours', valid_until = valid_until - INTERVAL '2 hours'
			WHERE authority_kind = 'media_object' AND authority_id = $1 AND authority_version = 2`, id); err != nil {
			t.Fatal(err)
		}
		issued := time.Now().UTC()
		if err := s.persistMediaObjectAuthority(withMediaAuthorityInUse(ctx, false), id, object, []string{"cell-a"}, revisions, issued, issued.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		current, err := q.GetCurrentMediaAuthorityPublication(ctx, commodoredb.GetCurrentMediaAuthorityPublicationParams{AuthorityKind: "media_object", AuthorityID: id})
		if err != nil || current.AuthorityVersion != 3 || !current.ValidUntil.After(time.Now()) || current.ValidUntil.After(issued.Add(time.Hour+time.Second)) {
			t.Fatalf("after the expired correction: version %d valid until %s (err %v), want version 3 valid for the compiled hour", current.AuthorityVersion, current.ValidUntil, err)
		}
		if bound := renewalBoundVersion(t, ctx, db, id); bound != 3 {
			t.Fatalf("renewal bound to version %d, want 3: the correction must stay valid while the thirty-day copy is", bound)
		}
		// Once the only cell acknowledges the correction it no longer holds the
		// thirty-day copy, so there is nothing left to keep the correction for.
		if err = q.UpsertMediaAuthorityDistribution(ctx, commodoredb.UpsertMediaAuthorityDistributionParams{AuthorityKind: "media_object", AuthorityID: id, CellID: "cell-a", AuthorityVersion: 3}); err != nil {
			t.Fatal(err)
		}
		horizon, err := mediaAuthorityValidHorizon(ctx, q, "media_object", id)
		if err != nil || !horizon.Equal(current.ValidUntil) {
			t.Fatalf("horizon after the correction was acknowledged = %s (err %v), want the correction's own %s", horizon, err, current.ValidUntil)
		}
	})

	// A fetch builds the object on the current tenant version. When that version
	// has expired, the fetch compiles the tenant, even though an older tenant
	// version a cell may hold is still valid.
	t.Run("a fetch rebuilds an expired current tenant", func(t *testing.T) {
		const tenantID = "70000000-0000-0000-0000-0000000000b1"
		tenant := obligationTenantPayload()
		tenant.TenantId = tenantID
		now := time.Now().UTC()
		if err := s.persistTenantAuthority(ctx, tenant, []string{"cell-a"}, revisions, now, now.Add(24*time.Hour)); err != nil {
			t.Fatal(err)
		}
		tenant.PreferredClusterId = ""
		if err := s.persistTenantAuthority(ctx, tenant, []string{"cell-a"}, revisions, now, now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `
			UPDATE commodore.media_authority_versions
			SET issued_at = issued_at - INTERVAL '2 hours', refresh_after = refresh_after - INTERVAL '2 hours', valid_until = valid_until - INTERVAL '2 hours'
			WHERE authority_kind = 'tenant' AND authority_id = $1 AND authority_version = 2`, tenantID); err != nil {
			t.Fatal(err)
		}
		fetcher := &CommodoreServer{db: db, logger: logging.NewLogger(), mediaAuthorityKeyID: "invariants", mediaAuthorityPrivateKey: s.mediaAuthorityPrivateKey,
			authorityTenantSource: tenantCompileProbe{&tenantPlacementOwnerFixture{}}, authorityBillingSource: &tenantPlacementOwnerFixture{}}
		err := fetcher.compileFetchedMediaAuthority(ctx, newFetchedMediaObject(commodoredb.MediaAuthorityTargetLiveStream, "73000000-0000-0000-0000-000000000003", tenantID))
		if !errors.Is(err, errTenantCompileReached) {
			t.Fatalf("fetch with an expired current tenant = %v, want the tenant compiled first", err)
		}
	})

	t.Run("fetching a valid published tenant does not compile", func(t *testing.T) {
		const tenantID = "70000000-0000-0000-0000-0000000000b2"
		tenant := obligationTenantPayload()
		tenant.TenantId = tenantID
		now := time.Now().UTC()
		if err := s.persistTenantAuthority(ctx, tenant, []string{"cell-a"}, revisions, now, now.Add(24*time.Hour)); err != nil {
			t.Fatal(err)
		}
		fetcher := &CommodoreServer{db: db, logger: logging.NewLogger(), mediaAuthorityKeyID: "invariants", mediaAuthorityPrivateKey: s.mediaAuthorityPrivateKey,
			authorityTenantSource: tenantCompileProbe{&tenantPlacementOwnerFixture{}}, authorityBillingSource: &tenantPlacementOwnerFixture{}}
		response, err := fetcher.FetchMediaAuthority(context.WithValue(ctx, ctxkeys.KeyAuthType, "service"), &commodorepb.FetchMediaAuthorityRequest{
			ControlCellId: "cell-a", Lookup: &commodorepb.FetchMediaAuthorityRequest_TenantId{TenantId: tenantID},
		})
		if err != nil || len(response.GetSignedAuthorities()) != 1 {
			t.Fatalf("valid published authority was recompiled: %+v, %v", response, err)
		}
	})

	// The expired-in-use gauge runs every thirty seconds, so it must not read the
	// authorities nobody uses: its cost follows the recently used set.
	t.Run("the expired-in-use count reads no cold catalog", func(t *testing.T) {
		if yugabyte {
			t.Skip("PostgreSQL optimizer assertion; Yugabyte queue index contracts run separately")
		}
		for _, statement := range []string{
			`INSERT INTO commodore.media_authority_use (authority_kind, authority_id, tenant_id, last_used_at)
			SELECT 'media_object', 'live_stream:cold-' || n, '` + obligationTenantID + `'::uuid, NOW() - INTERVAL '60 days' FROM generate_series(1, 20000) AS n`,
			`INSERT INTO commodore.media_authority_refresh_obligations (target_key, lane, tenant_id, target_kind, status, expires_at, last_reason, last_source_service, last_source_event_id)
			SELECT 'media_object:live_stream:cold-' || n, 'object_deadline', '` + obligationTenantID + `'::uuid, 'live_stream', 'dormant', NOW() - INTERVAL '20 days', 'renewal', 'commodore', 'cold'
			FROM generate_series(1, 20000) AS n`,
			`UPDATE commodore.media_authority_use SET last_used_at = NOW() WHERE authority_id IN ('live_stream:cold-1', 'live_stream:cold-2')`,
			`ANALYZE commodore.media_authority_use`,
			`ANALYZE commodore.media_authority_refresh_obligations`,
		} {
			if _, err := db.ExecContext(ctx, statement); err != nil {
				t.Fatal(err)
			}
		}
		counts, err := q.ListExpiredWarmMediaAuthorityCounts(ctx)
		if err != nil {
			t.Fatal(err)
		}
		dormantInUse := int64(0)
		for _, row := range counts {
			if row.Lane == "object_deadline" {
				dormantInUse = row.ExpiredCount
			}
		}
		if dormantInUse < 2 {
			t.Fatalf("expired in use = %+v, want the two recently used dormant renewals counted", counts)
		}
		rows, err := db.QueryContext(ctx, "EXPLAIN "+listExpiredWarmMediaAuthorityCountsSQL())
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var plan []string
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, line)
		}
		for _, line := range plan {
			if strings.Contains(line, "Seq Scan on media_authority_refresh_obligations") || strings.Contains(line, "Seq Scan on media_authority_use") {
				t.Fatalf("expired-in-use count scans the catalog:\n%s", strings.Join(plan, "\n"))
			}
		}
	})

	// Two lanes claiming the same target in overlapping transactions: the second
	// waits for the first and then skips the target.
	t.Run("two lanes never claim the same target", func(t *testing.T) {
		target := commodoredb.LiveStreamMediaAuthorityTarget("71000000-0000-0000-0000-000000000099")
		if err := q.EnqueueMediaAuthorityEvent(ctx, target, obligationTenantID, "invariants", "commodore", "lanes"); err != nil {
			t.Fatal(err)
		}
		if err := q.ScheduleMediaAuthorityRenewal(ctx, target, obligationTenantID, 1, time.Now().Add(-time.Minute)); err != nil {
			t.Fatal(err)
		}
		first, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer first.Rollback() //nolint:errcheck // committed below
		rows, err := commodoredb.New(first).ClaimMediaAuthorityObligations(ctx, commodoredb.ClaimMediaAuthorityObligationsParams{Lane: "object_deadline", LeaseMs: 60000, BatchSize: 100})
		if err != nil || len(rows) != 1 || rows[0].TargetKey != target.Key {
			t.Fatalf("renewal claim: %+v (err %v)", rows, err)
		}
		type claimResult struct {
			rows []commodoredb.ClaimMediaAuthorityObligationsRow
			err  error
		}
		second := make(chan claimResult, 1)
		go func() {
			claimed, claimErr := q.ClaimMediaAuthorityObligations(ctx, commodoredb.ClaimMediaAuthorityObligationsParams{Lane: "event", LeaseMs: 60000, BatchSize: 100})
			second <- claimResult{claimed, claimErr}
		}()
		var result claimResult
		received := false
		select {
		case result = <-second:
			received = true
		case <-time.After(300 * time.Millisecond):
		}
		if err = first.Commit(); err != nil {
			t.Fatal(err)
		}
		if !received {
			result = <-second
		}
		if result.err != nil {
			t.Fatal(result.err)
		}
		for _, row := range result.rows {
			if row.TargetKey == target.Key {
				t.Fatal("the event lane claimed a target the renewal lane holds")
			}
		}
		// Once the renewal settles, the event lane may take the target.
		if _, err = q.CompleteMediaAuthorityObligation(ctx, commodoredb.CompleteMediaAuthorityObligationParams{TargetKey: target.Key, Lane: "object_deadline", Revision: rows[0].Revision, ClaimToken: rows[0].ClaimToken}); err != nil {
			t.Fatal(err)
		}
		if err = q.ReleaseMediaAuthorityTargetClaim(ctx, commodoredb.ReleaseMediaAuthorityTargetClaimParams{TargetKey: target.Key, Lane: "object_deadline", ClaimToken: rows[0].ClaimToken}); err != nil {
			t.Fatal(err)
		}
		after, err := q.ClaimMediaAuthorityObligations(ctx, commodoredb.ClaimMediaAuthorityObligationsParams{Lane: "event", LeaseMs: 60000, BatchSize: 100})
		if err != nil || len(after) != 1 || after[0].TargetKey != target.Key {
			t.Fatalf("event claim after the renewal settled: %+v (err %v)", after, err)
		}
	})

	// A worker whose lease lapsed resumes after its target was claimed again, at
	// the same revision. It settles and releases nothing: the attempt, and the
	// claim on the target, belong to the worker that replaced it.
	t.Run("a lapsed worker cannot settle the attempt that replaced it", func(t *testing.T) {
		target := commodoredb.LiveStreamMediaAuthorityTarget("71000000-0000-0000-0000-000000000096")
		if err := q.EnqueueMediaAuthorityEvent(ctx, target, obligationTenantID, "invariants", "commodore", "lapsed"); err != nil {
			t.Fatal(err)
		}
		claimOne := func() commodoredb.ClaimMediaAuthorityObligationsRow {
			t.Helper()
			rows, err := q.ClaimMediaAuthorityObligations(ctx, commodoredb.ClaimMediaAuthorityObligationsParams{Lane: "event", LeaseMs: 60000, BatchSize: 100})
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range rows {
				if row.TargetKey == target.Key {
					return row
				}
			}
			t.Fatal("target not claimable")
			return commodoredb.ClaimMediaAuthorityObligationsRow{}
		}
		first := claimOne()
		if _, err := db.ExecContext(ctx, `UPDATE commodore.media_authority_refresh_obligations SET lease_expires_at = NOW() - INTERVAL '1 second' WHERE target_key = $1`, target.Key); err != nil {
			t.Fatal(err)
		}
		second := claimOne()
		if second.Revision != first.Revision || second.ClaimToken == first.ClaimToken {
			t.Fatalf("reclaim: revision %d→%d token %q→%q, want the same revision under a new token", first.Revision, second.Revision, first.ClaimToken, second.ClaimToken)
		}
		// The first worker resumes: complete, fail, park and release all miss.
		if settled, err := q.CompleteMediaAuthorityObligation(ctx, commodoredb.CompleteMediaAuthorityObligationParams{TargetKey: target.Key, Lane: "event", Revision: first.Revision, ClaimToken: first.ClaimToken}); err != nil || settled != 0 {
			t.Fatalf("lapsed complete settled %d (err %v)", settled, err)
		}
		if settled, err := q.ParkMediaAuthorityObligation(ctx, commodoredb.ParkMediaAuthorityObligationParams{
			ParkReason: sql.NullString{String: "x", Valid: true}, LastError: sql.NullString{String: "x", Valid: true},
			TargetKey: target.Key, Lane: "event", Revision: first.Revision, ClaimToken: first.ClaimToken,
		}); err != nil || settled != 0 {
			t.Fatalf("lapsed park settled %d (err %v)", settled, err)
		}
		if err := q.ReleaseMediaAuthorityTargetClaim(ctx, commodoredb.ReleaseMediaAuthorityTargetClaimParams{TargetKey: target.Key, Lane: "event", ClaimToken: first.ClaimToken}); err != nil {
			t.Fatal(err)
		}
		var claims int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM commodore.media_authority_target_claims WHERE target_key = $1`, target.Key).Scan(&claims); err != nil || claims != 1 {
			t.Fatalf("claim rows after the lapsed release: %d (err %v), want the second worker's", claims, err)
		}
		if got, _ := readObligation(t, ctx, db, "event", target.Key); got.status != "processing" || !got.leased {
			t.Fatalf("second attempt after the lapsed worker resumed: %+v, want processing with its lease", got)
		}
		// The second attempt fails and stays retryable.
		if settled, err := q.FailMediaAuthorityObligation(ctx, commodoredb.FailMediaAuthorityObligationParams{
			NextAttemptAt: time.Now(), LastError: sql.NullString{String: "transient", Valid: true},
			TargetKey: target.Key, Lane: "event", Revision: second.Revision, ClaimToken: second.ClaimToken,
		}); err != nil || settled != 1 {
			t.Fatalf("second attempt fail settled %d (err %v)", settled, err)
		}
		if got, _ := readObligation(t, ctx, db, "event", target.Key); got.status != "pending" {
			t.Fatalf("after the failed second attempt: %q, want pending", got.status)
		}
		if err := q.ReleaseMediaAuthorityTargetClaim(ctx, commodoredb.ReleaseMediaAuthorityTargetClaimParams{TargetKey: target.Key, Lane: "event", ClaimToken: second.ClaimToken}); err != nil {
			t.Fatal(err)
		}
	})

	// An event compile overtaken by another compile of the same authority, which
	// then fails, must not lose its change: the event stays pending.
	t.Run("an overtaken event compile keeps its change", func(t *testing.T) {
		target := commodoredb.LiveStreamMediaAuthorityTarget("71000000-0000-0000-0000-000000000095")
		if err := q.EnqueueMediaAuthorityEvent(ctx, target, obligationTenantID, "invariants", "commodore", "overtaken"); err != nil {
			t.Fatal(err)
		}
		rows, err := q.ClaimMediaAuthorityObligations(ctx, commodoredb.ClaimMediaAuthorityObligationsParams{Lane: "event", LeaseMs: 60000, BatchSize: 100})
		if err != nil {
			t.Fatal(err)
		}
		var claimed commodoredb.ClaimMediaAuthorityObligationsRow
		for _, row := range rows {
			if row.TargetKey == target.Key {
				claimed = row
			}
		}
		if claimed.TargetKey == "" {
			t.Fatal("event not claimed")
		}
		s.settleMediaAuthorityObligation(claimed, &mediaAuthorityCompileOutcome{}, errMediaAuthorityCompileSuperseded)
		if got, _ := readObligation(t, ctx, db, "event", target.Key); got.status != "pending" {
			t.Fatalf("overtaken event settled %q, want pending so it runs again", got.status)
		}
	})

	// A use that arrives while a renewal is deciding the authority is unused
	// must win, and so must a use that was already recorded that day.
	t.Run("use racing a dormant settlement keeps the renewal", func(t *testing.T) {
		target := commodoredb.LiveStreamMediaAuthorityTarget("71000000-0000-0000-0000-000000000098")
		authorityID := sharedauthority.LiveStreamAuthorityID(target.ObjectID())
		if err := q.ScheduleMediaAuthorityRenewal(ctx, target, obligationTenantID, 1, time.Now().Add(-time.Minute)); err != nil {
			t.Fatal(err)
		}
		claim := func() commodoredb.ClaimMediaAuthorityObligationsRow {
			t.Helper()
			rows, err := q.ClaimMediaAuthorityObligations(ctx, commodoredb.ClaimMediaAuthorityObligationsParams{Lane: "object_deadline", LeaseMs: 60000, BatchSize: 100})
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range rows {
				if row.TargetKey == target.Key {
					return row
				}
			}
			t.Fatal("renewal not claimable")
			return commodoredb.ClaimMediaAuthorityObligationsRow{}
		}
		row := claim()
		if err := s.recordMediaAuthorityUse(ctx, "media_object", authorityID, obligationTenantID); err != nil {
			t.Fatal(err)
		}
		s.settleMediaAuthorityObligation(row, &mediaAuthorityCompileOutcome{dormant: true}, nil)
		if got, _ := readObligation(t, ctx, db, "object_deadline", target.Key); got.status == "dormant" {
			t.Fatal("a use recorded while the renewal was compiling was lost to its dormant settlement")
		}

		// Settled dormant with no use in flight; then a second report the same
		// day, which does not advance the use row, still revives it.
		row = claim()
		s.settleMediaAuthorityObligation(row, &mediaAuthorityCompileOutcome{dormant: true}, nil)
		if got, _ := readObligation(t, ctx, db, "object_deadline", target.Key); got.status != "dormant" {
			t.Fatalf("renewal did not settle dormant: %q", got.status)
		}
		if err := s.recordMediaAuthorityUse(ctx, "media_object", authorityID, obligationTenantID); err != nil {
			t.Fatal(err)
		}
		if got, _ := readObligation(t, ctx, db, "object_deadline", target.Key); got.status != "pending" {
			t.Fatalf("a repeated use left the renewal %q", got.status)
		}

		// A compile of the object that records its own use (it is new, or
		// ingesting) does not fold its own renewal, or it would run forever.
		row = claim()
		laneCtx := context.WithValue(ctx, mediaAuthorityObligationLaneContextKey{}, commodoredb.MediaAuthorityLaneObjectDeadline)
		if err := s.recordMediaAuthorityUse(laneCtx, "media_object", authorityID, obligationTenantID); err != nil {
			t.Fatal(err)
		}
		if got, _ := readObligation(t, ctx, db, "object_deadline", target.Key); got.revision != row.Revision {
			t.Fatalf("a renewal folded itself: revision %d, claimed %d", got.revision, row.Revision)
		}
	})

	// A tenant can publish for another reason while an object is parking to wait
	// for it. Its wake-up misses the object, so the compile the object requests
	// must wake it even though that compile publishes nothing.
	t.Run("an object parking to wait for its tenant is always woken", func(t *testing.T) {
		const tenantID = "70000000-0000-0000-0000-0000000000a1"
		tenantPayload := obligationTenantPayload()
		tenantPayload.TenantId = tenantID
		target := commodoredb.LiveStreamMediaAuthorityTarget("71000000-0000-0000-0000-000000000097")
		if err := q.EnqueueMediaAuthorityEvent(ctx, target, tenantID, "invariants", "commodore", "wait"); err != nil {
			t.Fatal(err)
		}
		rows, err := q.ClaimMediaAuthorityObligations(ctx, commodoredb.ClaimMediaAuthorityObligationsParams{Lane: "event", LeaseMs: 60000, BatchSize: 100})
		if err != nil {
			t.Fatal(err)
		}
		var claimed commodoredb.ClaimMediaAuthorityObligationsRow
		for _, row := range rows {
			if row.TargetKey == target.Key {
				claimed = row
			}
		}
		if claimed.TargetKey == "" {
			t.Fatal("object not claimed")
		}
		lock, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Rollback() //nolint:errcheck // committed below
		if _, err = lock.ExecContext(ctx, `SELECT 1 FROM commodore.media_authority_refresh_obligations WHERE target_key = $1 AND lane = 'event' FOR UPDATE`, target.Key); err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			s.settleMediaAuthorityObligation(claimed, &mediaAuthorityCompileOutcome{}, errTenantAuthorityMissing)
		}()
		time.Sleep(200 * time.Millisecond)
		now := time.Now().UTC()
		if err = s.persistTenantAuthority(ctx, tenantPayload, []string{"cell-a"}, revisions, now, now.Add(24*time.Hour)); err != nil {
			t.Fatal(err)
		}
		if err = lock.Commit(); err != nil {
			t.Fatal(err)
		}
		<-done
		if got, _ := readObligation(t, ctx, db, "event", target.Key); got.status != "parked" {
			t.Fatalf("object after the race = %q, want parked (the tenant's publication could not see it)", got.status)
		}
		requested, found := readObligation(t, ctx, db, "event", commodoredb.TenantMediaAuthorityTarget(tenantID).Key)
		if !found || requested.status != "pending" {
			t.Fatalf("parking did not request the tenant compile: %+v (found %v)", requested, found)
		}
		// That compile finds the tenant already published and publishes nothing,
		// and still wakes the object.
		if err = s.persistTenantAuthority(ctx, tenantPayload, []string{"cell-a"}, revisions, now.Add(time.Minute), now.Add(time.Minute+24*time.Hour)); err != nil {
			t.Fatal(err)
		}
		if got, _ := readObligation(t, ctx, db, "event", target.Key); got.status != "pending" {
			t.Fatalf("object after the tenant compile = %q, want pending", got.status)
		}
	})

	// A cold object whose tenant authority ran out while a cell still holds the
	// object's thirty-day public copy. Its policy becomes restricted. The change
	// is compiled through the obligation worker's own path and published at
	// once, on the lapsed tenant: waiting would let a later renewal of the tenant
	// (for another object, changing nothing this one depends on) pair the old
	// public copy with a valid tenant again.
	t.Run("a policy change is not dropped while the tenant has lapsed", func(t *testing.T) {
		const tenantID = "70000000-0000-0000-0000-0000000000c1"
		const streamID = "74000000-0000-0000-0000-000000000001"
		authorityID := sharedauthority.LiveStreamAuthorityID(streamID)
		tenant := obligationTenantPayload()
		tenant.TenantId = tenantID
		compiler := &CommodoreServer{db: db, logger: logging.NewLogger(), mediaAuthorityKeyID: "invariants", mediaAuthorityPrivateKey: s.mediaAuthorityPrivateKey,
			authorityTenantSource: &tenantPlacementOwnerFixture{}, authorityBillingSource: &tenantPlacementOwnerFixture{}}
		now := time.Now().UTC()
		if err := compiler.persistTenantAuthority(ctx, tenant, []string{"cell-a"}, revisions, now, now.Add(24*time.Hour)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `
			INSERT INTO commodore.streams (id, tenant_id, user_id, stream_key, playback_id, internal_name, title, ingest_mode, requires_auth, created_at)
			VALUES ($1, $2, $2, 'lapsed-key', 'lapsed-playback', 'lapsed-internal', 'lapsed', 'push', FALSE, NOW() - INTERVAL '60 days')`, streamID, tenantID); err != nil {
			t.Fatal(err)
		}
		if err := compiler.compileLiveStreamAuthority(withMediaAuthorityInUse(ctx, true), streamID); err != nil {
			t.Fatalf("first compile: %v", err)
		}
		first, err := q.GetCurrentMediaAuthorityPublication(ctx, commodoredb.GetCurrentMediaAuthorityPublicationParams{AuthorityKind: "media_object", AuthorityID: authorityID})
		if err != nil || time.Until(first.ValidUntil) < 7*24*time.Hour {
			t.Fatalf("first version valid until %s (err %v), want a long public copy", first.ValidUntil, err)
		}

		// Nobody uses the object or its tenant, the tenant has run out, and the
		// playback policy changes.
		for _, statement := range []string{
			`UPDATE commodore.media_authority_use_epoch SET started_at = NOW() - INTERVAL '60 days'`,
			`UPDATE commodore.media_authority_versions SET issued_at = issued_at - INTERVAL '3 days', refresh_after = refresh_after - INTERVAL '3 days', valid_until = valid_until - INTERVAL '3 days'
			 WHERE authority_kind = 'tenant' AND authority_id = '` + tenantID + `'`,
			`UPDATE commodore.streams SET requires_auth = TRUE, playback_policy = '{"type":"webhook"}'::jsonb WHERE id = '` + streamID + `'`,
		} {
			if _, err = db.ExecContext(ctx, statement); err != nil {
				t.Fatal(err)
			}
		}
		defer func() {
			if _, resetErr := db.ExecContext(ctx, `UPDATE commodore.media_authority_use_epoch SET started_at = NOW()`); resetErr != nil {
				t.Error(resetErr)
			}
		}()
		target := commodoredb.LiveStreamMediaAuthorityTarget(streamID)
		if err = q.EnqueueMediaAuthorityEvent(ctx, target, tenantID, "stream_policy_changed", "commodore", "lapsed"); err != nil {
			t.Fatal(err)
		}
		rows, err := q.ClaimMediaAuthorityObligations(ctx, commodoredb.ClaimMediaAuthorityObligationsParams{Lane: "event", LeaseMs: 60000, BatchSize: 100})
		if err != nil {
			t.Fatal(err)
		}
		var claimed commodoredb.ClaimMediaAuthorityObligationsRow
		for _, row := range rows {
			if row.TargetKey == target.Key {
				claimed = row
			}
		}
		if claimed.TargetKey == "" {
			t.Fatal("policy change not claimed")
		}
		outcome := &mediaAuthorityCompileOutcome{}
		rowCtx := context.WithValue(ctx, mediaAuthorityObligationLaneContextKey{}, claimed.Lane)
		rowCtx = context.WithValue(rowCtx, mediaAuthorityCompileOutcomeContextKey{}, outcome)
		compileErr := compiler.compileMediaAuthorityObligation(rowCtx, claimed)
		compiler.settleMediaAuthorityObligation(claimed, outcome, compileErr)
		if compileErr != nil || outcome.dormant {
			t.Fatalf("policy change on a lapsed tenant: err=%v dormant=%v, want published", compileErr, outcome.dormant)
		}
		correction, err := q.GetCurrentMediaAuthorityPublication(ctx, commodoredb.GetCurrentMediaAuthorityPublicationParams{AuthorityKind: "media_object", AuthorityID: authorityID})
		if err != nil || correction.AuthorityVersion != first.AuthorityVersion+1 {
			t.Fatalf("after the policy change: version %d (err %v), want %d", correction.AuthorityVersion, err, first.AuthorityVersion+1)
		}
		var restricted bool
		if err = db.QueryRowContext(ctx, `
			SELECT EXISTS (SELECT 1 FROM commodore.media_authority_deliveries
			               WHERE authority_kind = 'media_object' AND authority_id = $1 AND authority_version = $2 AND cell_id = 'cell-a')`,
			authorityID, correction.AuthorityVersion).Scan(&restricted); err != nil || !restricted {
			t.Fatalf("correction queued for the cell holding the public copy: %v (err %v)", restricted, err)
		}

		// The tenant is renewed, unchanged, for some other reason. The correction
		// was queued for the cell first, so the cell never pairs the public copy
		// with the renewed tenant.
		renewed := time.Now().UTC()
		if err = compiler.persistTenantAuthority(ctx, tenant, []string{"cell-a"}, revisions, renewed, renewed.Add(24*time.Hour)); err != nil {
			t.Fatal(err)
		}
		var objectFirst bool
		if err = db.QueryRowContext(ctx, `
			SELECT (SELECT created_at FROM commodore.media_authority_deliveries WHERE authority_kind = 'media_object' AND authority_id = $1 AND authority_version = $2 AND cell_id = 'cell-a')
			     < (SELECT max(created_at) FROM commodore.media_authority_deliveries WHERE authority_kind = 'tenant' AND authority_id = $3 AND cell_id = 'cell-a')`,
			authorityID, correction.AuthorityVersion, tenantID).Scan(&objectFirst); err != nil || !objectFirst {
			t.Fatalf("correction queued before the renewed tenant: %v (err %v)", objectFirst, err)
		}
	})

	t.Run("a lost ACK for an expired successor does not replay the cell", func(t *testing.T) {
		const streamID = "74000000-0000-0000-0000-000000000099"
		const cell = "lost-ack-cell"
		id := sharedauthority.LiveStreamAuthorityID(streamID)
		object := useStreamPayload("lost-ack")
		object.GetLiveStream().StreamId = streamID
		now := time.Now().UTC()
		if err := s.persistMediaObjectAuthority(withMediaAuthorityLongValidity(ctx, now.Add(sharedauthority.MaxMediaObjectValidity)), id, object, []string{cell}, revisions, now, now.Add(24*time.Hour)); err != nil {
			t.Fatal(err)
		}
		if err := q.UpsertMediaAuthorityDistribution(ctx, commodoredb.UpsertMediaAuthorityDistributionParams{AuthorityKind: "media_object", AuthorityID: id, CellID: cell, AuthorityVersion: 1}); err != nil {
			t.Fatal(err)
		}
		object.PlaybackPolicy.Kind = mediapb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_DENY
		if err := s.persistMediaObjectAuthority(ctx, id, object, []string{cell}, revisions, now, now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `UPDATE commodore.media_authority_versions
			SET issued_at = issued_at - INTERVAL '2 hours', refresh_after = refresh_after - INTERVAL '2 hours', valid_until = valid_until - INTERVAL '2 hours'
			WHERE authority_kind = 'media_object' AND authority_id = $1 AND authority_version = 2`, id); err != nil {
			t.Fatal(err)
		}
		serviceCtx := context.WithValue(ctx, ctxkeys.KeyAuthType, "service")
		var empty sharedauthority.HeldSummary
		for range 3 {
			summary, err := s.RequestMediaAuthorityReplay(serviceCtx, &commodorepb.RequestMediaAuthorityReplayRequest{
				ControlCellId: cell, HeldCount: 0, HeldDigest: empty.Digest(), AsOf: timestamppb.Now(),
			})
			if err != nil || !summary.GetInventoryRequired() {
				t.Fatalf("unacknowledged successor summary: %+v, %v", summary, err)
			}
			page, err := s.RequestMediaAuthorityReplay(serviceCtx, &commodorepb.RequestMediaAuthorityReplayRequest{
				ControlCellId: cell, InventoryPage: true, FinalPage: true, AsOf: timestamppb.Now(), AcknowledgedBefore: summary.GetObservedAt(),
			})
			if err != nil || !page.GetSummaryChecked() || !page.GetHeldMatches() || page.GetRequeuedCount() != 0 {
				t.Fatalf("expired successor caused recovery: %+v, %v", page, err)
			}
		}
		var resets int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM commodore.media_authority_cell_ack_resets WHERE cell_id = $1`, cell).Scan(&resets); err != nil || resets != 0 {
			t.Fatalf("lost ACK reset count=%d, err=%v", resets, err)
		}
	})

	// A cell acknowledged a restrictive, shorter-lived replacement, which then
	// expired; its database is restored to before that, bringing the public
	// original back. Its summary does not match: the control plane stops
	// trusting its acknowledgements, so the original counts again, and the
	// expired replacement, which replay cannot resend, is issued again.
	t.Run("a cell that does not hold what it acknowledged is corrected", func(t *testing.T) {
		const streamID = "74000000-0000-0000-0000-000000000002"
		id := sharedauthority.LiveStreamAuthorityID(streamID)
		object := useStreamPayload("restored")
		object.GetLiveStream().StreamId = streamID
		now := time.Now().UTC()
		if err := s.persistMediaObjectAuthority(withMediaAuthorityLongValidity(ctx, now.Add(sharedauthority.MaxMediaObjectValidity)), id, object, []string{"restored-cell"}, revisions, now, now.Add(24*time.Hour)); err != nil {
			t.Fatal(err)
		}
		object.PlaybackPolicy.Kind = mediapb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_DENY
		if err := s.persistMediaObjectAuthority(ctx, id, object, []string{"restored-cell"}, revisions, now, now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		for _, statement := range []string{
			`UPDATE commodore.media_authority_versions SET issued_at = issued_at - INTERVAL '2 hours', refresh_after = refresh_after - INTERVAL '2 hours', valid_until = valid_until - INTERVAL '2 hours'
			 WHERE authority_kind = 'media_object' AND authority_id = '` + id + `' AND authority_version = 2`,
			`UPDATE commodore.media_authority_deliveries SET status = 'acknowledged' WHERE authority_kind = 'media_object' AND authority_id = '` + id + `'`,
		} {
			if _, err := db.ExecContext(ctx, statement); err != nil {
				t.Fatal(err)
			}
		}
		if err := q.UpsertMediaAuthorityDistribution(ctx, commodoredb.UpsertMediaAuthorityDistributionParams{AuthorityKind: "media_object", AuthorityID: id, CellID: "restored-cell", AuthorityVersion: 2}); err != nil {
			t.Fatal(err)
		}
		if gone, err := mediaAuthorityHasNoCopyLeft(ctx, q, "media_object", id, time.Now().UTC()); err != nil || !gone {
			t.Fatalf("before the restore: no copy left = %v (err %v), want the acknowledgement to bound the cell", gone, err)
		}

		// The restored cell reports holding the public original.
		serviceCtx := context.WithValue(ctx, ctxkeys.KeyAuthType, "service")
		var held sharedauthority.HeldSummary
		held.Add("media_object", id, 1)
		asOf := time.Now().UTC()
		resp, err := s.RequestMediaAuthorityReplay(serviceCtx, &commodorepb.RequestMediaAuthorityReplayRequest{
			ControlCellId: "restored-cell", HeldCount: held.Count, HeldDigest: held.Digest(), AsOf: timestamppb.New(asOf),
		})
		if err != nil || !resp.GetInventoryRequired() || resp.GetRequeuedCount() != 0 {
			t.Fatalf("digest mismatch must request inventory without mutation: %+v, %v", resp, err)
		}
		resp, err = s.RequestMediaAuthorityReplay(serviceCtx, &commodorepb.RequestMediaAuthorityReplayRequest{
			ControlCellId: "restored-cell", InventoryPage: true, FinalPage: true, AsOf: timestamppb.Now(), AcknowledgedBefore: resp.GetObservedAt(),
			Held: []*commodorepb.HeldMediaAuthority{{AuthorityKind: "media_object", AuthorityId: id, AuthorityVersion: 1}},
		})
		if err != nil || !resp.GetSummaryChecked() || resp.GetHeldMatches() {
			t.Fatalf("summary of a restored cell: %+v (err %v), want checked and not matching", resp, err)
		}
		if gone, err := mediaAuthorityHasNoCopyLeft(ctx, q, "media_object", id, time.Now().UTC()); err != nil || gone {
			t.Fatalf("after the mismatch: no copy left = %v (err %v), want the restored original counted", gone, err)
		}
		correction, found := readObligation(t, ctx, db, "bulk", mediaObjectAuthorityTarget(id).Key)
		if !found || correction.status != "pending" {
			t.Fatalf("correction of the expired replacement: %+v (found %v), want pending in bulk", correction, found)
		}
		for range 3 {
			repeated, repeatErr := s.RequestMediaAuthorityReplay(serviceCtx, &commodorepb.RequestMediaAuthorityReplayRequest{
				ControlCellId: "restored-cell", InventoryPage: true, FinalPage: true, AsOf: timestamppb.Now(), AcknowledgedBefore: resp.GetObservedAt(),
				Held: []*commodorepb.HeldMediaAuthority{{AuthorityKind: "media_object", AuthorityId: id, AuthorityVersion: 1}},
			})
			if repeatErr != nil || repeated.GetRequeuedCount() != 0 || repeated.GetSummaryChecked() || repeated.GetHeldMatches() {
				t.Fatalf("pending recovery requeued or trusted the uncorrected cell: %+v, %v", repeated, repeatErr)
			}
		}
		stillPending, _ := readObligation(t, ctx, db, "bulk", mediaObjectAuthorityTarget(id).Key)
		if stillPending.revision != correction.revision {
			t.Fatalf("recovery advanced obligation revision %d -> %d", correction.revision, stillPending.revision)
		}
		issued := time.Now().UTC()
		if err = s.persistMediaObjectAuthority(withMediaAuthorityLongValidity(withMediaAuthorityInUse(ctx, false), issued.Add(sharedauthority.MaxMediaObjectValidity)), id, object, []string{"restored-cell"}, revisions, issued, issued.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		current, err := q.GetCurrentMediaAuthorityPublication(ctx, commodoredb.GetCurrentMediaAuthorityPublicationParams{AuthorityKind: "media_object", AuthorityID: id})
		if err != nil || current.AuthorityVersion != 3 || !current.ValidUntil.After(time.Now()) {
			t.Fatalf("the restrictive version issued again: version %d valid until %s (err %v)", current.AuthorityVersion, current.ValidUntil, err)
		}

		// A cell holding exactly what it acknowledged is told so.
		var empty sharedauthority.HeldSummary
		resp, err = s.RequestMediaAuthorityReplay(serviceCtx, &commodorepb.RequestMediaAuthorityReplayRequest{
			ControlCellId: "cell-z", HeldCount: empty.Count, HeldDigest: empty.Digest(), AsOf: timestamppb.New(time.Now().UTC()),
		})
		if err != nil || !resp.GetSummaryChecked() || !resp.GetHeldMatches() {
			t.Fatalf("summary of a cell holding what it acknowledged: %+v (err %v), want a match", resp, err)
		}
	})

	// A cell that missed a tombstone (it was offline past the tombstone's
	// validity, or restored) still holds the object's earlier version. The
	// tombstone is kept renewed while that can be so.
	t.Run("a tombstone is renewed while an older copy may be held", func(t *testing.T) {
		const streamID = "74000000-0000-0000-0000-000000000003"
		id := sharedauthority.LiveStreamAuthorityID(streamID)
		object := useStreamPayload("deleted")
		object.GetLiveStream().StreamId = streamID
		now := time.Now().UTC()
		if err := s.persistMediaObjectAuthority(withMediaAuthorityLongValidity(ctx, now.Add(sharedauthority.MaxMediaObjectValidity)), id, object, []string{"cell-a"}, revisions, now, now.Add(24*time.Hour)); err != nil {
			t.Fatal(err)
		}
		object.Lifecycle = mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_TOMBSTONE
		object.PlaybackPolicy.Kind = mediapb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_DENY
		if err := s.persistMediaObjectAuthority(ctx, id, object, []string{"cell-a"}, revisions, now, now.Add(24*time.Hour)); err != nil {
			t.Fatal(err)
		}
		tombstone, err := q.GetCurrentMediaAuthorityPublication(ctx, commodoredb.GetCurrentMediaAuthorityPublicationParams{AuthorityKind: "media_object", AuthorityID: id})
		if err != nil {
			t.Fatal(err)
		}
		if bound := renewalBoundVersion(t, ctx, db, id); bound != tombstone.AuthorityVersion {
			t.Fatalf("tombstone renewal bound to version %d, want %d while the thirty-day original may be held", bound, tombstone.AuthorityVersion)
		}
	})
}

// renewalBoundVersion is the version a media object's renewal is bound to, or 0
// when it has no live renewal.
func renewalBoundVersion(t *testing.T, ctx context.Context, db *sql.DB, authorityID string) int64 {
	t.Helper()
	var bound sql.NullInt64
	err := db.QueryRowContext(ctx, `
		SELECT bound_version FROM commodore.media_authority_refresh_obligations
		WHERE target_key = $1 AND lane = 'object_deadline' AND status IN ('pending', 'processing')`,
		mediaObjectAuthorityTarget(authorityID).Key).Scan(&bound)
	if errors.Is(err, sql.ErrNoRows) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return bound.Int64
}

var errTenantCompileReached = errors.New("tenant compile reached")

// tenantCompileProbe stops a tenant compile at its first source call, which is
// how a test tells that the compile was attempted at all.
type tenantCompileProbe struct{ *tenantPlacementOwnerFixture }

func (tenantCompileProbe) GetTenant(context.Context, string) (*quartermasterpb.GetTenantResponse, error) {
	return nil, errTenantCompileReached
}

// listExpiredWarmMediaAuthorityCountsSQL is the statement as sqlc compiles it,
// read from its source so a plan test explains exactly what runs.
func listExpiredWarmMediaAuthorityCountsSQL() string {
	source, err := os.ReadFile("../database/queries/media_authority.sql")
	if err != nil {
		panic(err)
	}
	const marker = "-- name: ListExpiredWarmMediaAuthorityCounts :many"
	text := string(source)
	start := strings.Index(text, marker)
	if start < 0 {
		panic("ListExpiredWarmMediaAuthorityCounts not found")
	}
	text = text[start+len(marker):]
	if end := strings.Index(text, "-- name:"); end >= 0 {
		text = text[:end]
	}
	return strings.TrimSuffix(strings.TrimSpace(text), ";")
}
