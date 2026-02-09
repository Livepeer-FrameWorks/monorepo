package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"testing"

	purserclient "frameworks/pkg/clients/purser"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
)

type stubX402Service struct {
	pb.UnimplementedX402ServiceServer
	verifyResponse *pb.VerifyX402PaymentResponse
	verifyErr      error
	settleResponse *pb.SettleX402PaymentResponse
	settleErr      error
}

func (s *stubX402Service) VerifyX402Payment(ctx context.Context, req *pb.VerifyX402PaymentRequest) (*pb.VerifyX402PaymentResponse, error) {
	return s.verifyResponse, s.verifyErr
}

func (s *stubX402Service) SettleX402Payment(ctx context.Context, req *pb.SettleX402PaymentRequest) (*pb.SettleX402PaymentResponse, error) {
	return s.settleResponse, s.settleErr
}

func setupPurserClient(t *testing.T, service *stubX402Service) (*purserclient.GRPCClient, func()) {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}

	server := grpc.NewServer()
	pb.RegisterX402ServiceServer(server, service)

	go func() {
		_ = server.Serve(listener)
	}()

	client, err := purserclient.NewGRPCClient(purserclient.GRPCConfig{
		GRPCAddr: listener.Addr().String(),
		Logger:   logging.Logger(logrus.New()),
	})
	if err != nil {
		server.Stop()
		_ = listener.Close()
		t.Fatalf("failed to create purser client: %v", err)
	}

	cleanup := func() {
		_ = client.Close()
		server.Stop()
		_ = listener.Close()
	}

	return client, cleanup
}

func validPaymentHeader(t *testing.T) string {
	t.Helper()

	payload := map[string]interface{}{
		"x402Version": 1,
		"scheme":      "exact",
		"network":     "base-mainnet",
		"payload": map[string]interface{}{
			"signature": "0xabc123",
			"authorization": map[string]interface{}{
				"from":        "0xFromAddress",
				"to":          "0xToAddress",
				"value":       "1000000",
				"validAfter":  "0",
				"validBefore": "9999999999",
				"nonce":       "12345",
			},
		},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("failed to marshal payload: %v", err)
	}

	return base64.StdEncoding.EncodeToString(data)
}

func TestSettleX402PaymentForPlayback_SkipsWithoutPrereqs(t *testing.T) {
	previous := purserClient
	t.Cleanup(func() {
		purserClient = previous
	})

	purserClient = nil
	paid, decision := settleX402PaymentForPlayback(context.Background(), "tenant", "viewer", "", "1.2.3.4", logging.Logger(logrus.New()))
	if paid {
		t.Fatalf("expected unpaid response")
	}
	if decision != nil {
		t.Fatalf("expected nil decision when prerequisites missing")
	}
}

func TestSettleX402PaymentForPlayback_Success(t *testing.T) {
	service := &stubX402Service{
		verifyResponse: &pb.VerifyX402PaymentResponse{
			Valid: true,
		},
		settleResponse: &pb.SettleX402PaymentResponse{
			Success: true,
		},
	}
	client, cleanup := setupPurserClient(t, service)
	t.Cleanup(cleanup)

	previous := purserClient
	t.Cleanup(func() {
		purserClient = previous
	})
	purserClient = client

	paid, decision := settleX402PaymentForPlayback(context.Background(), "tenant-1", "viewer-1", validPaymentHeader(t), "1.2.3.4", logging.Logger(logrus.New()))
	if !paid {
		t.Fatalf("expected payment to be settled")
	}
	if decision != nil {
		t.Fatalf("expected nil decision on success")
	}
}

func TestSettleX402PaymentForPlayback_InvalidHeader(t *testing.T) {
	service := &stubX402Service{
		verifyResponse: &pb.VerifyX402PaymentResponse{Valid: true},
		settleResponse: &pb.SettleX402PaymentResponse{Success: true},
	}
	client, cleanup := setupPurserClient(t, service)
	t.Cleanup(cleanup)

	previous := purserClient
	t.Cleanup(func() {
		purserClient = previous
	})
	purserClient = client

	paid, decision := settleX402PaymentForPlayback(context.Background(), "tenant-1", "viewer-1", "not-base64", "1.2.3.4", logging.Logger(logrus.New()))
	if paid {
		t.Fatalf("expected unpaid response")
	}
	if decision == nil {
		t.Fatalf("expected decision for invalid payment header")
	}
	if decision.Status != 402 {
		t.Fatalf("expected 402 status, got %d", decision.Status)
	}
	if decision.Body["error"] != "payment_failed" {
		t.Fatalf("expected payment_failed error, got %v", decision.Body["error"])
	}
}

func TestSettleX402PaymentForPlayback_SettleFailure(t *testing.T) {
	service := &stubX402Service{
		verifyResponse: &pb.VerifyX402PaymentResponse{
			Valid: true,
		},
		settleResponse: &pb.SettleX402PaymentResponse{
			Success: false,
			Error:   "settlement failed",
		},
	}
	client, cleanup := setupPurserClient(t, service)
	t.Cleanup(cleanup)

	previous := purserClient
	t.Cleanup(func() {
		purserClient = previous
	})
	purserClient = client

	paid, decision := settleX402PaymentForPlayback(context.Background(), "tenant-1", "viewer-1", validPaymentHeader(t), "1.2.3.4", logging.Logger(logrus.New()))
	if paid {
		t.Fatalf("expected unpaid response")
	}
	if decision == nil {
		t.Fatalf("expected decision for settlement failure")
	}
	if decision.Status != 402 {
		t.Fatalf("expected 402 status, got %d", decision.Status)
	}
	if decision.Body["error"] != "payment_failed" {
		t.Fatalf("expected payment_failed error, got %v", decision.Body["error"])
	}
}

func TestSettleX402PaymentForPlayback_BillingDetailsRequired(t *testing.T) {
	service := &stubX402Service{
		verifyResponse: &pb.VerifyX402PaymentResponse{
			Valid:                  true,
			RequiresBillingDetails: true,
		},
	}
	client, cleanup := setupPurserClient(t, service)
	t.Cleanup(cleanup)

	previous := purserClient
	t.Cleanup(func() {
		purserClient = previous
	})
	purserClient = client

	paid, decision := settleX402PaymentForPlayback(context.Background(), "tenant-1", "viewer-1", validPaymentHeader(t), "1.2.3.4", logging.Logger(logrus.New()))
	if paid {
		t.Fatalf("expected unpaid response")
	}
	if decision == nil {
		t.Fatalf("expected decision for billing details required")
	}
	if decision.Status != 402 {
		t.Fatalf("expected 402 status, got %d", decision.Status)
	}
	if decision.Body["error"] != "billing_details_required" {
		t.Fatalf("expected billing_details_required error, got %v", decision.Body["error"])
	}
}

func TestSettleX402PaymentForPlayback_AuthOnlyPayment(t *testing.T) {
	service := &stubX402Service{
		verifyResponse: &pb.VerifyX402PaymentResponse{
			Valid:      true,
			IsAuthOnly: true,
		},
	}
	client, cleanup := setupPurserClient(t, service)
	t.Cleanup(cleanup)

	previous := purserClient
	t.Cleanup(func() {
		purserClient = previous
	})
	purserClient = client

	paid, decision := settleX402PaymentForPlayback(context.Background(), "tenant-1", "viewer-1", validPaymentHeader(t), "1.2.3.4", logging.Logger(logrus.New()))
	if paid {
		t.Fatalf("expected unpaid response")
	}
	if decision == nil {
		t.Fatalf("expected decision for auth-only payment")
	}
	if decision.Status != 402 {
		t.Fatalf("expected 402 status, got %d", decision.Status)
	}
	if decision.Body["error"] != "insufficient_balance" {
		t.Fatalf("expected insufficient_balance error, got %v", decision.Body["error"])
	}
}
