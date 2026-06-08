package grpc

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/x402"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// mapSettlementErrorToGRPC routes each x402 settlement failure code to a gRPC
// rejection. With no purser client the error builders are pure: every code must
// map to FailedPrecondition and carry the right message. A nil purserClient
// keeps the payment-requirements lookup out of the path.
func TestMapSettlementErrorToGRPC(t *testing.T) {
	s := &FoghornGRPCServer{logger: newTestFoghornLogger()}
	ctx := context.Background()

	cases := []struct {
		name    string
		code    string
		message string
		wantMsg string
	}{
		{name: "invalid payment passes message through", code: x402.ErrInvalidPayment, message: "bad payment", wantMsg: "bad payment"},
		{name: "verification failed passes message through", code: x402.ErrVerificationFailed, message: "verify failed", wantMsg: "verify failed"},
		{name: "settlement failed passes message through", code: x402.ErrSettlementFailed, message: "settle failed", wantMsg: "settle failed"},
		{name: "billing details required", code: x402.ErrBillingDetailsRequired, message: "need address", wantMsg: "need address"},
		{name: "auth only overrides message", code: x402.ErrAuthOnly, message: "ignored", wantMsg: "payment required - balance exhausted"},
		{name: "unknown code defaults to payment failed", code: "something_new", message: "weird", wantMsg: "weird"},
		{name: "empty message defaults (payment failed)", code: x402.ErrInvalidPayment, message: "", wantMsg: "payment failed"},
		{name: "empty message defaults (billing details)", code: x402.ErrBillingDetailsRequired, message: "", wantMsg: "billing details required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := s.mapSettlementErrorToGRPC(ctx, "tenant-1", "/play/x", &x402.SettlementError{Code: tc.code, Message: tc.message})
			st, ok := status.FromError(err)
			if !ok {
				t.Fatalf("error is not a gRPC status: %v", err)
			}
			if st.Code() != codes.FailedPrecondition {
				t.Fatalf("code = %v, want FailedPrecondition", st.Code())
			}
			if st.Message() != tc.wantMsg {
				t.Fatalf("message = %q, want %q", st.Message(), tc.wantMsg)
			}
		})
	}
}

func TestPaymentRequiredErrorDefaultMessage(t *testing.T) {
	s := &FoghornGRPCServer{logger: newTestFoghornLogger()}
	err := s.paymentRequiredError(context.Background(), "t", "/r", "   ")
	st, _ := status.FromError(err)
	if st.Message() != "payment required" {
		t.Fatalf("message = %q, want default 'payment required'", st.Message())
	}
}

var hex32 = regexp.MustCompile(`^[0-9a-f]{32}$`)

// generateVodHash derives a 32-char hex artifact id from tenant+filename+time.
// It must be deterministic for identical inputs and diverge on any change.
func TestGenerateVodHash(t *testing.T) {
	ts := time.Unix(1_700_000_000, 0)

	h := generateVodHash("tenant-1", "movie.mp4", ts)
	if !hex32.MatchString(h) {
		t.Fatalf("hash %q is not 32 lowercase hex chars", h)
	}
	if again := generateVodHash("tenant-1", "movie.mp4", ts); again != h {
		t.Fatalf("hash not deterministic: %q vs %q", h, again)
	}
	if other := generateVodHash("tenant-1", "other.mp4", ts); other == h {
		t.Fatal("different filename must produce a different hash")
	}
	if other := generateVodHash("tenant-2", "movie.mp4", ts); other == h {
		t.Fatal("different tenant must produce a different hash")
	}
	if other := generateVodHash("tenant-1", "movie.mp4", ts.Add(time.Nanosecond)); other == h {
		t.Fatal("different timestamp must produce a different hash")
	}
}
