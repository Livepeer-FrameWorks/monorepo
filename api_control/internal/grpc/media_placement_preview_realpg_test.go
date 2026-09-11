//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_control/internal/placementpolicy"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type previewOwnerFixture struct {
	placementManagementOwnerFixture
	entitlement *quartermasterpb.GetTenantEntitlementResponse
	reads       atomic.Int32
}

func (owner *previewOwnerFixture) GetTenantEntitlement(context.Context, string) (*quartermasterpb.GetTenantEntitlementResponse, error) {
	owner.reads.Add(1)
	return proto.CloneOf(owner.entitlement), nil
}

func TestMediaPlacementPreview_RealPG(t *testing.T) {
	testMediaPlacementPreviewDatabase(t, startCommodoreRealPG(t))
}

func TestMediaPlacementPreview_RealYugabyte(t *testing.T) {
	db, ok := dockerpg.OpenSharedYugabyteDatabase(t, "commodore_placement_preview")
	if !ok {
		t.Skip("requires the shared Yugabyte contract fixture")
	}
	baseline, err := dbsql.Content.ReadFile("schema/commodore.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(baseline)); err != nil {
		t.Fatal(err)
	}
	testMediaPlacementPreviewDatabase(t, db)
}

func testMediaPlacementPreviewDatabase(t *testing.T, db *sql.DB) {
	t.Helper()
	preview, entitlement, inventory := previewCapacityFixture(t)
	const actor = "20000000-0000-4000-8000-000000000091"
	const stream = "30000000-0000-4000-8000-000000000091"
	tenant := preview.snapshot.Scope.TenantID
	ctx := context.WithValue(ctxAs(actor, tenant, "viewer"), ctxkeys.KeyAuthType, "jwt")
	owner := &previewOwnerFixture{entitlement: entitlement}
	server := &CommodoreServer{db: db, authorityTenantSource: owner, authorityBillingSource: owner}
	server.placementPushSource = func(_ context.Context, cluster string, query *placementpb.PushSourcePreviewQuery) (*placementpb.PushSourcePreviewObservation, error) {
		if cluster != "own-eu" || query.InternalName != "preview-stream" || query.TenantId != tenant {
			t.Error("publisher read escaped owned stream")
		}
		return previewSourceResponse(query), nil
	}
	var observations atomic.Int32
	server.placementCapacitySource = func(_ context.Context, _ string, query *placementpb.CapacityPreviewQuery) (*placementpb.CapacityPreviewObservation, error) {
		observations.Add(1)
		return previewCapacityResponse(t, query, inventory), nil
	}
	req := &placementpb.PreviewRequest{Scope: &placementpb.Scope{Kind: placementpb.ScopeKind_SCOPE_KIND_TENANT}, Verb: placementpb.Verb_VERB_SERVE, Coordinates: &placementpb.Coordinates{Latitude: 39, Longitude: -77}}
	result, err := server.PreviewMediaPlacement(ctx, req)
	if err != nil || !result.GetComplete() || result.GetSelected().GetClusterId() != "official-us" || result.GetSourceEvaluated() || result.GetSelected().GetNodeId() != "" || result.GetSelected().GetRequiresSourcePull() {
		t.Fatalf("real capacity preview: %+v %v", result, err)
	}
	if result.GetRevision() != 0 || result.GetParentRevision() != 0 || observations.Load() != 2 {
		t.Fatal("preview lost base revision or omitted a cell")
	}
	for _, scenario := range []string{"priced", "quote unavailable", "never rated"} {
		draft := proto.CloneOf(req)
		rules := &placement.Rules{SchemaVersion: 1, Preferences: &placement.Preferences{Groups: []placement.Group{{ID: "cheap", Order: placement.PriceFirst, PriceCurrency: "EUR", PriceUnit: "serve:minutes=1;gib=1"}}}}
		if scenario == "never rated" {
			rules.Constraints.Deny = []placement.Selector{{Classes: []placement.Class{placement.Official}, Charging: []placement.Charging{placement.Rated}}}
		}
		wireRules, rulesErr := placement.RulesToProto(rules)
		if rulesErr != nil {
			t.Fatal(rulesErr)
		}
		draft.ExpectedRevision, draft.ExpectedParentRevision = proto.Uint64(0), proto.Uint64(0)
		draft.DraftUpdate = &placementpb.VerbUpdate{Verb: draft.Verb, Kind: placementpb.UpdateKind_UPDATE_KIND_SET, Rules: wireRules}
		server.authorityCommercialSource = commercialSourceFunc(func(_ context.Context, request *placementpb.CommercialQuoteRequest) (*placementpb.CommercialQuoteResponse, error) {
			return previewQuoteResponse(request, inventory)
		})
		if scenario == "quote unavailable" {
			server.authorityCommercialSource = nil
		}
		priced, priceErr := server.PreviewMediaPlacement(ctx, draft)
		if priceErr != nil {
			t.Fatal(priceErr)
		}
		if scenario == "quote unavailable" {
			if priced.GetSelected() != nil || priced.GetComplete() {
				t.Fatalf("unknown pricing became cheapest/free: %+v", priced)
			}
		} else if priced.GetSelected().GetClusterId() != "own-eu" || priced.GetSelected().GetPrice().GetAmountMicros() != 5 || !priced.GetComplete() {
			t.Fatalf("fresh comparable cost did not beat distance: %+v", priced)
		}
	}
	server.authorityCommercialSource = nil
	req.StreamId = stream
	for _, scenario := range []string{"missing", "foreign"} {
		if scenario == "foreign" {
			if _, err := db.ExecContext(ctx, `INSERT INTO commodore.streams(id,tenant_id,user_id,stream_key,playback_id,internal_name,title) VALUES ($1,$2,$3,'preview-key','preview-playback','preview-stream','Preview')`, stream, tenant, actor); err != nil {
				t.Fatal(err)
			}
		}
		readCtx := ctx
		if scenario == "foreign" {
			readCtx = context.WithValue(ctx, ctxkeys.KeyTenantID, "10000000-0000-4000-8000-000000000092")
		}
		before := owner.reads.Load()
		if _, err := server.PreviewMediaPlacement(readCtx, req); status.Code(err) != codes.NotFound || owner.reads.Load() != before {
			t.Fatalf("%s stream exposed owner evidence: %v", scenario, err)
		}
	}
	result, err = server.PreviewMediaPlacement(ctx, req)
	if err != nil || result.GetSelected() != nil || result.GetComplete() || result.GetSourceEvaluated() {
		t.Fatalf("missing publisher claim invented a playable route: %+v %v", result, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE commodore.streams SET active_ingest_cluster_id='own-eu',active_ingest_cluster_updated_at=NOW() WHERE id=$1 AND tenant_id=$2`, stream, tenant); err != nil {
		t.Fatal(err)
	}
	result, err = server.PreviewMediaPlacement(ctx, req)
	if err != nil || result.GetSelected().GetClusterId() != "official-us" || !result.GetSourceEvaluated() || !result.GetSelected().GetRequiresSourcePull() || result.GetActiveIngestClusterId() != "" {
		t.Fatalf("owned push stream source-aware preview: %+v %v", result, err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM commodore.media_placement_policies WHERE tenant_id=$1`, tenant).Scan(&count); err != nil || count != 0 {
		t.Fatalf("preview initialized policy: %d %v", count, err)
	}

	// A tenant draft cannot remove a stream's independently stored hard exclusion.
	store := placementpolicy.NewStore(db)
	streamScope := placementpolicy.Scope{TenantID: tenant, Kind: "stream", ID: stream}
	streamRules := &placementpb.Rules{SchemaVersion: 1, Constraints: &placementpb.Constraints{Deny: []*placementpb.Selector{{ClusterIds: []string{"official-us"}}}}}
	_, err = store.Apply(ctx, placementpolicy.ApplyInput{Scope: streamScope, Policy: &placementpb.PolicySet{Revision: 1, Serve: streamRules}, ActorID: actor, IdempotencyKey: "preview-stream", ReviewDigest: strings.Repeat("a", 64)}, func(placementpolicy.Snapshot) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	req.ExpectedRevision, req.ExpectedParentRevision = proto.Uint64(0), proto.Uint64(0)
	req.DraftUpdate = &placementpb.VerbUpdate{Verb: req.Verb, Kind: placementpb.UpdateKind_UPDATE_KIND_CLEAR}
	result, err = server.PreviewMediaPlacement(ctx, req)
	if err != nil || result.GetSelected().GetClusterId() != "own-eu" || result.GetRevision() != 0 {
		t.Fatalf("account draft bypassed stream constraints: %+v %v", result, err)
	}
	ownerCtx := context.WithValue(ctx, ctxkeys.KeyRole, "owner")
	result, err = server.PreviewMediaPlacement(ownerCtx, req)
	if err != nil || result.GetSelected().GetNodeId() != "own-eu-node" {
		t.Fatalf("owner private inspection unavailable: %+v %v", result, err)
	}
	for _, candidate := range result.GetCandidates() {
		if candidate.GetClusterId() == "official-us" && candidate.GetNodeId() != "" {
			t.Fatal("tenant owner saw official physical node")
		}
	}

	req.DraftUpdate, req.Verb = nil, placementpb.Verb_VERB_INGEST
	if _, err := db.ExecContext(ctx, `UPDATE commodore.streams SET active_ingest_cluster_id='own-eu',active_ingest_cluster_updated_at=NOW() WHERE id=$1 AND tenant_id=$2`, stream, tenant); err != nil {
		t.Fatal(err)
	}
	result, err = server.PreviewMediaPlacement(ctx, req)
	if err != nil || result.GetActiveIngestClusterId() != "own-eu" || result.GetSelected().GetClusterId() != "own-eu" {
		t.Fatalf("active ingest migrated to nearer node: %+v %v", result, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE commodore.streams SET active_ingest_cluster_updated_at=NOW()-INTERVAL '1 day' WHERE id=$1 AND tenant_id=$2`, stream, tenant); err != nil {
		t.Fatal(err)
	}
	result, err = server.PreviewMediaPlacement(ctx, req)
	if err != nil || result.GetActiveIngestClusterId() != "" || result.GetSelected().GetClusterId() != "official-us" {
		t.Fatalf("expired publisher claim pinned ingest: %+v %v", result, err)
	}
	for _, value := range []string{"NULL", "NOW()+INTERVAL '1 hour'"} {
		if _, err := db.ExecContext(ctx, `UPDATE commodore.streams SET active_ingest_cluster_updated_at=`+value+` WHERE id=$1 AND tenant_id=$2`, stream, tenant); err != nil {
			t.Fatal(err)
		}
		before := owner.reads.Load()
		if _, err := server.PreviewMediaPlacement(ctx, req); status.Code(err) != codes.Unavailable || owner.reads.Load() != before {
			t.Fatalf("ambiguous publisher ownership became a new destination: %v", err)
		}
	}
	if _, err := db.ExecContext(ctx, `UPDATE commodore.streams SET active_ingest_cluster_id=NULL,active_ingest_cluster_updated_at=NULL WHERE id=$1 AND tenant_id=$2`, stream, tenant); err != nil {
		t.Fatal(err)
	}

	// Revisions must be checked again after observation, even when the draft itself is unchanged.
	req.Verb = placementpb.Verb_VERB_SERVE
	var changed atomic.Bool
	server.placementCapacitySource = func(readCtx context.Context, _ string, query *placementpb.CapacityPreviewQuery) (*placementpb.CapacityPreviewObservation, error) {
		if changed.CompareAndSwap(false, true) {
			_, changeErr := store.Apply(readCtx, placementpolicy.ApplyInput{Scope: streamScope, ExpectedRevision: 1, Policy: &placementpb.PolicySet{Revision: 2, Serve: streamRules}, ActorID: actor, IdempotencyKey: "preview-drift", ReviewDigest: strings.Repeat("b", 64)}, func(placementpolicy.Snapshot) error { return nil })
			if changeErr != nil {
				return nil, changeErr
			}
		}
		return previewCapacityResponse(t, query, inventory), nil
	}
	if _, err := server.PreviewMediaPlacement(ctx, req); status.Code(err) != codes.Aborted {
		t.Fatalf("concurrent stream edit returned old preview: %v", err)
	}
	if !changed.Load() {
		t.Fatal("drift test never changed state")
	}
	stale := proto.CloneOf(req)
	stale.ExpectedRevision = proto.Uint64(7)
	before := owner.reads.Load()
	if _, err := server.PreviewMediaPlacement(ctx, stale); status.Code(err) != codes.Aborted || owner.reads.Load() != before {
		t.Fatalf("stale draft reached remote reads: %v", err)
	}
	deadline, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
	defer cancel()
	if _, err := server.PreviewMediaPlacement(deadline, req); err == nil {
		t.Fatal("cancelled preview succeeded")
	}
}
