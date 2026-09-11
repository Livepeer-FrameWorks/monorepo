package grpc

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func previewQuoteResponse(request *placementpb.CommercialQuoteRequest, inventory *mediaPreviewInventory) (*placementpb.CommercialQuoteResponse, error) {
	scope, digest, err := placement.CanonicalCommercialQuoteRequest(request)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	response := &placementpb.CommercialQuoteResponse{Scope: scope, RequestDigest: digest, EntitlementDigest: inventory.entitlementDigest, ObservedAt: timestamppb.New(now), ExpiresAt: timestamppb.New(now.Add(15 * time.Second)), Usage: &placementpb.QuoteUsageEvidence{Status: placementpb.QuoteUsageStatus_QUOTE_USAGE_STATUS_NOT_REQUIRED}}
	for _, cluster := range scope.ClusterIds {
		facts := &placementpb.CommercialFacts{Charging: placementpb.Charging_CHARGING_RATED, Revision: strings.Repeat("b", 64), ExpiresAt: proto.CloneOf(response.ExpiresAt)}
		for _, basis := range scope.Bases {
			amount := uint64(100)
			if cluster == "own-eu" {
				amount = 5
			}
			price := &placementpb.Price{AmountMicros: amount, Currency: basis.Currency, Unit: basis.Unit}
			if scope.Verb == placementpb.Verb_VERB_INGEST {
				facts.IngestPrices = append(facts.IngestPrices, price)
			} else {
				facts.ServePrices = append(facts.ServePrices, price)
			}
		}
		response.Clusters = append(response.Clusters, &placementpb.ClusterCommercialQuote{ClusterId: cluster, Facts: facts})
	}
	return response, nil
}

func TestMediaPlacementPreviewQuoteBindsDraftEntitlementAndBasis(t *testing.T) {
	for _, scenario := range []string{"ok", "object", "digest", "entitlement", "missing price", "expired", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			preview, _, inventory := previewCapacityFixture(t)
			preview.policy = &placement.Policy{SchemaVersion: 1, Groups: []placement.Group{{ID: "cheap", Order: placement.PriceFirst, PriceCurrency: "EUR", PriceUnit: "serve:minutes=1;gib=1"}, {ID: "other", Order: placement.PriceFirst, PriceCurrency: "EUR", PriceUnit: "serve:minutes=1;gib=1"}}}
			var err error
			preview.digest, err = placement.Digest(preview.policy)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			server := &CommodoreServer{authorityCommercialSource: commercialSourceFunc(func(callCtx context.Context, request *placementpb.CommercialQuoteRequest) (*placementpb.CommercialQuoteResponse, error) {
				if request.TenantId != preview.snapshot.Scope.TenantID || request.ObjectId != "tenant:"+request.TenantId || request.PolicyDigest != preview.digest || len(request.ClusterIds) != 2 || len(request.Bases) != 1 {
					t.Error("quote lost draft scope, census or unique bases")
				}
				if deadline, ok := callCtx.Deadline(); !ok || time.Until(deadline) > 4*time.Second {
					t.Error("quote deadline missing")
				}
				response, responseErr := previewQuoteResponse(request, inventory)
				if responseErr != nil {
					return nil, responseErr
				}
				switch scenario {
				case "object":
					response.Scope.ObjectId = "other"
				case "digest":
					response.Scope.PolicyDigest = strings.Repeat("c", 64)
				case "entitlement":
					response.EntitlementDigest = strings.Repeat("d", 64)
				case "missing price":
					response.Clusters[0].Facts.ServePrices = nil
				case "expired":
					response.ExpiresAt = timestamppb.New(time.Now().Add(-time.Second))
				case "cancelled":
					cancel()
				}
				return response, nil
			})}
			quote := server.collectPreviewQuote(ctx, preview, inventory, placementpb.Verb_VERB_SERVE)
			if (quote != nil) != (scenario == "ok") {
				t.Fatalf("unbound quote accepted (%s): %+v", scenario, quote)
			}
		})
	}
}
