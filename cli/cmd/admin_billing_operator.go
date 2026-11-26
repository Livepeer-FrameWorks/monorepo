package cmd

import (
	"context"
	"fmt"
	"google.golang.org/protobuf/types/known/timestamppb"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"frameworks/cli/internal/ux"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

// adminUserLookupClient is the narrow Commodore surface that turns --email
// into a tenant.
type adminUserLookupClient interface {
	AdminLookupUserByEmail(ctx context.Context, email string) (*commodorepb.AdminLookupUserByEmailResponse, error)
}

// billingTenantTarget names the tenant an operator billing command acts on:
// exactly one of a tenant ID or the email of one of its users.
type billingTenantTarget struct {
	TenantID string
	Email    string
}

func (t billingTenantTarget) validate() error {
	tenantID := strings.TrimSpace(t.TenantID)
	email := strings.TrimSpace(t.Email)
	switch {
	case tenantID == "" && email == "":
		return fmt.Errorf("pass one of --tenant-id or --email")
	case tenantID != "" && email != "":
		return fmt.Errorf("pass --tenant-id or --email, not both")
	case tenantID != "":
		if err := validateUUID(tenantID); err != nil {
			return fmt.Errorf("--tenant-id: %w", err)
		}
	case !strings.Contains(email, "@"):
		return fmt.Errorf("--email %q is not an email address", email)
	}
	return nil
}

// resolve returns the tenant ID, looking the email up in Commodore when the
// target names a user. That lookup is a cross-tenant read Commodore admits only
// for a platform-operator session.
func (t billingTenantTarget) resolve(ctx context.Context, lookup adminUserLookupClient, jwt string) (tenantID, email string, err error) {
	if tenantID = strings.TrimSpace(t.TenantID); tenantID != "" {
		return tenantID, "", nil
	}
	email = strings.TrimSpace(t.Email)
	if sessionErr := requireOperatorSession(jwt); sessionErr != nil {
		return "", "", fmt.Errorf("--email: %w", sessionErr)
	}
	if lookup == nil {
		return "", "", fmt.Errorf("--email: no Commodore connection to resolve it")
	}
	cctx, cancel := adminRPCContext(ctx, jwt)
	defer cancel()
	user, err := lookup.AdminLookupUserByEmail(cctx, email)
	if err != nil {
		return "", "", fmt.Errorf("resolve --email %s: %w", email, err)
	}
	if user.GetTenantId() == "" {
		return "", "", fmt.Errorf("resolve --email %s: Commodore returned no tenant", email)
	}
	return user.GetTenantId(), email, nil
}

var billingAmountPattern = regexp.MustCompile(`^(-)?(\d+)(?:\.(\d+))?$`)

// parseBillingAmountCents converts an EUR decimal such as "25.00" into cents.
// A negative amount removes balance and is accepted only with debit, and debit
// accepts only a negative amount, so the sign and the flag always agree.
func parseBillingAmountCents(amount string, debit bool) (int64, error) {
	s := strings.TrimSpace(amount)
	if s == "" {
		return 0, fmt.Errorf("--amount is required (EUR, e.g. 25.00)")
	}
	m := billingAmountPattern.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("--amount %q is not an EUR amount like 25.00", amount)
	}
	negative, whole, fraction := m[1] == "-", m[2], m[3]
	if len(fraction) > 2 {
		return 0, fmt.Errorf("--amount %q has more than two decimals; use at most two decimals (cents)", amount)
	}
	if len(strings.TrimLeft(whole, "0")) > 12 {
		return 0, fmt.Errorf("--amount %q is too large", amount)
	}
	euros, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("--amount %q: %w", amount, err)
	}
	cents := euros * 100
	if fraction != "" {
		for len(fraction) < 2 {
			fraction += "0"
		}
		fractionCents, err := strconv.ParseInt(fraction, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("--amount %q: %w", amount, err)
		}
		cents += fractionCents
	}
	if cents == 0 {
		return 0, fmt.Errorf("--amount must be non-zero")
	}
	if negative && !debit {
		return 0, fmt.Errorf("a negative --amount removes balance; pass --debit to confirm the debit")
	}
	if !negative && debit {
		return 0, fmt.Errorf("--debit requires a negative --amount (e.g. --amount=-5.00)")
	}
	if negative {
		cents = -cents
	}
	return cents, nil
}

func formatEURCents(cents int64) string {
	sign := ""
	if cents < 0 {
		sign = "-"
		cents = -cents
	}
	return fmt.Sprintf("EUR %s%d.%02d", sign, cents/100, cents%100)
}

type billingCreditRequest struct {
	Target      billingTenantTarget
	Amount      string
	Reason      string
	Debit       bool
	ReferenceID string
}

// runBillingCredit adjusts a tenant's EUR prepaid balance. Purser's
// AdjustBalance creates the balance row when the tenant has none, records the
// description as the ledger reason, and records the calling operator as the
// transaction's actor. Eligible funded Free accounts activate prepaid PAYG.
func runBillingCredit(ctx context.Context, w io.Writer, p adminBillingClient, lookup adminUserLookupClient, jwt string, req billingCreditRequest, outputJSON bool) error {
	if err := req.Target.validate(); err != nil {
		return err
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		return fmt.Errorf("--reason is required; it is recorded on the balance transaction")
	}
	cents, err := parseBillingAmountCents(req.Amount, req.Debit)
	if err != nil {
		return err
	}
	tenantID, email, err := req.Target.resolve(ctx, lookup, jwt)
	if err != nil {
		return err
	}
	verb, description := "Credited", "operator credit: "+reason
	if cents < 0 {
		verb, description = "Debited", "operator debit: "+reason
	}

	id := strings.TrimSpace(req.ReferenceID)
	if id == "" {
		id = uuid.NewString()
	}
	if referenceErr := validateUUID(id); referenceErr != nil {
		return fmt.Errorf("--reference-id: %w", referenceErr)
	}
	kind := "operator_adjustment"
	referenceID, referenceType := &id, &kind

	cctx, cancel := adminRPCContextTimeout(ctx, jwt, 30*time.Second)
	defer cancel()
	txn, err := p.AdjustBalance(cctx, tenantID, cents, description, referenceID, referenceType)
	if err != nil {
		return fmt.Errorf("adjust balance for tenant %s: %w; retry this operation with --reference-id %s", tenantID, err, id)
	}
	if txn == nil || txn.GetAmountCents() != cents {
		return fmt.Errorf("balance operation returned a different amount; inspect its reference before retrying")
	}
	if outputJSON {
		return writeProtoJSON(w, txn)
	}
	magnitude := cents
	if magnitude < 0 {
		magnitude = -magnitude
	}
	ux.Success(w, fmt.Sprintf("%s %s to tenant %s", verb, formatEURCents(magnitude), tenantID))
	fields := []ux.ResultField{{Key: "tenant", OK: true, Detail: tenantID}}
	if email != "" {
		fields = append(fields, ux.ResultField{Key: "email", OK: true, Detail: email})
	}
	fields = append(fields,
		ux.ResultField{Key: "balance", OK: true, Detail: formatEURCents(txn.GetBalanceAfterCents())},
		ux.ResultField{Key: "transaction", OK: true, Detail: txn.GetId()},
		ux.ResultField{Key: "retry reference", OK: true, Detail: id},
		ux.ResultField{Key: "reason", OK: true, Detail: description},
	)
	ux.Result(w, fields)
	return nil
}

type billingSetTierRequest struct {
	Review       string
	QuotedAt     string
	Target       billingTenantTarget
	Tier         string
	BillingModel string
	Reason       string
}

// runBillingSetTier assigns a tier through Purser's AdminAssignTier, which
// applies it immediately without the payment-collection precondition of the
// self-serve tier change and reconciles cluster access.
func runBillingSetTier(ctx context.Context, w io.Writer, p adminBillingClient, lookup adminUserLookupClient, jwt string, req billingSetTierRequest, outputJSON bool) error {
	if err := req.Target.validate(); err != nil {
		return err
	}
	tier := strings.TrimSpace(req.Tier)
	if tier == "" {
		return fmt.Errorf("--tier is required (see 'frameworks admin billing tiers')")
	}
	model := strings.ToLower(strings.TrimSpace(req.BillingModel))
	if model != "" && model != "prepaid" && model != "postpaid" {
		return fmt.Errorf("--billing-model must be prepaid or postpaid, got %q", req.BillingModel)
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		return fmt.Errorf("--reason is required; it is recorded on the subscription audit event")
	}
	tenantID, email, err := req.Target.resolve(ctx, lookup, jwt)
	if err != nil {
		return err
	}

	cctx, cancel := adminRPCContextTimeout(ctx, jwt, 30*time.Second)
	defer cancel()
	assignment := &purserpb.AdminAssignTierRequest{TenantId: tenantID, TierName: tier, BillingModel: model, Reason: reason}
	if req.Review == "" {
		quote, previewErr := p.PreviewAdminAssignTier(cctx, assignment)
		if previewErr != nil {
			return fmt.Errorf("price tier assignment: %w", previewErr)
		}
		if outputJSON {
			return writeProtoJSON(w, quote)
		}
		fmt.Fprintf(w, "Tenant: %s\nFinancial effects: %s\nQuoted at: %s\nExpires at: %s\nConfirm with --review %s --quoted-at %s and the same tenant, tier, model and reason.\n", tenantID, quote.GetEffectsJson(), quote.GetQuotedAt().AsTime().Format(time.RFC3339Nano), quote.GetExpiresAt().AsTime().Format(time.RFC3339Nano), quote.GetFingerprint(), quote.GetQuotedAt().AsTime().Format(time.RFC3339Nano))
		return nil
	}
	quotedAt, parseErr := time.Parse(time.RFC3339Nano, req.QuotedAt)
	if parseErr != nil {
		return fmt.Errorf("--quoted-at must match the preview timestamp: %w", parseErr)
	}
	resp, err := p.AdminAssignTierWithReview(cctx, &purserpb.AdminAssignTierReviewedRequest{Assignment: assignment, QuotedAt: timestamppb.New(quotedAt), ExpectedFingerprint: req.Review})
	if err != nil {
		return fmt.Errorf("assign tier %s to tenant %s: %w", tier, tenantID, err)
	}
	if outputJSON {
		return writeProtoJSON(w, resp)
	}
	current := fmt.Sprintf("%s (%s)", resp.GetTierName(), resp.GetBillingModel())
	if resp.GetChanged() {
		ux.Success(w, fmt.Sprintf("Assigned %s to tenant %s", current, tenantID))
	} else {
		ux.Success(w, fmt.Sprintf("Tenant %s is already on %s; cluster access re-reconciled", tenantID, current))
	}
	fields := []ux.ResultField{{Key: "tenant", OK: true, Detail: tenantID}}
	if email != "" {
		fields = append(fields, ux.ResultField{Key: "email", OK: true, Detail: email})
	}
	if resp.GetPreviousTierName() != "" {
		fields = append(fields, ux.ResultField{Key: "previous", OK: true, Detail: fmt.Sprintf("%s (%s)", resp.GetPreviousTierName(), resp.GetPreviousBillingModel())})
	}
	fields = append(fields, ux.ResultField{Key: "tier", OK: true, Detail: fmt.Sprintf("%s level %d", current, resp.GetTierLevel())})
	if resp.GetPrimaryClusterId() != "" {
		fields = append(fields, ux.ResultField{Key: "primary cluster", OK: true, Detail: resp.GetPrimaryClusterId()})
	}
	if len(resp.GetEligibleClusterIds()) > 0 {
		fields = append(fields, ux.ResultField{Key: "eligible clusters", OK: true, Detail: strings.Join(resp.GetEligibleClusterIds(), ", ")})
	}
	ux.Result(w, fields)
	return nil
}

// billingTargetClients dials Purser, and Commodore only when --email needs
// resolving. The returned cleanup closes both.
func billingTargetClients(ctx context.Context, target billingTenantTarget) (adminBillingClient, adminUserLookupClient, string, func(), error) {
	p, ctxCfg, purserCleanup, err := purserGRPCClientFromContext(ctx)
	if err != nil {
		return nil, nil, "", nil, err
	}
	cleanup := func() {
		_ = p.Close()
		purserCleanup()
	}
	if strings.TrimSpace(target.Email) == "" {
		return p, nil, ctxCfg.Auth.JWT, cleanup, nil
	}
	c, _, commodoreCleanup, err := commodoreGRPCClientFromContext(ctx)
	if err != nil {
		cleanup()
		return nil, nil, "", nil, err
	}
	return p, c, ctxCfg.Auth.JWT, func() {
		_ = c.Close()
		commodoreCleanup()
		cleanup()
	}, nil
}

func newAdminBillingCreditCmd() *cobra.Command {
	var target billingTenantTarget
	var req billingCreditRequest
	cmd := &cobra.Command{
		Use:   "credit",
		Short: "Add (or with --debit, remove) EUR prepaid balance for a tenant",
		Long: `Add EUR prepaid balance to a tenant, addressed by --tenant-id or by the
email of one of its users. Eligible funded Free accounts activate prepaid PAYG.
Prepaid accounts spend credit on usage; paid postpaid plans retain their terms
and apply credit when their invoice closes. This does not refund a card payment.

Every adjustment has a retry reference, printed before it is sent. Pass
--reference-id <uuid> to choose one or retry. Reusing the same reference
returns its original receipt; different money under that reference is refused.

Purser records "operator credit: <reason>" as the transaction's reason and,
when you are logged in, your user as its actor. --email needs a
platform-operator session ('frameworks login'). A negative --amount
(--amount=-5.00) removes balance and is refused unless --debit is also passed.`,
		Example: `  frameworks admin billing credit --email owner@example.com --amount 25.00 --reason "staging storage test"
  frameworks admin billing credit --tenant-id <uuid> --amount=-5.00 --debit --reason "reverse test credit"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			req.Target = target
			if err := target.validate(); err != nil {
				return err
			}
			p, lookup, jwt, cleanup, err := billingTargetClients(cmd.Context(), target)
			if err != nil {
				return err
			}
			defer cleanup()
			if strings.TrimSpace(req.ReferenceID) == "" {
				req.ReferenceID = uuid.NewString()
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "Adjustment retry reference: %s (reuse with --reference-id if the result is uncertain)\n", req.ReferenceID)
			return runBillingCredit(cmd.Context(), cmd.OutOrStdout(), p, lookup, jwt, req, output == "json")
		},
	}
	cmd.Flags().StringVar(&target.TenantID, "tenant-id", "", "tenant UUID")
	cmd.Flags().StringVar(&target.Email, "email", "", "email of a user in the tenant (needs a platform-operator session)")
	cmd.Flags().StringVar(&req.Amount, "amount", "", "EUR amount with at most two decimals, e.g. 25.00 (required)")
	cmd.Flags().StringVar(&req.Reason, "reason", "", "why the balance changes; recorded on the transaction (required)")
	cmd.Flags().StringVar(&req.ReferenceID, "reference-id", "", "stable UUID for this adjustment; reuse it when retrying the same operation")
	cmd.Flags().BoolVar(&req.Debit, "debit", false, "confirm a negative --amount that removes balance")
	return cmd
}

func newAdminBillingSetTierCmd() *cobra.Command {
	var target billingTenantTarget
	var req billingSetTierRequest
	cmd := &cobra.Command{
		Use:   "set-tier",
		Short: "Put a tenant on a billing tier as an operator decision",
		Long: `Preview an immediate tier assignment for an existing tenant, addressed by
--tenant-id or --email. Review the closing invoice, wallet movement, carried
charges and retained unused fee. Apply within five minutes with the returned
--review and --quoted-at and the same tenant, tier, billing model and reason.
Changed billing or usage requires another preview. No cash refund is issued.

This reviewed path supports Purser-owned arrangements. Native Stripe/Mollie
subscriptions and operator grants use their dedicated recovery or grant commands.
Inactive tiers and incompatible billing models are refused. Without
--billing-model, the catalog determines prepaid or postpaid. Initialize an
account with no subscription before reviewing an assignment.

The committed assignment reconciles cluster access, refreshes cached limits and
records the calling identity and --reason on the subscription audit event.`,
		Example: `  frameworks admin billing set-tier --email owner@example.com --tier production --reason "upgraded after card setup"
  frameworks admin billing set-tier --tenant-id <uuid> --tier payg --billing-model prepaid --reason "back to pay as you go"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			req.Target = target
			if err := target.validate(); err != nil {
				return err
			}
			p, lookup, jwt, cleanup, err := billingTargetClients(cmd.Context(), target)
			if err != nil {
				return err
			}
			defer cleanup()
			return runBillingSetTier(cmd.Context(), cmd.OutOrStdout(), p, lookup, jwt, req, output == "json")
		},
	}
	cmd.Flags().StringVar(&target.TenantID, "tenant-id", "", "tenant UUID")
	cmd.Flags().StringVar(&target.Email, "email", "", "email of a user in the tenant (needs a platform-operator session)")
	cmd.Flags().StringVar(&req.Review, "review", "", "fingerprint of the priced preview to apply")
	cmd.Flags().StringVar(&req.QuotedAt, "quoted-at", "", "exact RFC3339 timestamp from the preview")
	cmd.Flags().StringVar(&req.Tier, "tier", "", "tier name, e.g. payg, free, supporter, developer, production (required)")
	cmd.Flags().StringVar(&req.BillingModel, "billing-model", "", "prepaid|postpaid (default: the model the tier runs)")
	cmd.Flags().StringVar(&req.Reason, "reason", "", "why the tier changes; recorded on the audit event (required)")
	return cmd
}
