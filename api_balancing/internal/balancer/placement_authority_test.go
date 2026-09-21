package balancer

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	localauthority "frameworks/api_balancing/internal/mediaauthority"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	clusterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func placementAuthorityFixture() (localauthority.PlacementPair, time.Time) {
	now := time.Unix(1800000000, 0)
	return localauthority.PlacementPair{
		Tenant: localauthority.TenantSnapshot{
			Version: 11, Ready: true, IngestReady: true, SourceReady: true, RefreshAfter: now.Add(-time.Second), ValidUntil: now.Add(time.Minute),
			Authority: &mediaauthoritypb.TenantAuthority{
				SchemaVersion: sharedauthority.PlacementSchemaVersion, TenantId: "tenant", Lifecycle: mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
				BillingDecision: mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW, MediaPlacement: &placementpb.PolicySet{Revision: 3},
				EffectiveClusterGrants: []*mediaauthoritypb.TenantClusterGrant{
					{ClusterId: "us", ControlCellId: "cell-us", ClusterClass: "platform_official", OwnerTenantId: "platform", SubscriptionStatus: "active", RegionId: "us-east",
						MediaConsent:    &placementpb.CapacityConsent{Revision: 4, AllowIngest: true, AllowServe: true, AllowExternalSource: true},
						CommercialFacts: &placementpb.CommercialFacts{Charging: placementpb.Charging_CHARGING_RATED, Revision: "classification-9", ExpiresAt: timestamppb.New(now.Add(2 * time.Minute))}},
					{ClusterId: "own", ControlCellId: "cell-eu", ClusterClass: "third_party_marketplace", OwnerTenantId: "tenant", SubscriptionStatus: "active",
						MediaConsent: &placementpb.CapacityConsent{AllowIngest: true, AllowServe: true}},
					{ClusterId: "eu", ControlCellId: "cell-eu", ClusterClass: "platform_official", OwnerTenantId: "platform", SubscriptionStatus: "active",
						MediaConsent: &placementpb.CapacityConsent{AllowIngest: true, AllowServe: true}},
				},
			},
		},
		Object: localauthority.MediaObjectSnapshot{
			AuthorityID: sharedauthority.LiveStreamAuthorityID("stream"), Version: 12, Ready: true, IngestReady: true, SourceReady: true,
			RefreshAfter: now.Add(-time.Second), ValidUntil: now.Add(15 * time.Second),
			Authority: &mediaauthoritypb.MediaObjectAuthority{SchemaVersion: sharedauthority.PlacementSchemaVersion, TenantId: "tenant", InternalName: "internal",
				Lifecycle: mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE, ObjectKind: mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM,
				MediaPlacement: &placementpb.PolicySet{Revision: 5}, PlacementTenantRevision: 3,
				Object: &mediaauthoritypb.MediaObjectAuthority_LiveStream{LiveStream: &mediaauthoritypb.LiveStreamAuthority{StreamId: "stream", IngestMode: "push"}},
			},
		},
	}, now
}

func TestPlacementAuthorityPreservesCompleteCensusAndCommercialProvenance(t *testing.T) {
	pair, now := placementAuthorityFixture()
	addPlacementObjectQuotes(t, &pair, now)
	beforeTenant, beforeObject := proto.CloneOf(pair.Tenant.Authority), proto.CloneOf(pair.Object.Authority)
	got, err := CompilePlacementAuthority(pair, placement.Serve, now)
	if err != nil {
		t.Fatal(err)
	}
	wantCells := []PlacementCell{{ID: "cell-eu", ClusterIDs: []string{"eu", "own"}}, {ID: "cell-us", ClusterIDs: []string{"us"}}}
	if !reflect.DeepEqual(got.Cells, wantCells) || len(got.Clusters) != 3 || !got.ExpiresAt.Equal(pair.Object.ValidUntil) || got.PolicyRevision != 5 || got.ParentRevision != 3 {
		t.Fatalf("incorrect authority census: %+v", got)
	}
	us := got.Clusters["us"]
	if !us.Official || us.Region != "us-east" || us.Charging != placement.Rated || us.ChargingRevision != strings.Repeat("b", 64) || us.Price != nil || !us.AllowExternalSource || us.ConsentRevision != 4 {
		t.Fatalf("lost authoritative facts: %+v", us)
	}
	if got.Clusters["own"].Official || got.Clusters["own"].OwnerTenantID != pair.Tenant.Authority.TenantId || got.Clusters["own"].Charging != placement.PermanentlyFree || len(got.Clusters["own"].Prices) != 0 {
		t.Fatal("ownership, classification or unavailable price changed")
	}
	ingest, err := CompilePlacementAuthority(pair, placement.Ingest, now)
	if err != nil || len(ingest.Clusters["us"].Prices) != 2 {
		t.Fatalf("wrong verb price: %+v %v", ingest, err)
	}
	if len(us.Prices) != 2 || us.Prices[0].AmountMicros != 20 || us.Prices[1].AmountMicros != 30 || us.Prices[0].Revision != us.ChargingRevision || !us.Prices[0].ExpiresAt.Equal(us.ChargingUntil) || ingest.Clusters["us"].Prices[0].AmountMicros != 10 || ingest.Clusters["us"].Prices[1].AmountMicros != 40 {
		t.Fatal("lost collection or selected wrong media verb")
	}
	got.Clusters["us"].Prices[0].AmountMicros = 100
	got.CommercialQuote.Clusters[0].Facts.Revision = "mutated"
	got.Clusters["us"].AllowedVerbs[0] = "invalid"
	got.Cells[0].ClusterIDs[0] = "invalid"
	if !proto.Equal(pair.Tenant.Authority, beforeTenant) || !proto.Equal(pair.Object.Authority, beforeObject) {
		t.Fatal("compiled facts alias signed state")
	}
}

func TestPlacementAuthorityRejectsConflictingPriceCollection(t *testing.T) {
	pair, now := placementAuthorityFixture()
	facts := pair.Tenant.Authority.EffectiveClusterGrants[0].CommercialFacts
	facts.IngestPrices = []*placementpb.Price{{Currency: "USD", Unit: "ingest:gib=1", AmountMicros: 1}}
	if _, err := CompilePlacementAuthority(pair, placement.Serve, now); !errors.Is(err, ErrPlacementAuthorityInvalid) {
		t.Fatalf("conflicting unselected verb accepted: %v", err)
	}
}

func addPlacementObjectQuotes(t *testing.T, pair *localauthority.PlacementPair, now time.Time) {
	t.Helper()
	pair.Tenant.Authority.TenantId = "81000000-0000-4000-8000-000000000001"
	pair.Object.Authority.TenantId = pair.Tenant.Authority.TenantId
	pair.Tenant.Authority.EffectiveClusterGrants[1].OwnerTenantId = pair.Tenant.Authority.TenantId
	for _, grant := range pair.Tenant.Authority.EffectiveClusterGrants {
		if grant.ClusterId == "own" {
			grant.AccessSource = clusterpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_OWNER
		} else {
			grant.OwnerTenantId = "81000000-0000-4000-8000-000000000002"
			grant.AccessSource = clusterpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_PLATFORM_TIER
		}
	}
	for _, verb := range []placement.Verb{placement.Ingest, placement.Serve} {
		units, amounts := []string{"ingest:gib=1", "ingest:gib=2"}, []uint64{10, 40}
		if verb == placement.Serve {
			units, amounts = []string{"serve:minutes=1;gib=2", "serve:minutes=60;gib=2"}, []uint64{20, 30}
		}
		rules := &placementpb.Rules{SchemaVersion: 1, Preferences: &placementpb.Preferences{Groups: []*placementpb.Group{
			{Id: "first", Order: placementpb.Order_ORDER_PRICE, PriceCurrency: "USD", PriceUnit: units[0]},
			{Id: "second", Order: placementpb.Order_ORDER_PRICE, PriceCurrency: "USD", PriceUnit: units[1]},
		}}}
		if verb == placement.Ingest {
			pair.Object.Authority.MediaPlacement.Ingest = rules
		} else {
			pair.Object.Authority.MediaPlacement.Serve = rules
		}
		request, err := sharedauthority.PlacementCommercialRequest(pair.Tenant.Authority, pair.Object.Authority, verb)
		if err != nil {
			t.Fatal(err)
		}
		_, digest, err := placement.CanonicalCommercialQuoteRequest(request)
		if err != nil {
			t.Fatal(err)
		}
		quote := &placementpb.CommercialQuoteResponse{Scope: request, RequestDigest: digest, ObservedAt: timestamppb.New(now), ExpiresAt: timestamppb.New(now.Add(30 * time.Second)), Usage: &placementpb.QuoteUsageEvidence{Status: placementpb.QuoteUsageStatus_QUOTE_USAGE_STATUS_NOT_REQUIRED}}
		quote.EntitlementDigest, err = sharedauthority.PlacementCommercialEntitlementDigest(pair.Tenant.Authority)
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range request.ClusterIds {
			cluster := &placementpb.ClusterCommercialQuote{ClusterId: id, Facts: &placementpb.CommercialFacts{Revision: strings.Repeat("b", 64), Charging: placementpb.Charging_CHARGING_RATED, ExpiresAt: proto.CloneOf(quote.ExpiresAt)}}
			if id == "own" {
				cluster.Facts.Charging = placementpb.Charging_CHARGING_PERMANENTLY_FREE
			}
			for index, basis := range request.Bases {
				if id != "us" {
					cluster.Unavailable = append(cluster.Unavailable, &placementpb.UnavailableQuote{Basis: proto.CloneOf(basis), Reason: placementpb.QuoteUnavailableReason_QUOTE_UNAVAILABLE_REASON_TARIFF_UNSUPPORTED})
					continue
				}
				price := &placementpb.Price{Currency: basis.Currency, Unit: basis.Unit, AmountMicros: amounts[index]}
				if verb == placement.Ingest {
					cluster.Facts.IngestPrices = append(cluster.Facts.IngestPrices, price)
				} else {
					cluster.Facts.ServePrices = append(cluster.Facts.ServePrices, price)
				}
			}
			quote.Clusters = append(quote.Clusters, cluster)
		}
		pair.Object.Authority.CommercialQuotes = append(pair.Object.Authority.CommercialQuotes, quote)
	}
}

func TestPlacementAuthorityRejectsReboundObjectCommercialEvidence(t *testing.T) {
	for name, mutate := range map[string]func(*localauthority.PlacementPair){
		"policy": func(p *localauthority.PlacementPair) {
			p.Object.Authority.MediaPlacement.Serve.Preferences.Groups[0].PriceUnit = "serve:minutes=2;gib=2"
		},
		"owner": func(p *localauthority.PlacementPair) {
			p.Tenant.Authority.EffectiveClusterGrants[0].OwnerTenantId = "81000000-0000-4000-8000-000000000003"
		},
		"consent": func(p *localauthority.PlacementPair) {
			p.Tenant.Authority.EffectiveClusterGrants[0].MediaConsent.AllowServe = false
		},
		"partial census": func(p *localauthority.PlacementPair) {
			p.Object.Authority.CommercialQuotes[0].Clusters = p.Object.Authority.CommercialQuotes[0].Clusters[:1]
		},
		"duplicate verb": func(p *localauthority.PlacementPair) {
			p.Object.Authority.CommercialQuotes[1] = proto.CloneOf(p.Object.Authority.CommercialQuotes[0])
		},
	} {
		t.Run(name, func(t *testing.T) {
			pair, now := placementAuthorityFixture()
			addPlacementObjectQuotes(t, &pair, now)
			mutate(&pair)
			if _, err := CompilePlacementAuthority(pair, placement.Serve, now); !errors.Is(err, ErrPlacementAuthorityInvalid) {
				t.Fatalf("rebound commercial evidence accepted: %v", err)
			}
		})
	}
}

func TestPlacementAuthorityRejectsUnsafeJoinsAndReadiness(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*localauthority.PlacementPair, time.Time)
		want   error
	}{
		{"tenant_not_ready", func(p *localauthority.PlacementPair, _ time.Time) { p.Tenant.Ready = false }, ErrPlacementAuthorityNotReady},
		{"object_not_ready", func(p *localauthority.PlacementPair, _ time.Time) { p.Object.Ready = false }, ErrPlacementAuthorityNotReady},
		{"legacy_tenant", func(p *localauthority.PlacementPair, _ time.Time) { p.Tenant.Authority.SchemaVersion = 1 }, ErrPlacementAuthorityNotReady},
		{"legacy_object", func(p *localauthority.PlacementPair, _ time.Time) { p.Object.Authority.SchemaVersion = 1 }, ErrPlacementAuthorityNotReady},
		{"exact_expiry", func(p *localauthority.PlacementPair, now time.Time) { p.Tenant.ValidUntil = now }, ErrPlacementAuthorityNotReady},
		{"missing_expiry", func(p *localauthority.PlacementPair, _ time.Time) { p.Object.ValidUntil = time.Time{} }, ErrPlacementAuthorityNotReady},
		// The cell's restore fence withholds a pair whose signed validity still runs.
		{"object_withheld", func(p *localauthority.PlacementPair, _ time.Time) {
			p.Object.Freshness = localauthority.FreshnessHardExpired
		}, ErrPlacementAuthorityNotReady},
		{"tenant_withheld", func(p *localauthority.PlacementPair, _ time.Time) {
			p.Tenant.Freshness = localauthority.FreshnessHardExpired
		}, ErrPlacementAuthorityNotReady},
		{"tenant_mismatch", func(p *localauthority.PlacementPair, _ time.Time) { p.Object.Authority.TenantId = "another" }, ErrPlacementAuthorityInvalid},
		{"parent_skew", func(p *localauthority.PlacementPair, _ time.Time) { p.Object.Authority.PlacementTenantRevision++ }, ErrPlacementAuthorityInvalid},
		{"missing_intent", func(p *localauthority.PlacementPair, _ time.Time) { p.Tenant.Authority.MediaPlacement = nil }, ErrPlacementAuthorityInvalid},
		{"inactive", func(p *localauthority.PlacementPair, _ time.Time) {
			p.Object.Authority.Lifecycle = mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_INACTIVE
		}, ErrPlacementAuthorityDenied},
		{"billing_denied", func(p *localauthority.PlacementPair, _ time.Time) {
			p.Tenant.Authority.BillingDecision = mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_PAYMENT_REQUIRED
		}, ErrPlacementAuthorityDenied},
		{"missing_consent", func(p *localauthority.PlacementPair, _ time.Time) {
			p.Tenant.Authority.EffectiveClusterGrants[0].MediaConsent = nil
		}, ErrPlacementAuthorityInvalid},
		{"missing_cell", func(p *localauthority.PlacementPair, _ time.Time) {
			p.Tenant.Authority.EffectiveClusterGrants[0].ControlCellId = ""
		}, ErrPlacementAuthorityInvalid},
		{"duplicate_cluster", func(p *localauthority.PlacementPair, _ time.Time) {
			p.Tenant.Authority.EffectiveClusterGrants = append(p.Tenant.Authority.EffectiveClusterGrants, p.Tenant.Authority.EffectiveClusterGrants[0])
		}, ErrPlacementAuthorityInvalid},
		{"expired_grant", func(p *localauthority.PlacementPair, now time.Time) {
			p.Tenant.Authority.EffectiveClusterGrants[0].ExpiresAt = timestamppb.New(now)
		}, ErrPlacementAuthorityInvalid},
		{"expired_commercial", func(p *localauthority.PlacementPair, now time.Time) {
			p.Tenant.Authority.EffectiveClusterGrants[0].CommercialFacts.ExpiresAt = timestamppb.New(now)
		}, ErrPlacementAuthorityInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pair, now := placementAuthorityFixture()
			tc.mutate(&pair, now)
			if _, err := CompilePlacementAuthority(pair, placement.Serve, now); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestPlacementAuthorityOwnerConsentDoesNotGrantOrForceUse(t *testing.T) {
	pair, now := placementAuthorityFixture()
	pair.Tenant.Authority.EffectiveClusterGrants[0].MediaConsent.AllowServe = false
	got, err := CompilePlacementAuthority(pair, placement.Serve, now)
	if err != nil || len(got.Clusters) != 2 || len(got.Cells) != 1 {
		t.Fatalf("owner-denied serve capacity leaked: %+v %v", got, err)
	}
	if grant := got.SourceGrants["us"]; grant.CellID != "cell-us" || !grant.AllowIngest || grant.AllowServe {
		t.Fatalf("ingest-only source grant was lost: %+v", grant)
	}
	got.SourceGrants["us"] = PlacementSourceGrant{}
	if !pair.Tenant.Authority.EffectiveClusterGrants[0].MediaConsent.AllowIngest {
		t.Fatal("source grant projection aliases signed authority")
	}
	ingest, err := CompilePlacementAuthority(pair, placement.Ingest, now)
	if err != nil || len(ingest.Clusters) != 3 {
		t.Fatalf("serve consent incorrectly denied ingest: %+v %v", ingest, err)
	}
	pair.Tenant.IngestReady = false
	if _, err := CompilePlacementAuthority(pair, placement.Ingest, now); !errors.Is(err, ErrPlacementAuthorityNotReady) {
		t.Fatal("playback readiness promoted ingest")
	}
	pair.Tenant.IngestReady = true
	pair.Object.Authority.GetLiveStream().IngestMode = "pull"
	pair.Object.SourceReady = false
	if _, err := CompilePlacementAuthority(pair, placement.Ingest, now); !errors.Is(err, ErrPlacementAuthorityNotReady) {
		t.Fatal("publisher readiness promoted a managed source")
	}
}

func TestPlacementAuthorityMatchesExactDestinationQuery(t *testing.T) {
	pair, now := placementAuthorityFixture()
	authority, err := CompilePlacementAuthority(pair, placement.Serve, now)
	if err != nil {
		t.Fatal(err)
	}
	query := &placementpb.CandidateQuery{TenantId: authority.TenantID, ObjectId: authority.ObjectID, InternalName: authority.InternalName, Verb: placementpb.Verb_VERB_SERVE,
		Protocol: "hls", PolicyDigest: authority.PolicyDigest, PolicyRevision: authority.PolicyRevision, ParentRevision: authority.ParentRevision, ClusterIds: []string{"eu", "own"}}
	if !authority.MatchesQuery(query, "cell-eu") || authority.MatchesQuery(query, "cell-us") {
		t.Fatal("cell ownership not enforced")
	}
	for name, mutate := range map[string]func(*placementpb.CandidateQuery){
		"tenant":         func(q *placementpb.CandidateQuery) { q.TenantId = "other" },
		"object":         func(q *placementpb.CandidateQuery) { q.ObjectId = "other" },
		"name":           func(q *placementpb.CandidateQuery) { q.InternalName = "other" },
		"digest":         func(q *placementpb.CandidateQuery) { q.PolicyDigest = "" },
		"parent":         func(q *placementpb.CandidateQuery) { q.ParentRevision++ },
		"revision":       func(q *placementpb.CandidateQuery) { q.PolicyRevision++ },
		"verb":           func(q *placementpb.CandidateQuery) { q.Verb = placementpb.Verb_VERB_INGEST },
		"cluster":        func(q *placementpb.CandidateQuery) { q.ClusterIds = []string{"us"} },
		"empty":          func(q *placementpb.CandidateQuery) { q.ClusterIds = nil },
		"partial_census": func(q *placementpb.CandidateQuery) { q.ClusterIds = []string{"eu"} },
	} {
		t.Run(name, func(t *testing.T) {
			altered := proto.CloneOf(query)
			mutate(altered)
			if authority.MatchesQuery(altered, "cell-eu") {
				t.Fatal("unbound peer query accepted")
			}
		})
	}
}

func TestPlacementObservationCannotOutliveAuthority(t *testing.T) {
	req := placementObservationFixture()
	facts := req.Clusters["private"]
	facts.AuthorityUntil = req.Now.Add(2 * time.Second)
	req.Clusters["private"] = facts
	if got := observeOne(t, req); !got.ExpiresAt.Equal(facts.AuthorityUntil) {
		t.Fatalf("observation outlives signed grant: %s", got.ExpiresAt)
	}
}
