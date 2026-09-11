package grpc

import (
	"context"
	"errors"
	"testing"
	"time"

	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/proto"
)

func TestCollectMediaObjectCommercialOnlyRequiredVerbs(t *testing.T) {
	for _, test := range []struct {
		name   string
		ingest bool
		serve  bool
	}{{"neither", false, false}, {"ingest", true, false}, {"serve", false, true}} {
		t.Run(test.name, func(t *testing.T) {
			tenant, object := quotedCommercialAuthorityFixture()
			if !test.ingest {
				tenant.MediaPlacement.Ingest = nil
			}
			if !test.serve {
				tenant.MediaPlacement.Serve = nil
			}
			object.CommercialQuotes = []*pb.CommercialQuoteResponse{{RequestDigest: "obsolete"}}
			before, parent := proto.CloneOf(object), proto.CloneOf(tenant)
			until := time.Now().Add(time.Hour)
			calls := 0
			source := commercialSourceFunc(func(_ context.Context, request *pb.CommercialQuoteRequest) (*pb.CommercialQuoteResponse, error) {
				calls++
				if (request.Verb == pb.Verb_VERB_INGEST && !test.ingest) || (request.Verb == pb.Verb_VERB_SERVE && !test.serve) {
					return nil, errors.New("quote requested for noncommercial verb")
				}
				return commercialResponse(tenant, request)
			})
			result, err := collectMediaObjectCommercial(context.Background(), source, tenant, object, until)
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if test.ingest || test.serve {
				want = 1
			}
			if calls != want || len(result.payload.CommercialQuotes) != want || !proto.Equal(before, object) || !proto.Equal(parent, tenant) {
				t.Fatalf("incorrect quote selection or mutation: calls=%d quotes=%d", calls, len(result.payload.CommercialQuotes))
			}
			if want == 0 && (!result.validUntil.Equal(until) || result.revision != "") {
				t.Fatal("noncommercial authority acquired quote expiry or false owner provenance")
			}
			if want == 1 && (result.revision == "" || result.validUntil.After(time.Now().Add(30*time.Second))) {
				t.Fatal("required quote lost provenance or expiry")
			}
			result, err = collectMediaObjectCommercial(context.Background(), nil, tenant, object, until)
			if (err != nil) != (want != 0) || (result == nil) != (want != 0) {
				t.Fatalf("missing commercial owner handled incorrectly: %v", err)
			}
		})
	}
}

func TestCollectMediaObjectNoncommercialStillValidatesScope(t *testing.T) {
	for _, scenario := range []string{"parent revision", "foreign tenant", "invalid policy", "missing grants", "missing object", "expired", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			tenant, object := commercialAuthorityFixture()
			tenant.MediaPlacement.Ingest, tenant.MediaPlacement.Serve = nil, nil
			until := time.Now().Add(time.Hour)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch scenario {
			case "parent revision":
				object.PlacementTenantRevision++
			case "foreign tenant":
				object.TenantId = "81000000-0000-4000-8000-000000000002"
			case "invalid policy":
				object.MediaPlacement.Serve = &pb.Rules{SchemaVersion: 999}
			case "missing grants":
				tenant.EffectiveClusterGrants = nil
			case "missing object":
				object.GetLiveStream().StreamId = ""
			case "expired":
				until = time.Now().Add(-time.Second)
			case "canceled":
				cancel()
			}
			before := proto.CloneOf(object)
			result, err := collectMediaObjectCommercial(ctx, nil, tenant, object, until)
			if err == nil || result != nil || !proto.Equal(before, object) {
				t.Fatalf("invalid noncommercial authority escaped validation: %v", err)
			}
		})
	}
}
