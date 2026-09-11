package placement

import (
	"testing"
	"time"
)

func TestCheckConstraintsDoesNotReelectManagedSource(t *testing.T) {
	now := time.Now()
	for _, scenario := range []string{"allowed", "foreign tenant", "no ingest consent", "unknown owner", "deny cluster", "allow private", "empty allow", "unknown charging", "rated", "expired charging", "preference elsewhere", "empty preferences", "invalid policy"} {
		t.Run(scenario, func(t *testing.T) {
			c := Candidate{TenantID: "tenant", ClusterID: "source", OwnerTenantID: "tenant", AllowedVerbs: []Verb{Ingest}}
			r := Request{TenantID: "tenant", Verb: Ingest, Now: now, Policy: &Policy{SchemaVersion: SchemaVersion, Groups: []Group{{ID: "entitled"}}}}
			want := Eligible
			switch scenario {
			case "foreign tenant":
				c.TenantID, want = "foreign", NotEntitled
			case "no ingest consent":
				c.AllowedVerbs, want = []Verb{Serve}, NotEntitled
			case "unknown owner":
				c.OwnerTenantID, want = "", UnknownOwnership
			case "deny cluster":
				r.Policy.Layers, want = []Constraints{{Deny: []Selector{{ClusterIDs: []string{"source"}}}}}, PolicyDenied
			case "allow private":
				r.Policy.Layers = []Constraints{{Allow: &SelectorSet{Any: []Selector{{Classes: []Class{Private}}}}}}
			case "empty allow":
				r.Policy.Layers, want = []Constraints{{Allow: &SelectorSet{}}}, PolicyDenied
			case "unknown charging", "rated", "expired charging":
				r.Policy.Layers = []Constraints{{Deny: []Selector{{Charging: []Charging{Rated}}}}}
				want = PolicyFactsUnavailable
				if scenario != "unknown charging" {
					c.Charging, c.ChargingUntil = Rated, now.Add(time.Minute)
					want = PolicyDenied
				}
				if scenario == "expired charging" {
					c.ChargingUntil, want = now, PolicyFactsUnavailable
				}
			case "preference elsewhere":
				r.Policy.Groups = []Group{{ID: "viewers-nearby", Match: Selector{ClusterIDs: []string{"elsewhere"}}}}
			case "empty preferences":
				r.Policy.Groups, want = nil, PolicyDenied
			case "invalid policy":
				r.Policy.SchemaVersion = 99
			}
			reason, err := CheckConstraints(r, c)
			if scenario == "invalid policy" {
				if err == nil {
					t.Fatal("invalid policy accepted")
				}
				return
			}
			if err != nil || reason != want {
				t.Fatalf("hard constraints: %s %v, want %s", reason, err, want)
			}
		})
	}
}
