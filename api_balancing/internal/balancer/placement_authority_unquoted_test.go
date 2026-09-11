package balancer

import (
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
)

func TestPlacementAuthorityWithoutQuotesPreservesLeaseAndUnknownFacts(t *testing.T) {
	for _, verb := range []placement.Verb{placement.Ingest, placement.Serve} {
		for _, scenario := range []string{"distance", "charging", "price"} {
			t.Run(string(verb)+"/"+scenario, func(t *testing.T) {
				pair, now := placementAuthorityFixture()
				until := now.Add(5 * time.Minute)
				pair.Tenant.ValidUntil, pair.Object.ValidUntil = until, until
				for _, grant := range pair.Tenant.Authority.EffectiveClusterGrants {
					grant.CommercialFacts = nil
				}
				var rules *pb.Rules
				switch scenario {
				case "charging":
					rules = &pb.Rules{SchemaVersion: 1, Constraints: &pb.Constraints{Deny: []*pb.Selector{{Charging: []pb.Charging{pb.Charging_CHARGING_RATED}}}}}
				case "price":
					unit := "ingest:gib=1"
					if verb == placement.Serve {
						unit = "serve:minutes=1;gib=1"
					}
					rules = &pb.Rules{SchemaVersion: 1, Preferences: &pb.Preferences{Groups: []*pb.Group{{Id: "cheap", Order: pb.Order_ORDER_PRICE, PriceCurrency: "USD", PriceUnit: unit}}}}
				}
				if verb == placement.Ingest {
					pair.Tenant.Authority.MediaPlacement.Ingest = rules
				} else {
					pair.Tenant.Authority.MediaPlacement.Serve = rules
				}
				authority, err := CompilePlacementAuthority(pair, verb, now)
				if err != nil || !authority.ExpiresAt.Equal(until) || authority.CommercialQuote != nil {
					t.Fatalf("unquoted authority gained a quote lease or failed compilation: %v", err)
				}
				facts := authority.Clusters["us"]
				if facts.Charging != placement.ChargingUnknown || facts.Price != nil || len(facts.Prices) != 0 || !facts.ChargingUntil.IsZero() || facts.ChargingRevision != "" {
					t.Fatal("missing evidence became a charging classification or price")
				}
				decision, err := placement.Evaluate(placement.Request{TenantID: authority.TenantID, Verb: verb, Now: now, Policy: authority.Policy, Complete: true,
					Candidates: []placement.Candidate{{TenantID: authority.TenantID, ClusterID: "us", NodeID: "us-edge", Official: facts.Official, OwnerTenantID: facts.OwnerTenantID,
						AllowedVerbs: facts.AllowedVerbs, Charging: facts.Charging, ChargingUntil: facts.ChargingUntil, ObservedAt: now, ExpiresAt: until,
						Capacity: placement.CapacityAvailable, Presence: placement.Present, SourceFeasible: true, BWAvailable: 100, BWLimit: 1000, RAMUsed: 1, RAMMax: 100}}})
				if err != nil {
					t.Fatal(err)
				}
				if (len(decision.Choices) != 0) != (scenario == "distance") {
					t.Fatalf("commercial evidence requirement changed admission: %+v", decision)
				}
			})
		}
	}
}
