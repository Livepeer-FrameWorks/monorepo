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
	retired           *commodorepb.AdminRetireIngestCellRequest
	unretired         *commodorepb.AdminUnretireIngestCellRequest
	stateFailure      bool
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
	state := retryingAccountState(req.GetTenantId())
	state.PendingIngestStopsTruncated = true
	return &commodorepb.AdminListAccountHistoryResponse{Entries: []*commodorepb.OperatorAccountAuditEntry{{Id: "audit-1", Action: "user_active:false", Reason: "line one\nline two", ActorUserId: "operator", TargetId: "departing-user", CreatedAt: timestamppb.New(time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))}}, HasMore: true, AccountState: state}, nil
}

func retryingAccountState(tenantID string) *commodorepb.TenantAccountState {
	at := timestamppb.New(time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC))
	return &commodorepb.TenantAccountState{
		TenantId: tenantID, ClosureSync: "retrying", ClosedAt: at, ClosedBy: "operator", ClosureAttemptedAt: at, ClosureRetryAfter: at,
		ClosureError: "quartermaster unavailable", ClosureFailures: 3,
		PendingIngestStops: []*commodorepb.PendingIngestStop{{TargetKind: "tenant", TargetId: tenantID, Status: "pending", Attempts: 3, PendingSince: at, NextAttemptAt: at, LastError: "stop ingest on cell removed-cell: unavailable", Cells: []string{"live-cell", "removed-cell"}, RetiredCells: []string{"removed-cell"}}},
	}
}

func (s *trialCommandServer) AdminGetTenantAccountState(ctx context.Context, req *commodorepb.AdminGetTenantAccountStateRequest) (*commodorepb.TenantAccountState, error) {
	s.readJWT(ctx)
	if s.failure || s.stateFailure {
		return nil, status.Error(codes.Unavailable, "account state unavailable")
	}
	return retryingAccountState(req.GetTenantId()), nil
}

func (s *trialCommandServer) AdminUnretireIngestCell(ctx context.Context, req *commodorepb.AdminUnretireIngestCellRequest) (*commodorepb.AdminUnretireIngestCellResponse, error) {
	s.readJWT(ctx)
	s.unretired = req
	if req.GetClusterId() != "removed-cell" {
		return &commodorepb.AdminUnretireIngestCellResponse{ClusterId: req.GetClusterId()}, nil
	}
	return &commodorepb.AdminUnretireIngestCellResponse{ClusterId: req.GetClusterId(), WasRetired: true, RetiredAt: timestamppb.New(time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)), RetiredBy: "operator", RetirementReason: "decommissioned"}, nil
}

func (s *trialCommandServer) AdminRetireIngestCell(ctx context.Context, req *commodorepb.AdminRetireIngestCellRequest) (*commodorepb.AdminRetireIngestCellResponse, error) {
	s.readJWT(ctx)
	s.retired = req
	return &commodorepb.AdminRetireIngestCellResponse{ClusterId: req.GetClusterId(), RetiredAt: timestamppb.New(time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)), RetiredBy: "operator", Reason: req.GetReason(), PendingIngestStops: 2}, nil
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
	return &purserpb.AdminListCheckoutRecoveryResponse{Items: []*purserpb.CheckoutRecoveryItem{{Id: "work", ReservationId: "reservation", LastError: "provider offline", NextAttemptAt: timestamppb.Now()}, {Id: "historical-payment", TenantId: "00000000-0000-4000-8000-000000000001", ReservationId: "historical-payment-reservation", Provider: "mollie", Status: "delivery_review", ReservationStatus: "succeeded", AmountKnown: true, AmountCents: 2000, Currency: "EUR", LastError: "verify delivered service before confirming admin billing recovery checkout"}}, HasMore: true}, nil
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
	response := &purserpb.AdminRecoverTierUpgradeResponse{UpgradeId: r.GetUpgradeId(), State: state, Provider: "stripe", RequestedBy: "operator", Detail: "No new purchase started"}
	if r.GetConfirmed() && r.GetWaiveMetering() {
		response.MeteringWaivedBy = "operator"
	}
	return response, nil
}

func (s *trialCommandServer) AdminResendFreeFallbackNotice(ctx context.Context, r *purserpb.AdminResendFreeFallbackNoticeRequest) (*purserpb.AdminResendFreeFallbackNoticeResponse, error) {
	s.readJWT(ctx)
	if s.retainUnsupported {
		return nil, status.Error(codes.Unimplemented, "notification retry unavailable")
	}
	deadline := timestamppb.New(time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC))
	if r.GetConfirmed() {
		if r.GetReason() != "email repaired" || r.GetScheduledAt().GetSeconds() != deadline.GetSeconds() || !r.GetMoved() {
			return nil, status.Error(codes.InvalidArgument, "notice confirmation fence lost")
		}
		s.recoveryCalls++
		return &purserpb.AdminResendFreeFallbackNoticeResponse{Status: "queued", Recipient: "repaired@example.test", ScheduledAt: deadline, Moved: true, Detail: "Email retry queued"}, nil
	}
	return &purserpb.AdminResendFreeFallbackNoticeResponse{Status: "notice_abandoned", Recipient: "repaired@example.test", ScheduledAt: deadline, Moved: true, Detail: "delivery rejected"}, nil
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
		{"lookup closure text", []string{"admin", "tenants", "get", id}, `Quartermaster synchronization failing (3 attempts`, false},
		{"lookup stop text", []string{"admin", "tenants", "get", id}, "cells=live-cell,removed-cell  retired=removed-cell", false},
		{"lookup closure json", []string{"--output", "json", "admin", "tenants", "get", id}, `"closure_sync": "retrying"`, false},
		{"history text", []string{"admin", "tenants", "history", id, "--limit", "7", "--before", "test-cursor"}, `line one\nline two`, false},
		{"history closure text", []string{"admin", "tenants", "history", id}, "Pending session stops: 1+ (showing the oldest 1)", false},
		{"lookup degraded text", []string{"admin", "tenants", "get", id}, "closure state unavailable from Commodore", true},
		{"lookup degraded json", []string{"--output", "json", "admin", "tenants", "get", id}, "closure state unavailable from Commodore", true},
		{"history closure json", []string{"--output", "json", "admin", "tenants", "history", id}, `"closure_error": "quartermaster unavailable"`, false},
		{"retire missing reason", []string{"admin", "clusters", "retire", "removed-cell", "--yes"}, "--reason is required", true},
		{"retire mismatched confirmation", []string{"admin", "clusters", "retire", "removed-cell", "--reason", "decommissioned"}, "nothing was retired", true},
		{"retire interactive", []string{"admin", "clusters", "retire", "removed-cell", "--reason", "decommissioned"}, "Cell removed-cell retired", false},
		{"retire", []string{"admin", "clusters", "retire", "removed-cell", "--reason", "decommissioned", "--yes"}, "2 include this cell and retry now", false},
		{"retire json", []string{"--output", "json", "admin", "clusters", "retire", "removed-cell", "--reason", "decommissioned", "--yes"}, `"pending_ingest_stops": 2`, false},
		{"unretire missing reason", []string{"admin", "clusters", "unretire", "removed-cell"}, "--reason is required", true},
		{"unretire", []string{"admin", "clusters", "unretire", "removed-cell", "--reason", "retired by mistake"}, "Retirement of cell removed-cell removed", false},
		{"unretire not retired", []string{"admin", "clusters", "unretire", "other-cell", "--reason", "retired by mistake"}, "Cell other-cell was not retired; nothing changed", false},
		{"unretire json", []string{"--output", "json", "admin", "clusters", "unretire", "removed-cell", "--reason", "retired by mistake"}, `"retirement_reason": "decommissioned"`, false},
		{"history json", []string{"--output", "json", "admin", "tenants", "history", id}, `"actor_user_id": "operator"`, false},
		{"recovery text", []string{"admin", "billing", "recovery", "list", "--limit", "7"}, "provider offline", false},
		{"recovery json", []string{"--output", "json", "admin", "billing", "recovery", "list"}, `"reservation_id": "reservation"`, false},
		{"historical payment text", []string{"admin", "billing", "recovery", "list", "--tenant-id", id}, "historical-payment-reservation  succeeded/delivery_review  20.00 EUR  -", false},
		{"historical payment json", []string{"--output", "json", "admin", "billing", "recovery", "list"}, `"status": "delivery_review"`, false},
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
		{"recover upgrade waiving metering", []string{"admin", "billing", "recovery", "upgrade", "--tenant-id", id, "--upgrade-id", id, "--reason", "reconcile", "--waive-metering", "--yes"}, "Metering waived by operator", false},
		{"recover upgrade interactive yes", []string{"admin", "billing", "recovery", "upgrade", "--tenant-id", id, "--upgrade-id", id, "--reason", "reconcile"}, "Tier upgrade recovery: cancelled", false},
		{"notice unconfirmed", []string{"admin", "billing", "recovery", "notice", "--tenant-id", id, "--reason", "email repaired"}, "retry declined", true},
		{"notice older server", []string{"admin", "billing", "recovery", "notice", "--tenant-id", id, "--reason", "email repaired", "--yes"}, "notification retry unavailable", true},
		{"notice retry", []string{"admin", "billing", "recovery", "notice", "--tenant-id", id, "--reason", "email repaired", "--yes"}, "Fallback notification: queued", false},
		{"notice retry json", []string{"--output", "json", "admin", "billing", "recovery", "notice", "--tenant-id", id, "--reason", "email repaired", "--yes"}, `"status": "queued"`, false},
		{"notice retry interactive yes", []string{"admin", "billing", "recovery", "notice", "--tenant-id", id, "--reason", "email repaired"}, "Fallback notification: queued", false},
		{"recovery error", []string{"admin", "billing", "recovery", "list"}, "recovery unavailable", true},
		{"empty grants", []string{"admin", "billing", "grant", "list"}, "No operator grants found.", false},
		{"lookup error", []string{"admin", "tenants", "get", id}, "tenant absent", true},
		{"history error", []string{"admin", "tenants", "history", id}, "history unavailable", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			degraded := strings.HasPrefix(tc.name, "lookup degraded")
			service.failure = tc.fail && !degraded
			service.stateFailure = degraded
			service.retainUnsupported = (tc.name == "retain delivered older server" || tc.name == "notice older server")
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
			if tc.name == "recover checkout interactive yes" || tc.name == "recover upgrade interactive yes" || tc.name == "notice retry interactive yes" {
				command.SetIn(strings.NewReader("yes\n"))
			}
			if tc.name == "retire interactive" {
				command.SetIn(strings.NewReader("removed-cell\n"))
			}
			service.retired, service.unretired = nil, nil
			command.SetArgs(tc.args)
			err := command.Execute()
			if (tc.name == "recover checkout refuses unconfirmed" || tc.name == "recover upgrade unconfirmed" || tc.name == "retain delivered unconfirmed" || tc.name == "notice unconfirmed" || (tc.name == "retain delivered older server" || tc.name == "notice older server")) && service.recoveryCalls != callsBefore {
				t.Fatal("unconfirmed request moved money")
			}
			if strings.HasPrefix(tc.name, "retire ") && tc.fail && service.retired != nil {
				t.Fatalf("a refused retirement reached Commodore: %v", service.retired)
			}
			if strings.HasPrefix(tc.name, "unretire ") && tc.fail && service.unretired != nil {
				t.Fatalf("a refused unretirement reached Commodore: %v", service.unretired)
			}
			if tc.fail {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("error=%v want %s", err, tc.want)
				}
				// A degraded read still prints the Quartermaster tenant.
				if degraded && !strings.Contains(out.String(), "Trial tenant") {
					t.Fatalf("degraded lookup dropped the tenant: %s", out.String())
				}
				if tc.name == "lookup degraded json" && !strings.Contains(out.String(), `"account_state_error"`) {
					t.Fatalf("degraded JSON lacks account_state_error: %s", out.String())
				}
				return
			}
			if strings.HasPrefix(tc.name, "unretire") && (service.unretired.GetReason() != "retired by mistake") {
				t.Fatalf("unretirement request=%v", service.unretired)
			}
			if strings.HasPrefix(tc.name, "retire") && (service.retired.GetClusterId() != "removed-cell" || service.retired.GetReason() != "decommissioned") {
				t.Fatalf("retirement request=%v", service.retired)
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
	for history, empty := range map[bool]string{false: "No provider recovery work found.", true: "No checkout recovery history found."} {
		fake := &recoveryValidationClient{}
		var out bytes.Buffer
		if err := runCheckoutRecoveryList(t.Context(), &out, fake, "jwt", &purserpb.AdminListCheckoutRecoveryRequest{TenantId: id, Limit: 10, History: history}, false); err != nil || !strings.Contains(out.String(), empty) {
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
