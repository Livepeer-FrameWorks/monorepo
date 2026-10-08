package grpc

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	purserclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/purser"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type countingAdmissionPurser struct {
	purserpb.UnimplementedBillingServiceServer
	calls atomic.Int32
}

func (p *countingAdmissionPurser) GetTenantAdmissionStatus(context.Context, *purserpb.GetTenantAdmissionStatusRequest) (*purserpb.GetTenantAdmissionStatusResponse, error) {
	p.calls.Add(1)
	// Long enough that every concurrent ValidateTenant arrives while the
	// first Purser call is still in flight.
	time.Sleep(200 * time.Millisecond)
	return &purserpb.GetTenantAdmissionStatusResponse{BillingModel: "prepaid", TierName: "pro"}, nil
}

// Bridge calls ValidateTenant on every cache miss; a burst of misses for one
// tenant must cost Purser one call, and calls inside the freshness window
// none.
func TestValidateTenantSharesOnePurserCallPerTenant(t *testing.T) {
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	purser := &countingAdmissionPurser{}
	purserServer := grpc.NewServer()
	purserpb.RegisterBillingServiceServer(purserServer, purser)
	go func() { _ = purserServer.Serve(listener) }()
	defer purserServer.Stop()
	client, err := purserclient.NewGRPCClient(purserclient.GRPCConfig{
		GRPCAddr: listener.Addr().String(), Timeout: 5 * time.Second, Logger: logging.NewLogger(),
		ServiceToken: "service-token", PreferServiceToken: true, AllowInsecure: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	mock.MatchExpectationsInOrder(false)
	const tenantID = "tenant-burst"
	const concurrent, sequential = 16, 4
	for range concurrent + sequential {
		mock.ExpectQuery(`FROM quartermaster\.tenants\s+WHERE id = \$1`).WithArgs(tenantID).
			WillReturnRows(sqlmock.NewRows([]string{"name", "is_active", "rate_limit_per_minute", "rate_limit_burst"}).
				AddRow("Tenant", true, int32(60), int32(120)))
	}

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, client, nil, nil)
	validate := func() {
		resp, validateErr := server.ValidateTenant(serviceCtx(), &quartermasterpb.ValidateTenantRequest{TenantId: tenantID})
		if validateErr != nil {
			t.Errorf("ValidateTenant: %v", validateErr)
			return
		}
		if resp.GetBillingStatusUnavailable() || resp.GetBillingModel() != "prepaid" || resp.GetTierName() != "pro" {
			t.Errorf("ValidateTenant billing = %+v, want Purser's answer", resp)
		}
	}
	var wg sync.WaitGroup
	for range concurrent {
		wg.Add(1)
		go func() {
			defer wg.Done()
			validate()
		}()
	}
	wg.Wait()
	for range sequential {
		validate()
	}
	if got := purser.calls.Load(); got != 1 {
		t.Fatalf("Purser GetTenantAdmissionStatus calls = %d for %d ValidateTenant calls of one tenant, want 1", got, concurrent+sequential)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A transient Purser failure is answered with the tenant's last good status,
// while anything Purser actually answers, including a suspension, replaces it
// once the freshness window passes.
func TestTenantAdmissionCacheServesLastGoodOnlyForTransientPurserErrors(t *testing.T) {
	type answer struct {
		resp *purserpb.GetTenantAdmissionStatusResponse
		err  error
	}
	var next answer
	var calls int
	admission := newTenantAdmissionCache(func(context.Context, string) (*purserpb.GetTenantAdmissionStatusResponse, error) {
		calls++
		return next.resp, next.err
	}, 20*time.Millisecond, time.Minute)
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	server.tenantAdmission = admission
	const tenantID = "tenant-last-good"
	validate := func(reply answer) *quartermasterpb.ValidateTenantResponse {
		t.Helper()
		time.Sleep(30 * time.Millisecond)
		next = reply
		mock.ExpectQuery(`FROM quartermaster\.tenants\s+WHERE id = \$1`).WithArgs(tenantID).
			WillReturnRows(sqlmock.NewRows([]string{"name", "is_active", "rate_limit_per_minute", "rate_limit_burst"}).
				AddRow("Tenant", true, int32(60), int32(120)))
		resp, validateErr := server.ValidateTenant(serviceCtx(), &quartermasterpb.ValidateTenantRequest{TenantId: tenantID})
		if validateErr != nil {
			t.Fatalf("ValidateTenant: %v", validateErr)
		}
		return resp
	}

	good := validate(answer{resp: &purserpb.GetTenantAdmissionStatusResponse{BillingModel: "postpaid", CollectionReady: true}})
	if good.GetBillingStatusUnavailable() || good.GetBillingModel() != "postpaid" {
		t.Fatalf("first answer = %+v, want Purser's postpaid status", good)
	}
	for _, code := range []codes.Code{codes.Unavailable, codes.DeadlineExceeded, codes.Internal} {
		outage := validate(answer{err: status.Error(code, "purser down")})
		if outage.GetBillingStatusUnavailable() || outage.GetBillingModel() != "postpaid" || !outage.GetCollectionReady() {
			t.Fatalf("%s from Purser answered %+v, want the last good postpaid status", code, outage)
		}
	}
	suspended := validate(answer{resp: &purserpb.GetTenantAdmissionStatusResponse{BillingModel: "postpaid", IsSuspended: true}})
	if !suspended.GetIsSuspended() {
		t.Fatalf("a suspension after the freshness window answered %+v, want suspended", suspended)
	}
	if outage := validate(answer{err: status.Error(codes.Unavailable, "purser down")}); !outage.GetIsSuspended() {
		t.Fatalf("an outage after a suspension answered %+v, want the suspension as last good", outage)
	}
	notFound := validate(answer{err: status.Error(codes.NotFound, "no billing account")})
	if !notFound.GetBillingStatusUnavailable() || notFound.GetIsSuspended() {
		t.Fatalf("Purser's NotFound answered %+v, want billing unavailable", notFound)
	}
	if outage := validate(answer{err: status.Error(codes.Unavailable, "purser down")}); !outage.GetBillingStatusUnavailable() {
		t.Fatalf("an outage after Purser's NotFound answered %+v; the evicted last good status must not return", outage)
	}
	if calls != 8 {
		t.Fatalf("Purser calls = %d, want one per ValidateTenant outside the freshness window (8)", calls)
	}
	if tenantAdmissionFreshFor > 10*time.Second {
		t.Fatalf("tenantAdmissionFreshFor = %s; a suspension must reach admission within 10s", tenantAdmissionFreshFor)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
