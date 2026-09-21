package placement

import (
	"slices"
	"testing"

	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
)

func TestConsentVerbs(t *testing.T) {
	for _, test := range []struct {
		name    string
		consent *placementpb.CapacityConsent
		want    []Verb
	}{
		{"missing consent", nil, []Verb{}},
		{"nothing", &placementpb.CapacityConsent{AllowExternalSource: true}, []Verb{}},
		{"ingest", &placementpb.CapacityConsent{AllowIngest: true}, []Verb{Ingest}},
		{"serve", &placementpb.CapacityConsent{AllowServe: true}, []Verb{Serve}},
		{"both", &placementpb.CapacityConsent{AllowIngest: true, AllowServe: true}, []Verb{Ingest, Serve}},
	} {
		if got := ConsentVerbs(test.consent); !slices.Equal(got, test.want) {
			t.Errorf("%s: ConsentVerbs = %v, want %v", test.name, got, test.want)
		}
	}
}
