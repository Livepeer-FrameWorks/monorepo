package placement

import (
	"strings"
	"testing"
	"time"

	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func commercialQuoteWireFixture(t *testing.T) (*pb.CommercialQuoteRequest, *pb.CommercialQuoteResponse, time.Time) {
	t.Helper()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	req := &pb.CommercialQuoteRequest{TenantId: "81000000-0000-4000-8000-000000000001", ObjectId: "live_stream:stream", Verb: pb.Verb_VERB_SERVE, PolicyRevision: 7, ParentRevision: 3, PolicyDigest: strings.Repeat("a", 64), ClusterIds: []string{"cluster"}, Bases: []*pb.ComparisonBasis{{Currency: "EUR", Unit: "serve:minutes=1;gib=2"}}}
	scope, digest, err := CanonicalCommercialQuoteRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	expiry := timestamppb.New(now.Add(30 * time.Second))
	resp := &pb.CommercialQuoteResponse{Scope: scope, RequestDigest: digest, ObservedAt: timestamppb.New(now), ExpiresAt: expiry, Usage: &pb.QuoteUsageEvidence{Status: pb.QuoteUsageStatus_QUOTE_USAGE_STATUS_NOT_REQUIRED}, Clusters: []*pb.ClusterCommercialQuote{{ClusterId: "cluster", Facts: &pb.CommercialFacts{Revision: strings.Repeat("b", 64), Charging: pb.Charging_CHARGING_RATED, ExpiresAt: proto.CloneOf(expiry), ServePrices: []*pb.Price{{Currency: "EUR", Unit: req.Bases[0].Unit, AmountMicros: 9007199254740993}}}}}}
	resp.EntitlementDigest = strings.Repeat("c", 64)
	return req, resp, now
}

func TestCommercialQuoteRequestCanonicalScope(t *testing.T) {
	req, _, _ := commercialQuoteWireFixture(t)
	req.ClusterIds = []string{"z", "a"}
	req.Bases = append(req.Bases, &pb.ComparisonBasis{Currency: "USD", Unit: "serve:minutes=1;gib=0"})
	before := proto.CloneOf(req)
	canonical, digest, err := CanonicalCommercialQuoteRequest(req)
	if err != nil || !proto.Equal(req, before) || canonical.ClusterIds[0] != "a" {
		t.Fatalf("canonicalization mutated input or failed: %v", err)
	}
	reordered := proto.CloneOf(req)
	reordered.ClusterIds[0], reordered.ClusterIds[1] = reordered.ClusterIds[1], reordered.ClusterIds[0]
	reordered.Bases[0], reordered.Bases[1] = reordered.Bases[1], reordered.Bases[0]
	_, reorderedDigest, err := CanonicalCommercialQuoteRequest(reordered)
	if err != nil || reorderedDigest != digest {
		t.Fatal("unordered request sets changed digest")
	}
	for name, mutate := range map[string]func(*pb.CommercialQuoteRequest){
		"tenant":  func(r *pb.CommercialQuoteRequest) { r.TenantId = "81000000-0000-4000-8000-000000000002" },
		"object":  func(r *pb.CommercialQuoteRequest) { r.ObjectId += "-other" },
		"verb":    func(r *pb.CommercialQuoteRequest) { r.Verb = pb.Verb_VERB_INGEST },
		"policy":  func(r *pb.CommercialQuoteRequest) { r.PolicyRevision++ },
		"parent":  func(r *pb.CommercialQuoteRequest) { r.ParentRevision++ },
		"digest":  func(r *pb.CommercialQuoteRequest) { r.PolicyDigest = strings.Repeat("c", 64) },
		"cluster": func(r *pb.CommercialQuoteRequest) { r.ClusterIds[0] += "-other" },
		"basis":   func(r *pb.CommercialQuoteRequest) { r.Bases[0].Unit += "0" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := proto.CloneOf(req)
			mutate(changed)
			_, changedDigest, err := CanonicalCommercialQuoteRequest(changed)
			if err != nil || changedDigest == digest {
				t.Fatalf("scope mutation did not change digest: %v", err)
			}
		})
	}
}

func TestCommercialQuoteResponseRejectsIncompleteOrReboundEvidence(t *testing.T) {
	req, resp, now := commercialQuoteWireFixture(t)
	encoded, err := proto.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	decoded := &pb.CommercialQuoteResponse{}
	if err := proto.Unmarshal(encoded, decoded); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCommercialQuoteResponse(req, decoded, now); err != nil || decoded.Clusters[0].Facts.ServePrices[0].AmountMicros != 9007199254740993 {
		t.Fatalf("wire quote failed or lost integer precision: %v", err)
	}
	for name, mutate := range map[string]func(*pb.CommercialQuoteResponse){
		"wrong object":   func(r *pb.CommercialQuoteResponse) { r.Scope.ObjectId += "-other" },
		"wrong digest":   func(r *pb.CommercialQuoteResponse) { r.RequestDigest = strings.Repeat("c", 64) },
		"missing cell":   func(r *pb.CommercialQuoteResponse) { r.Clusters = nil },
		"duplicate cell": func(r *pb.CommercialQuoteResponse) { r.Clusters = append(r.Clusters, proto.CloneOf(r.Clusters[0])) },
		"different cell": func(r *pb.CommercialQuoteResponse) { r.Clusters[0].ClusterId = "other" },
		"expired":        func(r *pb.CommercialQuoteResponse) { r.ExpiresAt = timestamppb.New(now) },
		"future":         func(r *pb.CommercialQuoteResponse) { r.ObservedAt = timestamppb.New(now.Add(2 * time.Second)) },
		"too long":       func(r *pb.CommercialQuoteResponse) { r.ExpiresAt = timestamppb.New(now.Add(2 * time.Minute)) },
		"facts expire earlier": func(r *pb.CommercialQuoteResponse) {
			r.Clusters[0].Facts.ExpiresAt = timestamppb.New(now.Add(time.Second))
		},
		"invalid revision": func(r *pb.CommercialQuoteResponse) { r.Clusters[0].Facts.Revision = "opaque" },
		"unknown field": func(r *pb.CommercialQuoteResponse) {
			r.Clusters[0].Facts.ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01})
		},
		"unknown charging": func(r *pb.CommercialQuoteResponse) { r.Clusters[0].Facts.Charging = 99 },
		"priced permanently free": func(r *pb.CommercialQuoteResponse) {
			r.Clusters[0].Facts.Charging = pb.Charging_CHARGING_PERMANENTLY_FREE
		},
		"wrong verb": func(r *pb.CommercialQuoteResponse) {
			r.Clusters[0].Facts.IngestPrices = r.Clusters[0].Facts.ServePrices
			r.Clusters[0].Facts.ServePrices = nil
		},
		"silent omission":   func(r *pb.CommercialQuoteResponse) { r.Clusters[0].Facts.ServePrices = nil },
		"unrequested price": func(r *pb.CommercialQuoteResponse) { r.Clusters[0].Facts.ServePrices[0].Currency = "USD" },
		"contradictory absence": func(r *pb.CommercialQuoteResponse) {
			r.Clusters[0].Unavailable = []*pb.UnavailableQuote{{Basis: proto.CloneOf(req.Bases[0]), Reason: pb.QuoteUnavailableReason_QUOTE_UNAVAILABLE_REASON_USAGE_UNKNOWN}}
		},
		"missing usage": func(r *pb.CommercialQuoteResponse) { r.Usage = nil },
		"unknown usage": func(r *pb.CommercialQuoteResponse) { r.Usage.Status = 99 },
		"unproved usage price": func(r *pb.CommercialQuoteResponse) {
			r.Clusters[0].RecordedUsageBases = []*pb.ComparisonBasis{proto.CloneOf(req.Bases[0])}
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := proto.CloneOf(resp)
			mutate(changed)
			if err := ValidateCommercialQuoteResponse(req, changed, now); err == nil {
				t.Fatal("accepted invalid commercial evidence")
			}
		})
	}
}

func TestCommercialQuoteRecordedUsageCannotOutlivePeriod(t *testing.T) {
	req, resp, now := commercialQuoteWireFixture(t)
	resp.Usage = &pb.QuoteUsageEvidence{Status: pb.QuoteUsageStatus_QUOTE_USAGE_STATUS_COVERED_THROUGH_CUTOFF, PeriodStart: timestamppb.New(now.Add(-time.Hour)), PeriodEnd: proto.CloneOf(resp.ExpiresAt), Through: timestamppb.New(now.Add(-5 * time.Minute))}
	resp.Clusters[0].RecordedUsageBases = []*pb.ComparisonBasis{proto.CloneOf(req.Bases[0])}
	if err := ValidateCommercialQuoteResponse(req, resp, now); err != nil {
		t.Fatal(err)
	}
	resp.Usage.PeriodEnd = timestamppb.New(now.Add(time.Second))
	if err := ValidateCommercialQuoteResponse(req, resp, now); err == nil {
		t.Fatal("accepted quote extending consumed allowance across billing periods")
	}
}
