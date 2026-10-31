package cmd

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	fwcfg "frameworks/cli/internal/config"

	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type trialCommandServer struct {
	commodorepb.UnimplementedUserServiceServer
	quartermasterpb.UnimplementedTenantServiceServer
	purserpb.UnimplementedPrepaidServiceServer
	history           *commodorepb.AdminListAccountHistoryRequest
	tenant            string
	failure           bool
	jwt               string
	recoveryCalls     int
	recoveryOutcome   string
	retainDelivered   bool
	retainUnsupported bool
}

func (s *trialCommandServer) GetTenant(ctx context.Context, req *quartermasterpb.GetTenantRequest) (*quartermasterpb.GetTenantResponse, error) {
	s.tenant = req.GetTenantId()
	s.readJWT(ctx)
	if s.failure {
		return nil, status.Error(codes.NotFound, "tenant absent")
	}
	return &quartermasterpb.GetTenantResponse{Tenant: &quartermasterpb.Tenant{Id: s.tenant, Name: "Trial tenant"}}, nil
}

func (s *trialCommandServer) AdminListAccountHistory(ctx context.Context, req *commodorepb.AdminListAccountHistoryRequest) (*commodorepb.AdminListAccountHistoryResponse, error) {
	s.history = req
	s.readJWT(ctx)
	if s.failure {
		return nil, status.Error(codes.Unavailable, "history unavailable")
	}
	return &commodorepb.AdminListAccountHistoryResponse{Entries: []*commodorepb.OperatorAccountAuditEntry{{Id: "audit-1", Action: "user_active:false", Reason: "line one\nline two", ActorUserId: "operator", TargetId: "departing-user", CreatedAt: timestamppb.New(time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))}}, HasMore: true}, nil
}

func (s *trialCommandServer) AdminListBillingGrants(ctx context.Context, req *purserpb.AdminListBillingGrantsRequest) (*purserpb.AdminListBillingGrantsResponse, error) {
	s.readJWT(ctx)
	return &purserpb.AdminListBillingGrantsResponse{}, nil
}

func (s *trialCommandServer) AdminListCheckoutRecovery(ctx context.Context, r *purserpb.AdminListCheckoutRecoveryRequest) (*purserpb.AdminListCheckoutRecoveryResponse, error) {
	s.readJWT(ctx)
	if s.failure {
		return nil, status.Error(codes.Unavailable, "recovery unavailable")
	}
	return &purserpb.AdminListCheckoutRecoveryResponse{Items: []*purserpb.CheckoutRecoveryItem{{Id: "work", ReservationId: "reservation", LastError: "provider offline", NextAttemptAt: timestamppb.Now()}}, HasMore: true}, nil
}

func (s *trialCommandServer) AdminRecoverCheckout(ctx context.Context, r *purserpb.AdminRecoverCheckoutRequest) (*purserpb.AdminRecoverCheckoutResponse, error) {
	s.readJWT(ctx)
	s.retainDelivered = false
	if r.GetReason() != "abandoned" {
		return nil, status.Error(codes.InvalidArgument, "reason lost")
	}
	if !r.GetConfirmed() {
		return &purserpb.AdminRecoverCheckoutResponse{Provider: "stripe", Status: "provider_open", AmountCents: 1000, Currency: "EUR", Outcome: "preview"}, nil
	}
	s.recoveryCalls++
	outcome := s.recoveryOutcome
	if outcome == "" {
		outcome = "expired"
	}

	return &purserpb.AdminRecoverCheckoutResponse{Provider: "stripe", Status: "expired", Outcome: outcome, AuditId: "audit", Detail: "provider result"}, nil
}

func (s *trialCommandServer) AdminRetainDeliveredCheckout(ctx context.Context, r *purserpb.AdminRecoverCheckoutRequest) (*purserpb.AdminRecoverCheckoutResponse, error) {
	s.readJWT(ctx)
	s.retainDelivered = true
	if s.retainUnsupported {
		return nil, status.Error(codes.Unimplemented, "delivery attestation unavailable")
	}
	if r.GetReason() != "abandoned" {
		return nil, status.Error(codes.InvalidArgument, "reason lost")
	}
	if !r.GetConfirmed() {
		return &purserpb.AdminRecoverCheckoutResponse{Provider: "mollie", Status: "succeeded", Outcome: "preview"}, nil
	}
	s.recoveryCalls++
	return &purserpb.AdminRecoverCheckoutResponse{Provider: "mollie", Status: "succeeded", Outcome: "delivered", AuditId: "audit"}, nil
}

func (s *trialCommandServer) AdminRecoverTierUpgrade(ctx context.Context, r *purserpb.AdminRecoverTierUpgradeRequest) (*purserpb.AdminRecoverTierUpgradeResponse, error) {
	s.readJWT(ctx)
	if r.GetReason() != "reconcile" {
		return nil, status.Error(codes.InvalidArgument, "reason lost")
	}
	state := "requested"
	if r.GetConfirmed() {
		s.recoveryCalls++
		state = "cancelled"
	}
	return &purserpb.AdminRecoverTierUpgradeResponse{UpgradeId: r.GetUpgradeId(), State: state, Provider: "stripe", RequestedBy: "operator", Detail: "No new purchase started"}, nil
}

func (s *trialCommandServer) readJWT(ctx context.Context) {
	md, _ := metadata.FromIncomingContext(ctx)
	s.jwt = strings.Join(md.Get("authorization"), ",")
}

func TestTrialAccountCommandsUseRealRPCs(t *testing.T) {
	_, _, restore := withTempHomeAndCwd(t)
	defer restore()
	t.Setenv("FW_USER_TOKEN", "operator-jwt")
	oldOutput := output
	t.Cleanup(func() { output = oldOutput })
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	service := &trialCommandServer{}
	server := grpc.NewServer()
	commodorepb.RegisterUserServiceServer(server, service)
	quartermasterpb.RegisterTenantServiceServer(server, service)
	purserpb.RegisterPrepaidServiceServer(server, service)
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		if err := <-serveErrors; err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			t.Error(err)
		}
	})
	dir := t.TempDir()
	manifest := filepath.Join(dir, "cluster.yaml")
	if err := os.WriteFile(manifest, []byte("version: \"1\"\ntype: cluster\nenv_files: [secrets.env]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secrets.env"), []byte("SERVICE_TOKEN=test-service\n"), 0600); err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	cfg := fwcfg.Config{Current: "test", Contexts: map[string]fwcfg.Context{"test": {Name: "test", Executor: fwcfg.Executor{Type: "local"}, AccessMode: fwcfg.AccessModeLocal, Endpoints: fwcfg.Endpoints{BridgeURL: "http://127.0.0.1:18000", AllowInsecure: true, QuartermasterGRPCAddr: addr, CommodoreGRPCAddr: addr, PurserGRPCAddr: addr}, Gitops: &fwcfg.Gitops{Source: fwcfg.GitopsManifest, ManifestPath: manifest}}}}
	if err := fwcfg.Save(cfg); err != nil {
		t.Fatal(err)
	}
	const id = "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"
	for _, tc := range []struct {
		name string
		args []string
		want string
		fail bool
	}{
		{"lookup text", []string{"admin", "tenants", "get", id}, "Trial tenant (aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa)", false},
		{"lookup json", []string{"--output", "json", "admin", "tenants", "get", id}, `"name": "Trial tenant"`, false},
		{"history text", []string{"admin", "tenants", "history", id, "--limit", "7", "--before", "test-cursor"}, `line one\nline two`, false},
		{"history json", []string{"--output", "json", "admin", "tenants", "history", id}, `"actor_user_id": "operator"`, false},
		{"recovery text", []string{"admin", "billing", "recovery", "list", "--limit", "7"}, "provider offline", false},
		{"recovery json", []string{"--output", "json", "admin", "billing", "recovery", "list"}, `"reservation_id": "reservation"`, false},
		{"recover missing reason", []string{"admin", "billing", "recovery", "checkout", "--tenant-id", id, "--reservation-id", id, "--yes"}, "--reason is required", true},
		{"recover invalid UUID", []string{"admin", "billing", "recovery", "checkout", "--tenant-id", "bad", "--reservation-id", id, "--reason", "abandoned", "--yes"}, "invalid UUID", true},
		{"recover checkout refuses unconfirmed", []string{"admin", "billing", "recovery", "checkout", "--tenant-id", id, "--reservation-id", id, "--reason", "abandoned"}, "confirmation", true},
		{"recover checkout interactive yes", []string{"admin", "billing", "recovery", "checkout", "--tenant-id", id, "--reservation-id", id, "--reason", "abandoned"}, "Checkout recovery: expired", false},
		{"recover checkout", []string{"admin", "billing", "recovery", "checkout", "--yes", "--tenant-id", id, "--reservation-id", id, "--reason", "abandoned"}, "Checkout recovery: expired", false},
		{"recover checkout pending", []string{"admin", "billing", "recovery", "checkout", "--yes", "--tenant-id", id, "--reservation-id", id, "--reason", "abandoned"}, "Checkout recovery: pending", false},
		{"retain delivered unconfirmed", []string{"admin", "billing", "recovery", "checkout", "--retain-delivered", "--tenant-id", id, "--reservation-id", id, "--reason", "abandoned"}, "confirmation", true},
		{"retain delivered older server", []string{"admin", "billing", "recovery", "checkout", "--retain-delivered", "--yes", "--tenant-id", id, "--reservation-id", id, "--reason", "abandoned"}, "delivery attestation unavailable", true},
		{"retain delivered confirmed", []string{"admin", "billing", "recovery", "checkout", "--retain-delivered", "--yes", "--tenant-id", id, "--reservation-id", id, "--reason", "abandoned"}, "Checkout recovery: delivered", false},

		{"recover upgrade unconfirmed", []string{"admin", "billing", "recovery", "upgrade", "--tenant-id", id, "--upgrade-id", id, "--reason", "reconcile"}, "confirmation", true},
		{"recover upgrade", []string{"admin", "billing", "recovery", "upgrade", "--tenant-id", id, "--upgrade-id", id, "--reason", "reconcile", "--yes"}, "Tier upgrade recovery: cancelled", false},
		{"recover upgrade json", []string{"--output", "json", "admin", "billing", "recovery", "upgrade", "--tenant-id", id, "--upgrade-id", id, "--reason", "reconcile", "--yes"}, `"state": "cancelled"`, false},
		{"recover upgrade interactive yes", []string{"admin", "billing", "recovery", "upgrade", "--tenant-id", id, "--upgrade-id", id, "--reason", "reconcile"}, "Tier upgrade recovery: cancelled", false},
		{"recovery error", []string{"admin", "billing", "recovery", "list"}, "recovery unavailable", true},
		{"empty grants", []string{"admin", "billing", "grant", "list"}, "No operator grants found.", false},
		{"lookup error", []string{"admin", "tenants", "get", id}, "tenant absent", true},
		{"history error", []string{"admin", "tenants", "history", id}, "history unavailable", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service.failure = tc.fail
			service.retainUnsupported = tc.name == "retain delivered older server"
			service.recoveryOutcome = ""
			if tc.name == "recover checkout pending" {
				service.recoveryOutcome = "pending"
			}
			callsBefore := service.recoveryCalls
			command := NewRootCmd()
			var out bytes.Buffer
			command.SetOut(&out)
			command.SetErr(&out)
			command.SetIn(strings.NewReader("no\n"))
			if tc.name == "recover checkout interactive yes" || tc.name == "recover upgrade interactive yes" {
				command.SetIn(strings.NewReader("yes\n"))
			}
			command.SetArgs(tc.args)
			err := command.Execute()
			if (tc.name == "recover checkout refuses unconfirmed" || tc.name == "recover upgrade unconfirmed" || tc.name == "retain delivered unconfirmed" || tc.name == "retain delivered older server") && service.recoveryCalls != callsBefore {
				t.Fatal("unconfirmed request moved money")
			}
			if tc.fail {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("error=%v want %s", err, tc.want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), tc.want) {
				t.Fatalf("output=%s, want %s", out.String(), tc.want)
			}
			if tc.name == "retain delivered confirmed" && (!service.retainDelivered || !strings.Contains(out.String(), "retained without refund")) {
				t.Fatalf("delivery decision lost its flag or confirmation warning: %s", out.String())
			}
			if service.jwt != "Bearer operator-jwt" {
				t.Fatalf("operator identity lost: %q", service.jwt)
			}
			if tc.name == "history text" && (service.history.GetTenantId() != "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" || service.history.GetLimit() != 7 || service.history.GetBefore() != "test-cursor") {
				t.Fatalf("history request=%v", service.history)
			}
		})
	}
}

type recoveryValidationClient struct {
	calls    int
	response *purserpb.AdminListCheckoutRecoveryResponse
}

func (c *recoveryValidationClient) AdminListCheckoutRecovery(context.Context, *purserpb.AdminListCheckoutRecoveryRequest) (*purserpb.AdminListCheckoutRecoveryResponse, error) {
	c.calls++
	if c.response != nil {
		return c.response, nil
	}
	return &purserpb.AdminListCheckoutRecoveryResponse{}, nil
}

func (c *recoveryValidationClient) AdminRecoverCheckout(context.Context, *purserpb.AdminRecoverCheckoutRequest) (*purserpb.AdminRecoverCheckoutResponse, error) {
	c.calls++
	return &purserpb.AdminRecoverCheckoutResponse{Outcome: "pending", Detail: "bank settlement"}, nil
}

func TestRecoveryCommandsValidateBeforeRPCAndShowEmptyInventory(t *testing.T) {
	const id = "00000000-0000-4000-8000-000000000001"
	for _, r := range []*purserpb.AdminRecoverCheckoutRequest{{TenantId: "bad", ReservationId: id, Reason: "reason", Confirmed: true}, {TenantId: id, ReservationId: "bad", Reason: "reason", Confirmed: true}, {TenantId: id, ReservationId: id, Confirmed: true}, {TenantId: id, ReservationId: id, Reason: "reason"}} {
		fake := &recoveryValidationClient{}
		var out bytes.Buffer
		if err := runCheckoutRecoveryNow(t.Context(), &out, fake, "jwt", r, false, false); err == nil || fake.calls != 0 {
			t.Fatalf("invalid recovery called RPC: request=%v calls=%d err=%v", r, fake.calls, err)
		}
	}
	for _, r := range []*purserpb.AdminListCheckoutRecoveryRequest{{TenantId: "bad", Limit: 1}, {Limit: 101}, {Limit: -1}, {Limit: 1, Offset: -1}, {Limit: 1, History: true}} {
		fake := &recoveryValidationClient{}
		var out bytes.Buffer
		if err := runCheckoutRecoveryList(t.Context(), &out, fake, "jwt", r, false); err == nil || fake.calls != 0 {
			t.Fatalf("invalid list called RPC: request=%v calls=%d err=%v", r, fake.calls, err)
		}
	}
	for _, history := range []bool{false, true} {
		fake := &recoveryValidationClient{}
		var out bytes.Buffer
		if err := runCheckoutRecoveryList(t.Context(), &out, fake, "jwt", &purserpb.AdminListCheckoutRecoveryRequest{TenantId: id, Limit: 10, History: history}, false); err != nil || !strings.Contains(out.String(), "No checkout recovery") {
			t.Fatalf("empty inventory=%q err=%v", out.String(), err)
		}
	}
}

func TestCheckoutRecoveryTextShowsMoneyAndHistoryColumns(t *testing.T) {
	client := &recoveryValidationClient{response: &purserpb.AdminListCheckoutRecoveryResponse{Items: []*purserpb.CheckoutRecoveryItem{{Provider: "mollie", AmountKnown: true, AmountCents: 2900, Currency: "EUR"}, {Provider: "mollie", AmountKnown: false}, {Provider: "free_fallback", Status: "scheduled"}}}}
	var out bytes.Buffer
	if err := runCheckoutRecoveryList(t.Context(), &out, client, "jwt", &purserpb.AdminListCheckoutRecoveryRequest{Limit: 10}, false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"29.00 EUR", "unknown", "NEXT ATTEMPT", "free_fallback", "admin billing set-tier", "provider_stopping"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in %s", want, out.String())
		}
	}
	client.response = &purserpb.AdminListCheckoutRecoveryResponse{Entries: []*purserpb.CheckoutRecoveryAudit{{Id: "request", Detail: "refund accepted", CreatedAt: timestamppb.New(time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC))}}}
	out.Reset()
	if err := runCheckoutRecoveryList(t.Context(), &out, client, "jwt", &purserpb.AdminListCheckoutRecoveryRequest{TenantId: "00000000-0000-4000-8000-000000000001", Limit: 10, History: true}, false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"DETAIL  CREATED", "refund accepted", "2026-10-29T10:00:00Z"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in %s", want, out.String())
		}
	}
}

func (c *recoveryValidationClient) AdminRetainDeliveredCheckout(context.Context, *purserpb.AdminRecoverCheckoutRequest) (*purserpb.AdminRecoverCheckoutResponse, error) {
	c.calls++
	return &purserpb.AdminRecoverCheckoutResponse{Outcome: "delivered"}, nil
}
