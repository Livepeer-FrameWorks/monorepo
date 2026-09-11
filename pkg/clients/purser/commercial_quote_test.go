package purser

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type commercialQuoteWireServer struct {
	purserpb.UnimplementedClusterPricingServiceServer
	mutate func(*pb.CommercialQuoteResponse)
}

func (s *commercialQuoteWireServer) GetMediaPlacementQuote(ctx context.Context, req *pb.CommercialQuoteRequest) (*pb.CommercialQuoteResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	if auth := md.Get("authorization"); len(auth) != 1 || auth[0] != "Bearer quote-service-secret" || len(md.Get("x-tenant-id")) != 0 || len(md.Get("x-user-id")) != 0 {
		return nil, status.Error(codes.Unauthenticated, "quote client did not isolate service credentials")
	}
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 3*time.Second {
		return nil, status.Error(codes.InvalidArgument, "quote client omitted deadline")
	}
	scope, digest, err := placement.CanonicalCommercialQuoteRequest(req)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	resp := &pb.CommercialQuoteResponse{Scope: scope, RequestDigest: digest, ObservedAt: timestamppb.New(now), ExpiresAt: timestamppb.New(now.Add(30 * time.Second)), Usage: &pb.QuoteUsageEvidence{Status: pb.QuoteUsageStatus_QUOTE_USAGE_STATUS_NOT_REQUIRED}}
	resp.EntitlementDigest = strings.Repeat("c", 64)
	for _, id := range scope.ClusterIds {
		facts := &pb.CommercialFacts{Revision: strings.Repeat("b", 64), ExpiresAt: proto.CloneOf(resp.ExpiresAt), Charging: pb.Charging_CHARGING_RATED}
		for _, basis := range scope.Bases {
			facts.ServePrices = append(facts.ServePrices, &pb.Price{Currency: basis.Currency, Unit: basis.Unit, AmountMicros: 9007199254740993})
		}
		resp.Clusters = append(resp.Clusters, &pb.ClusterCommercialQuote{ClusterId: id, Facts: facts})
	}
	if s.mutate != nil {
		s.mutate(resp)
	}
	return resp, nil
}

func TestCommercialQuoteClientValidatesWireEvidence(t *testing.T) {
	for name, mutate := range map[string]func(*pb.CommercialQuoteResponse){
		"valid":             nil,
		"wrong stream":      func(r *pb.CommercialQuoteResponse) { r.Scope.ObjectId = "other-stream" },
		"expired":           func(r *pb.CommercialQuoteResponse) { r.ExpiresAt = timestamppb.New(time.Now().Add(-time.Second)) },
		"missing cluster":   func(r *pb.CommercialQuoteResponse) { r.Clusters = nil },
		"unrequested ratio": func(r *pb.CommercialQuoteResponse) { r.Clusters[0].Facts.ServePrices[0].Unit = "serve:minutes=2;gib=2" },
		"unknown evidence":  func(r *pb.CommercialQuoteResponse) { r.Usage.ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01}) },
	} {
		t.Run(name, func(t *testing.T) {
			listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			server := grpc.NewServer()
			purserpb.RegisterClusterPricingServiceServer(server, &commercialQuoteWireServer{mutate: mutate})
			go func() { _ = server.Serve(listener) }()
			t.Cleanup(func() { server.Stop(); _ = listener.Close() })
			client, err := NewGRPCClient(GRPCConfig{GRPCAddr: listener.Addr().String(), ServiceToken: "quote-service-secret", AllowInsecure: true, Timeout: 3 * time.Second, Logger: logging.NewLogger()})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close() })
			req := &pb.CommercialQuoteRequest{TenantId: "81000000-0000-4000-8000-000000000001", ObjectId: "live_stream:stream", Verb: pb.Verb_VERB_SERVE, PolicyRevision: 7, ParentRevision: 3, PolicyDigest: strings.Repeat("a", 64), ClusterIds: []string{"z", "a"}, Bases: []*pb.ComparisonBasis{{Currency: "EUR", Unit: "serve:minutes=1;gib=2"}}}
			before := proto.CloneOf(req)
			ctx := context.WithValue(context.Background(), ctxkeys.KeyJWTToken, "caller-jwt")
			ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, "caller-tenant")
			ctx = context.WithValue(ctx, ctxkeys.KeyUserID, "caller-user")
			ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer stale", "x-tenant-id", "stale-tenant", "x-user-id", "stale-user"))
			response, err := client.GetMediaPlacementQuote(ctx, req)
			if mutate == nil {
				if err != nil || response.Clusters[0].ClusterId != "a" || response.Clusters[0].Facts.ServePrices[0].AmountMicros != 9007199254740993 {
					t.Fatalf("valid service quote failed: %+v %v", response, err)
				}
			} else if err == nil || response != nil {
				t.Fatal("client published unvalidated commercial quote")
			}
			if !proto.Equal(before, req) {
				t.Fatal("client changed caller request")
			}
		})
	}
}
