package grpc

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	purserclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/purser"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	clusterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	qmpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus"
	grpcpkg "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const quoteTenant = "81000000-0000-4000-8000-000000000001"
const quoteTier = "82000000-0000-4000-8000-000000000001"
const quoteSubscription = "83000000-0000-4000-8000-000000000001"

type quoteEntitlementStub struct {
	commercialQuartermasterStub
	read func(context.Context, string) (*qmpb.GetTenantEntitlementResponse, error)
}

func (s *quoteEntitlementStub) GetTenantEntitlement(ctx context.Context, tenant string) (*qmpb.GetTenantEntitlementResponse, error) {
	return s.read(ctx, tenant)
}

func quoteRPCFixture() (*pb.CommercialQuoteRequest, *qmpb.GetTenantEntitlementResponse) {
	return &pb.CommercialQuoteRequest{TenantId: quoteTenant, ObjectId: "live_stream:stream", Verb: pb.Verb_VERB_SERVE, PolicyRevision: 7, ParentRevision: 3, PolicyDigest: strings.Repeat("a", 64), ClusterIds: []string{"official"}, Bases: []*pb.ComparisonBasis{{Currency: "EUR", Unit: "serve:minutes=1;gib=2"}}},
		&qmpb.GetTenantEntitlementResponse{AllowedClusterIds: []string{"official"}, EffectiveAccess: []*clusterpb.TenantClusterPeer{{ClusterId: "official", ClusterClass: "platform_official", AccessActive: true, SubscriptionStatus: "active", AccessSource: clusterpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_PLATFORM_TIER, MediaConsent: &pb.CapacityConsent{Revision: 2, AllowIngest: true, AllowServe: true, AllowExternalSource: true}}}}
}

func expectQuoteSnapshot(mock sqlmock.Sqlmock, now time.Time, failure string) {
	mock.ExpectBegin()
	mock.ExpectQuery("clock_timestamp").WillReturnRows(sqlmock.NewRows([]string{"observed_at"}).AddRow(now))
	tier := mock.ExpectQuery("tenant_subscriptions").WithArgs(quoteTenant)
	if failure == "read" {
		tier.WillReturnError(errors.New("private database detail"))
		mock.ExpectRollback()
		return
	}
	tier.WillReturnRows(sqlmock.NewRows([]string{"tier_id", "tier_name", "base_price", "currency", "metering_enabled", "subscription_id"}).AddRow(quoteTier, "pro", "10", "EUR", true, quoteSubscription))
	mock.ExpectQuery("tier_pricing_rules").WithArgs(quoteTier).WillReturnRows(sqlmock.NewRows([]string{"meter", "model", "currency", "included_quantity", "unit_price", "config"}).AddRow("egress_gb", "all_usage", "EUR", "0", "0.05", "{}").AddRow("delivered_minutes", "all_usage", "EUR", "0", "0.02", "{}"))
	mock.ExpectQuery("subscription_pricing_overrides").WithArgs(quoteSubscription).WillReturnRows(sqlmock.NewRows([]string{"meter", "model", "currency", "included_quantity", "unit_price", "config"}))
	mock.ExpectQuery("tier_entitlements").WithArgs(quoteTier).WillReturnRows(sqlmock.NewRows([]string{"key", "value"}))
	mock.ExpectQuery("subscription_entitlement_overrides").WithArgs(quoteSubscription).WillReturnRows(sqlmock.NewRows([]string{"key", "value"}))
	contextRows := sqlmock.NewRows([]string{"period_start", "period_end", "pending_tier_id", "pending_effective_at"})
	if failure == "pending due" {
		contextRows.AddRow(nil, nil, quoteTier, now.Add(-time.Second))
	} else {
		contextRows.AddRow(nil, nil, nil, nil)
	}
	mock.ExpectQuery("ReadPlacementSubscriptionContext").WithArgs(quoteTenant, quoteSubscription).WillReturnRows(contextRows)
	mock.ExpectQuery("cluster_pricing_history").WithArgs("official", now).WillReturnRows(sqlmock.NewRows([]string{"version_id", "pricing_model", "currency", "base_price", "rates"}))
	mock.ExpectQuery("ListPlacementPricingBoundaries").WithArgs(pq.Array([]string{"official"}), now).WillReturnRows(sqlmock.NewRows([]string{"cluster_id", "valid_until"}))
	commit := mock.ExpectCommit()
	if failure == "commit" {
		commit.WillReturnError(errors.New("private commit failure"))
	}
}

func TestMediaPlacementQuoteServiceAuthBeforeDependencies(t *testing.T) {
	req, _ := quoteRPCFixture()
	server := &PurserServer{}
	for _, auth := range []string{"", "jwt", "api_token", "platform_operator"} {
		ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, auth)
		resp, err := server.GetMediaPlacementQuote(ctx, req)
		if resp != nil || status.Code(err) != codes.PermissionDenied {
			t.Fatalf("%s bypassed service auth: %v %v", auth, resp, err)
		}
	}
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")
	if _, err := server.GetMediaPlacementQuote(ctx, nil); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("invalid request = %v", err)
	}
	if _, err := server.GetMediaPlacementQuote(ctx, req); status.Code(err) != codes.Unavailable {
		t.Fatalf("unconfigured dependencies = %v", err)
	}
}

func TestMediaPlacementQuoteRechecksOutsideTransaction(t *testing.T) {
	for _, scenario := range []string{"unchanged", "health only", "consent revision", "consent flag", "ownership", "revoked", "read", "commit", "pending due"} {
		t.Run(scenario, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			now := time.Now().UTC()
			expectQuoteSnapshot(mock, now, scenario)
			req, entitlement := quoteRPCFixture()
			calls := 0
			stub := &quoteEntitlementStub{}
			stub.read = func(ctx context.Context, tenant string) (*qmpb.GetTenantEntitlementResponse, error) {
				calls++
				deadline, ok := ctx.Deadline()
				if tenant != quoteTenant || !ok || time.Until(deadline) > time.Second || db.Stats().InUse != 0 {
					t.Fatal("entitlement read not tenant/deadline scoped or held database transaction")
				}
				out := proto.CloneOf(entitlement)
				if calls == 2 {
					switch scenario {
					case "health only":
						out.EffectiveAccess[0].HealthStatus = "offline"
					case "consent revision":
						out.EffectiveAccess[0].MediaConsent.Revision++
					case "consent flag":
						out.EffectiveAccess[0].MediaConsent.AllowServe = false
					case "ownership":
						out.EffectiveAccess[0].OwnerTenantId = quoteTenant
					case "revoked":
						out.EffectiveAccess[0].AccessActive = false
					}
				}
				return out, nil
			}
			server := &PurserServer{db: db, quartermasterClient: stub}
			ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")
			response, err := server.GetMediaPlacementQuote(ctx, req)
			want := codes.OK
			switch scenario {
			case "consent revision", "consent flag", "ownership":
				want = codes.Aborted
			case "revoked":
				want = codes.PermissionDenied
			case "read", "commit":
				want = codes.Unavailable
			case "pending due":
				want = codes.FailedPrecondition
			}
			if status.Code(err) != want {
				t.Fatalf("status = %v, want %v", err, want)
			}
			if want != codes.OK && response != nil {
				t.Fatal("failed read returned partial quote")
			}
			if want == codes.OK {
				if err := placement.ValidateCommercialQuoteResponse(req, response, time.Now()); err != nil {
					t.Fatal(err)
				}
				if response.Clusters[0].Facts.ServePrices[0].AmountMicros != 120000 {
					t.Fatalf("wrong quote: %+v", response)
				}
			}
			wantCalls := 2
			if scenario == "read" || scenario == "commit" {
				wantCalls = 1
			}
			if calls != wantCalls || stub.materialized != nil || stub.bootstrapped {
				t.Fatal("wrong revalidation count or read mutated cluster access")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMediaPlacementQuoteRejectsAmbiguousEntitlement(t *testing.T) {
	for name, mutate := range map[string]func(*qmpb.GetTenantEntitlementResponse){
		"missing membership": func(r *qmpb.GetTenantEntitlementResponse) { r.AllowedClusterIds = nil },
		"duplicate membership": func(r *qmpb.GetTenantEntitlementResponse) {
			r.AllowedClusterIds = append(r.AllowedClusterIds, "official")
		},
		"missing peer": func(r *qmpb.GetTenantEntitlementResponse) { r.EffectiveAccess = nil },
		"duplicate peer": func(r *qmpb.GetTenantEntitlementResponse) {
			r.EffectiveAccess = append(r.EffectiveAccess, proto.CloneOf(r.EffectiveAccess[0]))
		},
		"unknown source":  func(r *qmpb.GetTenantEntitlementResponse) { r.EffectiveAccess[0].AccessSource = 99 },
		"missing consent": func(r *qmpb.GetTenantEntitlementResponse) { r.EffectiveAccess[0].MediaConsent = nil },
		"unknown consent": func(r *qmpb.GetTenantEntitlementResponse) {
			r.EffectiveAccess[0].MediaConsent.ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01})
		},
		"wrong owner": func(r *qmpb.GetTenantEntitlementResponse) {
			r.EffectiveAccess[0].AccessSource = clusterpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_OWNER
		},
		"invalid owner":          func(r *qmpb.GetTenantEntitlementResponse) { r.EffectiveAccess[0].OwnerTenantId = "not-a-uuid" },
		"unknown class":          func(r *qmpb.GetTenantEntitlementResponse) { r.EffectiveAccess[0].ClusterClass = "unknown" },
		"platform grant private": func(r *qmpb.GetTenantEntitlementResponse) { r.EffectiveAccess[0].ClusterClass = "tenant_private" },
		"expired": func(r *qmpb.GetTenantEntitlementResponse) {
			r.EffectiveAccess[0].AccessExpiresAt = timestamppb.New(time.Now().Add(-time.Second))
		},
		"pending": func(r *qmpb.GetTenantEntitlementResponse) {
			r.EffectiveAccess[0].SubscriptionStatus = "pending_approval"
		},
	} {
		t.Run(name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			req, response := quoteRPCFixture()
			mutate(response)
			stub := &quoteEntitlementStub{read: func(context.Context, string) (*qmpb.GetTenantEntitlementResponse, error) { return response, nil }}
			server := &PurserServer{db: db, quartermasterClient: stub}
			ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")
			got, err := server.GetMediaPlacementQuote(ctx, req)
			if err == nil || got != nil {
				t.Fatal("invalid entitlement returned commercial evidence")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMediaPlacementQuoteDependencyErrorStatus(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want codes.Code
	}{
		{context.Canceled, codes.Canceled}, {context.DeadlineExceeded, codes.DeadlineExceeded},
		{status.Error(codes.Canceled, "private detail"), codes.Canceled}, {status.Error(codes.DeadlineExceeded, "private detail"), codes.DeadlineExceeded},
		{sql.ErrNoRows, codes.FailedPrecondition}, {errors.New("private detail"), codes.Unavailable},
	} {
		err := placementQuoteRPCError(tc.err)
		if status.Code(err) != tc.want || strings.Contains(err.Error(), "private detail") {
			t.Fatalf("error mapping = %v", err)
		}
	}
}

func TestMediaPlacementQuoteProductionTransportAndTypedClient(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	expectQuoteSnapshot(mock, time.Now().UTC(), "")
	req, entitlement := quoteRPCFixture()
	stub := &quoteEntitlementStub{read: func(_ context.Context, tenant string) (*qmpb.GetTenantEntitlementResponse, error) {
		if tenant != quoteTenant {
			return nil, errors.New("unexpected tenant")
		}
		return proto.CloneOf(entitlement), nil
	}}
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	secret := []byte("placement-quote-wire-test-secret")
	metrics := &ServerMetrics{
		GRPCRequests: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_placement_quote_requests_total"}, []string{"method", "status"}),
		GRPCDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "test_placement_quote_duration_seconds"}, []string{"method"}),
	}
	cfg := GRPCServerConfig{DB: db, Logger: logging.NewLogger(), ServiceToken: "service-secret", JWTSecret: secret, Metrics: metrics, AllowInsecure: true}
	server := grpcpkg.NewServer(grpcpkg.ChainUnaryInterceptor(purserUnaryInterceptors(cfg)...))
	purserpb.RegisterClusterPricingServiceServer(server, &PurserServer{db: db, logger: cfg.Logger, quartermasterClient: stub})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	conn, err := grpcpkg.NewClient(listener.Addr().String(), grpcpkg.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	raw := purserpb.NewClusterPricingServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	jwt, err := auth.GenerateJWT("user-1", quoteTenant, "owner@example.com", "owner", secret)
	if err != nil {
		t.Fatal(err)
	}
	for _, credential := range []string{"", "Bearer " + jwt} {
		callCtx := metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", credential))
		resp, callErr := raw.GetMediaPlacementQuote(callCtx, req)
		if resp != nil || status.Code(callErr) != codes.PermissionDenied && status.Code(callErr) != codes.Unauthenticated {
			t.Fatalf("non-service wire request reached pricing: %+v %v", resp, callErr)
		}
	}
	client, err := purserclient.NewGRPCClient(purserclient.GRPCConfig{GRPCAddr: listener.Addr().String(), ServiceToken: "service-secret", AllowInsecure: true, Timeout: 3 * time.Second, Logger: cfg.Logger})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	caller := context.WithValue(ctx, ctxkeys.KeyJWTToken, jwt)
	caller = context.WithValue(caller, ctxkeys.KeyTenantID, "other-tenant")
	caller = context.WithValue(caller, ctxkeys.KeyUserID, "user-1")
	response, err := client.GetMediaPlacementQuote(caller, req)
	if err != nil {
		t.Fatal(err)
	}
	if response.Scope.TenantId != quoteTenant || response.Clusters[0].Facts.ServePrices[0].AmountMicros != 120000 {
		t.Fatalf("wire quote scope or exact amount changed: %+v", response)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
