package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
)

const billingOperatorTenant = "3f2a1b4c-5d6e-4f70-8a9b-0c1d2e3f4a5b"

type adjustBalanceCall struct {
	tenantID    string
	amountCents int64
	description string
}

type assignTierCall struct {
	req *purserpb.AdminAssignTierRequest
}

type fakeUserLookup struct {
	emails []string
	resp   *commodorepb.AdminLookupUserByEmailResponse
	err    error
}

func (f *fakeUserLookup) AdminLookupUserByEmail(_ context.Context, email string) (*commodorepb.AdminLookupUserByEmailResponse, error) {
	f.emails = append(f.emails, email)
	return f.resp, f.err
}

func TestParseBillingAmountCents(t *testing.T) {
	for _, tc := range []struct {
		amount  string
		debit   bool
		want    int64
		wantErr string
	}{
		{amount: "25.00", want: 2500},
		{amount: "25", want: 2500},
		{amount: "0.5", want: 50},
		{amount: "0.05", want: 5},
		{amount: " 12.34 ", want: 1234},
		{amount: "-5.00", debit: true, want: -500},
		{amount: "-5.00", wantErr: "--debit"},
		{amount: "5.00", debit: true, wantErr: "negative"},
		{amount: "0", wantErr: "non-zero"},
		{amount: "-0.00", debit: true, wantErr: "non-zero"},
		{amount: "1.234", wantErr: "at most two decimals"},
		{amount: "25,00", wantErr: "EUR amount"},
		{amount: "1e3", wantErr: "EUR amount"},
		{amount: "€5", wantErr: "EUR amount"},
		{amount: "", wantErr: "--amount is required"},
		{amount: "99999999999999999", wantErr: "too large"},
	} {
		t.Run(tc.amount, func(t *testing.T) {
			got, err := parseBillingAmountCents(tc.amount, tc.debit)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("cents = %d, err %v; want %d", got, err, tc.want)
			}
		})
	}
}

func TestBillingTenantTargetValidate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		target  billingTenantTarget
		wantErr string
	}{
		{"neither", billingTenantTarget{}, "one of --tenant-id or --email"},
		{"both", billingTenantTarget{TenantID: billingOperatorTenant, Email: "a@example.com"}, "not both"},
		{"bad uuid", billingTenantTarget{TenantID: "acme"}, "--tenant-id"},
		{"bad email", billingTenantTarget{Email: "acme"}, "--email"},
		{"tenant", billingTenantTarget{TenantID: billingOperatorTenant}, ""},
		{"email", billingTenantTarget{Email: "owner@example.com"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.target.validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestRunBillingCreditResolvesEmailAndRecordsReason(t *testing.T) {
	lookup := &fakeUserLookup{resp: &commodorepb.AdminLookupUserByEmailResponse{
		UserId: "user-1", TenantId: billingOperatorTenant, Email: "owner@example.com",
	}}
	fake := &fakeAdminBillingClient{adjustResp: &purserpb.BalanceTransaction{
		Id: "txn-1", TenantId: billingOperatorTenant, AmountCents: 2500, BalanceAfterCents: 3750,
	}}
	var buf bytes.Buffer
	err := runBillingCredit(context.Background(), &buf, fake, lookup, "operator-jwt", billingCreditRequest{
		Target: billingTenantTarget{Email: "owner@example.com"}, Amount: "25.00", Reason: "staging storage test",
	}, false)
	if err != nil {
		t.Fatalf("runBillingCredit: %v", err)
	}
	if len(lookup.emails) != 1 || lookup.emails[0] != "owner@example.com" {
		t.Fatalf("email lookups = %v", lookup.emails)
	}
	if len(fake.adjustCalls) != 1 {
		t.Fatalf("AdjustBalance calls = %d, want 1", len(fake.adjustCalls))
	}
	call := fake.adjustCalls[0]
	if call.tenantID != billingOperatorTenant || call.amountCents != 2500 || call.description != "operator credit: staging storage test" {
		t.Fatalf("AdjustBalance call = %+v", call)
	}
	out := buf.String()
	for _, want := range []string{"Credited EUR 25.00", billingOperatorTenant, "owner@example.com", "EUR 37.50", "txn-1"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}

	var jbuf bytes.Buffer
	if err := runBillingCredit(context.Background(), &jbuf, fake, nil, "", billingCreditRequest{
		Target: billingTenantTarget{TenantID: billingOperatorTenant}, Amount: "25", Reason: "r",
	}, true); err != nil {
		t.Fatalf("json: %v", err)
	}
	if !json.Valid(jbuf.Bytes()) {
		t.Errorf("not valid JSON: %s", jbuf.String())
	}
}

func TestRunBillingCreditDebit(t *testing.T) {
	fake := &fakeAdminBillingClient{adjustResp: &purserpb.BalanceTransaction{Id: "txn-2", AmountCents: -500, BalanceAfterCents: 1000}}
	var buf bytes.Buffer
	if err := runBillingCredit(context.Background(), &buf, fake, nil, "", billingCreditRequest{
		Target: billingTenantTarget{TenantID: billingOperatorTenant}, Amount: "-5.00", Reason: "reverse test credit", Debit: true,
	}, false); err != nil {
		t.Fatalf("runBillingCredit: %v", err)
	}
	if call := fake.adjustCalls[0]; call.amountCents != -500 || call.description != "operator debit: reverse test credit" {
		t.Fatalf("AdjustBalance call = %+v", call)
	}
	if !strings.Contains(buf.String(), "Debited EUR 5.00") {
		t.Errorf("output:\n%s", buf.String())
	}
}

func TestRunBillingCreditRefusesBeforeAnyRPC(t *testing.T) {
	for _, tc := range []struct {
		name    string
		jwt     string
		req     billingCreditRequest
		wantErr string
	}{
		{"negative without --debit", "jwt", billingCreditRequest{Target: billingTenantTarget{TenantID: billingOperatorTenant}, Amount: "-5", Reason: "r"}, "--debit"},
		{"missing reason", "jwt", billingCreditRequest{Target: billingTenantTarget{TenantID: billingOperatorTenant}, Amount: "5", Reason: " "}, "--reason is required"},
		{"no target", "jwt", billingCreditRequest{Amount: "5", Reason: "r"}, "one of --tenant-id or --email"},
		{"email without operator session", "", billingCreditRequest{Target: billingTenantTarget{Email: "owner@example.com"}, Amount: "5", Reason: "r"}, "platform-operator session"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lookup := &fakeUserLookup{}
			fake := &fakeAdminBillingClient{}
			err := runBillingCredit(context.Background(), &bytes.Buffer{}, fake, lookup, tc.jwt, tc.req, false)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
			if len(lookup.emails) != 0 || len(fake.adjustCalls) != 0 {
				t.Fatalf("refused command made RPCs: lookups %v adjust %v", lookup.emails, fake.adjustCalls)
			}
		})
	}
}

func TestRunBillingCreditPropagatesLookupFailure(t *testing.T) {
	lookup := &fakeUserLookup{err: errors.New("no user with that email")}
	fake := &fakeAdminBillingClient{}
	err := runBillingCredit(context.Background(), &bytes.Buffer{}, fake, lookup, "jwt", billingCreditRequest{
		Target: billingTenantTarget{Email: "nobody@example.com"}, Amount: "5", Reason: "r",
	}, false)
	if err == nil || !strings.Contains(err.Error(), "nobody@example.com") || !strings.Contains(err.Error(), "no user with that email") {
		t.Fatalf("err = %v", err)
	}
	if len(fake.adjustCalls) != 0 {
		t.Fatal("credit applied after a failed email lookup")
	}
}

func TestRunBillingSetTier(t *testing.T) {
	fake := &fakeAdminBillingClient{assignResp: &purserpb.AdminAssignTierResponse{
		SubscriptionId: "sub-1", TierName: "production", TierLevel: 4, BillingModel: "postpaid",
		PreviousTierName: "free", PreviousBillingModel: "postpaid", Changed: true, PrimaryClusterId: "c-1",
		EligibleClusterIds: []string{"c-1", "c-2"},
	}}
	var buf bytes.Buffer
	if err := runBillingSetTier(context.Background(), &buf, fake, nil, "", billingSetTierRequest{
		Target: billingTenantTarget{TenantID: billingOperatorTenant}, Tier: "production", Reason: "comped partner",
	}, false); err != nil {
		t.Fatalf("runBillingSetTier: %v", err)
	}
	if len(fake.assignCalls) != 1 {
		t.Fatalf("AdminAssignTier calls = %d", len(fake.assignCalls))
	}
	req := fake.assignCalls[0].req
	if req.GetTenantId() != billingOperatorTenant || req.GetTierName() != "production" || req.GetBillingModel() != "" || req.GetReason() != "comped partner" {
		t.Fatalf("request = %+v", req)
	}
	out := buf.String()
	for _, want := range []string{"production", "free (postpaid)", "production (postpaid)", "c-1, c-2"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}

	unchanged := &fakeAdminBillingClient{assignResp: &purserpb.AdminAssignTierResponse{TierName: "production", BillingModel: "postpaid"}}
	var ubuf bytes.Buffer
	if err := runBillingSetTier(context.Background(), &ubuf, unchanged, nil, "", billingSetTierRequest{
		Target: billingTenantTarget{TenantID: billingOperatorTenant}, Tier: "production", BillingModel: "POSTPAID", Reason: "r",
	}, false); err != nil {
		t.Fatalf("runBillingSetTier: %v", err)
	}
	if unchanged.assignCalls[0].req.GetBillingModel() != "postpaid" || !strings.Contains(ubuf.String(), "already on") {
		t.Fatalf("model %q, output:\n%s", unchanged.assignCalls[0].req.GetBillingModel(), ubuf.String())
	}

	var jbuf bytes.Buffer
	if err := runBillingSetTier(context.Background(), &jbuf, fake, nil, "", billingSetTierRequest{
		Target: billingTenantTarget{TenantID: billingOperatorTenant}, Tier: "production", Reason: "r",
	}, true); err != nil {
		t.Fatalf("json: %v", err)
	}
	if !json.Valid(jbuf.Bytes()) {
		t.Errorf("not valid JSON: %s", jbuf.String())
	}
}

func TestRunBillingSetTierResolvesEmail(t *testing.T) {
	lookup := &fakeUserLookup{resp: &commodorepb.AdminLookupUserByEmailResponse{TenantId: billingOperatorTenant, Email: "owner@example.com"}}
	fake := &fakeAdminBillingClient{assignResp: &purserpb.AdminAssignTierResponse{TierName: "payg", BillingModel: "prepaid", Changed: true}}
	if err := runBillingSetTier(context.Background(), &bytes.Buffer{}, fake, lookup, "jwt", billingSetTierRequest{
		Target: billingTenantTarget{Email: "owner@example.com"}, Tier: "payg", BillingModel: "prepaid", Reason: "r",
	}, false); err != nil {
		t.Fatalf("runBillingSetTier: %v", err)
	}
	if got := fake.assignCalls[0].req.GetTenantId(); got != billingOperatorTenant {
		t.Fatalf("tenant = %q", got)
	}
}

func TestRunBillingSetTierRefusesBeforeAnyRPC(t *testing.T) {
	target := billingTenantTarget{TenantID: billingOperatorTenant}
	for _, tc := range []struct {
		name    string
		req     billingSetTierRequest
		wantErr string
	}{
		{"missing tier", billingSetTierRequest{Target: target, Reason: "r"}, "--tier is required"},
		{"bad model", billingSetTierRequest{Target: target, Tier: "production", BillingModel: "monthly", Reason: "r"}, "--billing-model must be prepaid or postpaid"},
		{"missing reason", billingSetTierRequest{Target: target, Tier: "production"}, "--reason is required"},
		{"no target", billingSetTierRequest{Tier: "production", Reason: "r"}, "one of --tenant-id or --email"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeAdminBillingClient{}
			err := runBillingSetTier(context.Background(), &bytes.Buffer{}, fake, &fakeUserLookup{}, "jwt", tc.req, false)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
			if len(fake.assignCalls) != 0 {
				t.Fatal("refused command called AdminAssignTier")
			}
		})
	}
}

func TestAdminBillingOperatorCommandsAreWired(t *testing.T) {
	billing := newAdminBillingCmd()
	for name, flags := range map[string][]string{
		"credit":   {"tenant-id", "email", "amount", "reason", "debit"},
		"set-tier": {"tenant-id", "email", "tier", "billing-model", "reason"},
	} {
		cmd, _, err := billing.Find([]string{name})
		if err != nil || cmd.Name() != name {
			t.Fatalf("billing %s not registered: %v", name, err)
		}
		for _, flag := range flags {
			if cmd.Flags().Lookup(flag) == nil {
				t.Errorf("billing %s is missing --%s", name, flag)
			}
		}
	}
}
