package grpc

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
)

// newGuardServer builds a PurserServer backed by a sqlmock with NO expectations.
// These tests only exercise request-validation guards that must return before
// any DB access; if a guard fails to short-circuit and a query fires, the
// unexpected query surfaces as an error and the assertion below catches it.
func newGuardServer(t *testing.T) *PurserServer {
	t.Helper()
	mockDB, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = mockDB.Close() })
	return &PurserServer{db: mockDB, logger: logging.NewLogger()}
}

// TestMethodInputGuards asserts the InvalidArgument validation guards at the top
// of large gRPC methods. A bare context.Background() is a service call
// (middleware.IsServiceCall is true when both user_id and tenant_id are empty),
// so the empty-required-field guards are reached directly.
func TestMethodInputGuards(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name string
		call func(s *PurserServer) error
		want codes.Code
	}{
		// Prepaid balance mutations.
		{"TopupBalance empty tenant", func(s *PurserServer) error {
			_, err := s.TopupBalance(ctx, &purserpb.TopupBalanceRequest{AmountCents: 100})
			return err
		}, codes.InvalidArgument},
		{"TopupBalance non-positive amount", func(s *PurserServer) error {
			_, err := s.TopupBalance(ctx, &purserpb.TopupBalanceRequest{TenantId: "t1", AmountCents: 0})
			return err
		}, codes.InvalidArgument},
		{"DeductBalance empty tenant", func(s *PurserServer) error {
			_, err := s.DeductBalance(ctx, &purserpb.DeductBalanceRequest{AmountCents: 100})
			return err
		}, codes.InvalidArgument},
		{"DeductBalance non-positive amount", func(s *PurserServer) error {
			_, err := s.DeductBalance(ctx, &purserpb.DeductBalanceRequest{TenantId: "t1", AmountCents: -1})
			return err
		}, codes.InvalidArgument},
		{"AdjustBalance empty tenant", func(s *PurserServer) error {
			_, err := s.AdjustBalance(ctx, &purserpb.AdjustBalanceRequest{Description: "x"})
			return err
		}, codes.InvalidArgument},
		{"AdjustBalance empty description", func(s *PurserServer) error {
			_, err := s.AdjustBalance(ctx, &purserpb.AdjustBalanceRequest{TenantId: "t1"})
			return err
		}, codes.InvalidArgument},

		// Prepaid reads.
		{"GetPrepaidBalance empty tenant", func(s *PurserServer) error {
			_, err := s.GetPrepaidBalance(ctx, &purserpb.GetPrepaidBalanceRequest{})
			return err
		}, codes.InvalidArgument},
		{"ListBalanceTransactions empty tenant", func(s *PurserServer) error {
			_, err := s.ListBalanceTransactions(ctx, &purserpb.ListBalanceTransactionsRequest{})
			return err
		}, codes.InvalidArgument},

		// Card top-up: every required-field branch.
		{"CreateCardTopup empty tenant", func(s *PurserServer) error {
			_, err := s.CreateCardTopup(ctx, &purserpb.CreateCardTopupRequest{AmountCents: 100})
			return err
		}, codes.InvalidArgument},
		{"CreateCardTopup non-positive amount", func(s *PurserServer) error {
			_, err := s.CreateCardTopup(ctx, &purserpb.CreateCardTopupRequest{TenantId: "t1", AmountCents: 0})
			return err
		}, codes.InvalidArgument},
		{"CreateCardTopup missing urls", func(s *PurserServer) error {
			_, err := s.CreateCardTopup(ctx, &purserpb.CreateCardTopupRequest{TenantId: "t1", AmountCents: 100})
			return err
		}, codes.InvalidArgument},
		{"CreateCardTopup bad provider", func(s *PurserServer) error {
			_, err := s.CreateCardTopup(ctx, &purserpb.CreateCardTopupRequest{
				TenantId: "t1", AmountCents: 100, SuccessUrl: "https://x/ok", CancelUrl: "https://x/no", Provider: "paypal",
			})
			return err
		}, codes.InvalidArgument},

		// Crypto top-up.
		{"CreateCryptoTopup empty tenant", func(s *PurserServer) error {
			_, err := s.CreateCryptoTopup(ctx, &purserpb.CreateCryptoTopupRequest{ExpectedAmountCents: 100})
			return err
		}, codes.InvalidArgument},
		{"CreateCryptoTopup non-positive amount", func(s *PurserServer) error {
			_, err := s.CreateCryptoTopup(ctx, &purserpb.CreateCryptoTopupRequest{TenantId: "t1", ExpectedAmountCents: 0})
			return err
		}, codes.InvalidArgument},
		{"CreateCryptoTopup unsupported currency", func(s *PurserServer) error {
			_, err := s.CreateCryptoTopup(ctx, &purserpb.CreateCryptoTopupRequest{
				TenantId: "t1", ExpectedAmountCents: 100, Currency: "GBP",
			})
			return err
		}, codes.InvalidArgument},
		{"GetCryptoTopup empty id", func(s *PurserServer) error {
			_, err := s.GetCryptoTopup(ctx, &purserpb.GetCryptoTopupRequest{})
			return err
		}, codes.InvalidArgument},
		{"GetPendingTopup no selector", func(s *PurserServer) error {
			_, err := s.GetPendingTopup(ctx, &purserpb.GetPendingTopupRequest{})
			return err
		}, codes.InvalidArgument},

		// Cluster pricing / access / subscription.
		{"GetClusterPricing empty cluster", func(s *PurserServer) error {
			_, err := s.GetClusterPricing(ctx, &purserpb.GetClusterPricingRequest{})
			return err
		}, codes.InvalidArgument},
		{"SetClusterPricing empty cluster", func(s *PurserServer) error {
			_, err := s.SetClusterPricing(ctx, &purserpb.SetClusterPricingRequest{})
			return err
		}, codes.InvalidArgument},
		{"SetClusterPricing invalid model", func(s *PurserServer) error {
			_, err := s.SetClusterPricing(ctx, &purserpb.SetClusterPricingRequest{ClusterId: "c1", PricingModel: "bogus"})
			return err
		}, codes.InvalidArgument},
		{"CheckClusterAccess missing ids", func(s *PurserServer) error {
			_, err := s.CheckClusterAccess(ctx, &purserpb.CheckClusterAccessRequest{TenantId: "t1"})
			return err
		}, codes.InvalidArgument},
		{"CreateClusterSubscription missing ids", func(s *PurserServer) error {
			_, err := s.CreateClusterSubscription(ctx, &purserpb.CreateClusterSubscriptionRequest{ClusterId: "c1"})
			return err
		}, codes.InvalidArgument},

		// Payments / limits.
		{"CreatePayment missing invoice/method", func(s *PurserServer) error {
			_, err := s.CreatePayment(ctx, &purserpb.PaymentRequest{})
			return err
		}, codes.InvalidArgument},
		{"CheckUserLimit empty tenant", func(s *PurserServer) error {
			_, err := s.CheckUserLimit(ctx, &purserpb.CheckUserLimitRequest{})
			return err
		}, codes.InvalidArgument},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newGuardServer(t)
			err := c.call(s)
			if err == nil {
				t.Fatalf("expected error with code %s, got nil", c.want)
			}
			if got := status.Code(err); got != c.want {
				t.Fatalf("code = %s, want %s (err: %v)", got, c.want, err)
			}
		})
	}
}
