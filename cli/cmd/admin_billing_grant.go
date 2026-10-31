package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"frameworks/cli/internal/ux"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type billingGrantRequest struct {
	Target       billingTenantTarget
	Tier         string
	FallbackTier string
	BaseFee      string
	WaiveUsage   bool
	Collection   string
	Until        string
	Reason       string
}

// parseGrantUntil reads --until: a date means through that day (the grant
// ends at the next UTC midnight); an RFC 3339 time is taken as given.
func parseGrantUntil(value string, now time.Time) (*timestamppb.Timestamp, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	until, err := (billing.OperatorGrantSpec{Until: value}).Expiry()
	if err != nil {
		return nil, fmt.Errorf("--until %q is not a date (2026-12-31) or an RFC 3339 time", value)
	}
	if !until.After(now) {
		return nil, fmt.Errorf("--until %s is not in the future", value)
	}
	return timestamppb.New(until), nil
}

func (r billingGrantRequest) toProto(tenantID string, now time.Time) (*purserpb.AdminSetBillingGrantRequest, error) {
	reason := strings.TrimSpace(r.Reason)
	if reason == "" {
		return nil, fmt.Errorf("--reason is required; it is recorded on the grant and the audit event")
	}
	collection := strings.ToLower(strings.TrimSpace(r.Collection))
	if collection != "" && collection != "provider" && collection != "invoice" {
		return nil, fmt.Errorf("--collection must be provider or invoice, got %q", r.Collection)
	}
	req := &purserpb.AdminSetBillingGrantRequest{
		TenantId: tenantID, TierName: strings.TrimSpace(r.Tier), WaiveUsage: r.WaiveUsage,
		Collection: collection, Reason: reason,
	}
	if r.FallbackTier != "" {
		value := strings.TrimSpace(r.FallbackTier)
		req.FallbackTierName = &value
	}
	if base := strings.TrimSpace(r.BaseFee); base != "" {
		if _, err := billing.ParseOperatorGrantAmount(base); err != nil {
			return nil, fmt.Errorf("--base-fee %q: %w", r.BaseFee, err)
		}
		req.BasePrice = &base
	}
	until, err := parseGrantUntil(r.Until, now)
	if err != nil {
		return nil, err
	}
	req.ExpiresAt = until
	if req.BasePrice == nil && !req.WaiveUsage && (collection == "" || collection == "provider") {
		return nil, fmt.Errorf("the grant changes nothing: pass --base-fee, --waive-usage or --collection invoice")
	}
	return req, nil
}

func runBillingGrantSet(ctx context.Context, w io.Writer, p adminBillingClient, lookup adminUserLookupClient, jwt string, req billingGrantRequest, outputJSON bool) error {
	if err := req.Target.validate(); err != nil {
		return err
	}
	if _, err := req.toProto("", time.Now()); err != nil {
		return err
	}
	tenantID, email, err := req.Target.resolve(ctx, lookup, jwt)
	if err != nil {
		return err
	}
	grantReq, err := req.toProto(tenantID, time.Now())
	if err != nil {
		return err
	}
	cctx, cancel := adminRPCContextTimeout(ctx, jwt, 30*time.Second)
	defer cancel()
	resp, err := p.AdminSetBillingGrant(cctx, grantReq)
	if err != nil {
		return fmt.Errorf("set billing grant for tenant %s: %w", tenantID, err)
	}
	return writeBillingGrant(w, "Granted billing arrangement to tenant "+tenantID, tenantID, email, resp, outputJSON)
}

func runBillingGrantShow(ctx context.Context, w io.Writer, p adminBillingClient, lookup adminUserLookupClient, jwt string, target billingTenantTarget, outputJSON bool) error {
	if err := target.validate(); err != nil {
		return err
	}
	tenantID, email, err := target.resolve(ctx, lookup, jwt)
	if err != nil {
		return err
	}
	cctx, cancel := adminRPCContext(ctx, jwt)
	defer cancel()
	resp, err := p.AdminGetBillingGrant(cctx, tenantID)
	if err != nil {
		return fmt.Errorf("read billing grant for tenant %s: %w", tenantID, err)
	}
	return writeBillingGrant(w, "Billing arrangement of tenant "+tenantID, tenantID, email, resp, outputJSON)
}

func runBillingGrantRevoke(ctx context.Context, w io.Writer, p adminBillingClient, lookup adminUserLookupClient, jwt string, target billingTenantTarget, reason string, recoveryTier string, outputJSON bool) error {
	if err := target.validate(); err != nil {
		return err
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return fmt.Errorf("--reason is required; it is recorded on the audit event")
	}
	tenantID, email, err := target.resolve(ctx, lookup, jwt)
	if err != nil {
		return err
	}
	cctx, cancel := adminRPCContextTimeout(ctx, jwt, 30*time.Second)
	defer cancel()
	resp, err := p.AdminRevokeBillingGrant(cctx, tenantID, reason, recoveryTier)
	if err != nil {
		return fmt.Errorf("revoke billing grant for tenant %s: %w", tenantID, err)
	}
	return writeBillingGrant(w, "Revoked the billing arrangement of tenant "+tenantID, tenantID, email, resp, outputJSON)
}

func writeBillingGrant(w io.Writer, headline, tenantID, email string, resp *purserpb.AdminBillingGrantResponse, outputJSON bool) error {
	if outputJSON {
		return writeProtoJSON(w, resp)
	}
	ux.Success(w, headline)
	fields := []ux.ResultField{{Key: "tenant", OK: true, Detail: tenantID}}
	if email != "" {
		fields = append(fields, ux.ResultField{Key: "email", OK: true, Detail: email})
	}
	if tier := resp.GetTier(); tier != nil {
		detail := fmt.Sprintf("%s (%s)", tier.GetTierName(), tier.GetBillingModel())
		if assignment := resp.GetAssignment(); assignment.GetChanged() && assignment.GetPreviousTierName() != "" {
			detail += fmt.Sprintf(", was %s (%s)", assignment.GetPreviousTierName(), assignment.GetPreviousBillingModel())
		}
		fields = append(fields, ux.ResultField{Key: "tier", OK: true, Detail: detail})
	}
	if grant := resp.GetGrant(); grant != nil {
		base := "tier price"
		if grant.BasePrice != nil {
			base = "EUR " + grant.GetBasePrice()
		}
		usage := "tier rates"
		if grant.GetWaiveUsage() {
			usage = "waived (rated at zero)"
		}
		until := "until revoked"
		if grant.GetExpiresAt() != nil {
			until = grant.GetExpiresAt().AsTime().UTC().Format(time.RFC3339)
		}
		fields = append(fields,
			ux.ResultField{Key: "base fee", OK: true, Detail: base},
			ux.ResultField{Key: "usage", OK: true, Detail: usage},
			ux.ResultField{Key: "collection", OK: true, Detail: grant.GetCollection()},
			ux.ResultField{Key: "expires", OK: grant.GetActive(), Detail: until},
			ux.ResultField{Key: "reason", OK: true, Detail: grant.GetReason()},
		)
		if grant.GetExpiryFailures() > 0 {
			fields = append(fields, ux.ResultField{Key: "expiry failure", OK: false, Detail: fmt.Sprintf("%d attempts: %s", grant.GetExpiryFailures(), grant.GetExpiryError())})
		}
		if grant.GetFallbackTierName() != "" {
			fields = append(fields, ux.ResultField{Key: "fallback tier", OK: true, Detail: grant.GetFallbackTierName()})
		}
		if !grant.GetActive() {
			fields = append(fields, ux.ResultField{Key: "state", OK: false, Detail: "expired; the expiry worker closes the phase and restores an eligible fallback"})
		}
	} else {
		fields = append(fields, ux.ResultField{Key: "grant", OK: true, Detail: "none"})
	}
	collection := "not ready: paid postpaid usage is refused until the tenant adds a card or you grant --collection invoice"
	if resp.GetCollectionReady() {
		collection = "ready (" + resp.GetCollectionProvider() + ")"
	}
	fields = append(fields, ux.ResultField{Key: "collection readiness", OK: resp.GetCollectionReady(), Detail: collection})
	ux.Result(w, fields)
	return nil
}

func newAdminBillingGrantCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "grant",
		Short: "Operator billing arrangements: base fee, usage waiver, manual invoicing",
		Long: `An operator billing arrangement stands in for the self-serve rule that a paid
postpaid tier needs Stripe or Mollie collection. Metering and rating keep
running under it, so invoices still show what the tenant used.

  --base-fee      EUR base fee in place of the tier's, e.g. 0 for no monthly fee.
                  Refused while the tenant has a Stripe or Mollie subscription,
                  which bills the tier's price on the provider's side.
  --waive-usage   rate usage at zero.
  --collection    provider (default): Stripe or Mollie collect what is owed, so
                  the tenant needs a card for usage. invoice: you collect
                  invoices by hand and record payments with
                  'frameworks admin billing record-payment'.
  --until         end of the arrangement: a date (through that day, UTC) or an
                  RFC 3339 time. Expiry closes the granted billing phase and restores
                  its fallback while the granted tier is still current.
  --fallback-tier active tier to restore when the grant ends; an initial tier
                  change otherwise remembers the previous tier.

A tenant that owes nothing (no base fee and usage waived) needs no collection
setup. Setting a grant replaces the previous one.`,
	}
	cmd.AddCommand(newAdminBillingGrantSetCmd(), newAdminBillingGrantShowCmd(), newAdminBillingGrantRevokeCmd(), newAdminBillingGrantListCmd())
	return cmd
}

func newAdminBillingGrantSetCmd() *cobra.Command {
	var target billingTenantTarget
	var req billingGrantRequest
	cmd := &cobra.Command{
		Use:   "set",
		Short: "Grant a tenant a billing arrangement, optionally on a tier",
		Example: `  frameworks admin billing grant set --email owner@example.com --tier supporter --base-fee 0 --reason "beta supporter"
  frameworks admin billing grant set --tenant-id <uuid> --tier supporter --base-fee 0 --collection invoice --reason "house account"
  frameworks admin billing grant set --tenant-id <uuid> --tier production --base-fee 0 --waive-usage --until 2026-12-31 --reason "staging test"`,
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
			return runBillingGrantSet(cmd.Context(), cmd.OutOrStdout(), p, lookup, jwt, req, output == "json")
		},
	}
	cmd.Flags().StringVar(&target.TenantID, "tenant-id", "", "tenant UUID")
	cmd.Flags().StringVar(&target.Email, "email", "", "email of a user in the tenant (needs a platform-operator session)")
	cmd.Flags().StringVar(&req.Tier, "tier", "", "also assign this tier (as set-tier); empty keeps the tenant's tier")
	cmd.Flags().StringVar(&req.BaseFee, "base-fee", "", "EUR base fee in place of the tier's, e.g. 0")
	cmd.Flags().BoolVar(&req.WaiveUsage, "waive-usage", false, "rate usage at zero")
	cmd.Flags().StringVar(&req.Collection, "collection", "", "provider (default) or invoice")
	cmd.Flags().StringVar(&req.Until, "until", "", "absolute deadline: date includes that UTC day; RFC3339 names an instant; expiry restores the fallback")
	cmd.Flags().StringVar(&req.FallbackTier, "fallback-tier", "", "tier restored when the grant ends (default: the original tier)")
	cmd.Flags().StringVar(&req.Reason, "reason", "", "why; recorded on the grant and the audit event (required)")
	return cmd
}

func newAdminBillingGrantShowCmd() *cobra.Command {
	var target billingTenantTarget
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Show a tenant's billing arrangement and whether its charges can be collected",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := target.validate(); err != nil {
				return err
			}
			p, lookup, jwt, cleanup, err := billingTargetClients(cmd.Context(), target)
			if err != nil {
				return err
			}
			defer cleanup()
			return runBillingGrantShow(cmd.Context(), cmd.OutOrStdout(), p, lookup, jwt, target, output == "json")
		},
	}
	cmd.Flags().StringVar(&target.TenantID, "tenant-id", "", "tenant UUID")
	cmd.Flags().StringVar(&target.Email, "email", "", "email of a user in the tenant (needs a platform-operator session)")
	return cmd
}

func newAdminBillingGrantRevokeCmd() *cobra.Command {
	var target billingTenantTarget
	var reason, tier string
	cmd := &cobra.Command{
		Use:   "revoke",
		Short: "Remove a tenant's billing arrangement",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := target.validate(); err != nil {
				return err
			}
			p, lookup, jwt, cleanup, err := billingTargetClients(cmd.Context(), target)
			if err != nil {
				return err
			}
			defer cleanup()
			return runBillingGrantRevoke(cmd.Context(), cmd.OutOrStdout(), p, lookup, jwt, target, reason, tier, output == "json")
		},
	}
	cmd.Flags().StringVar(&target.TenantID, "tenant-id", "", "tenant UUID")
	cmd.Flags().StringVar(&target.Email, "email", "", "email of a user in the tenant (needs a platform-operator session)")
	cmd.Flags().StringVar(&reason, "reason", "", "why; recorded on the audit event (required)")
	cmd.Flags().StringVar(&tier, "tier", "", "active tier to restore instead of the recorded fallback")
	return cmd
}

type billingRecordPaymentRequest struct {
	Target    billingTenantTarget
	InvoiceID string
	Reference string
	Reason    string
}

func runBillingRecordPayment(ctx context.Context, w io.Writer, p adminBillingClient, lookup adminUserLookupClient, jwt string, req billingRecordPaymentRequest, outputJSON bool) error {
	if err := req.Target.validate(); err != nil {
		return err
	}
	invoiceID := strings.TrimSpace(req.InvoiceID)
	if err := validateUUID(invoiceID); err != nil {
		return fmt.Errorf("--invoice-id: %w", err)
	}
	reference := strings.TrimSpace(req.Reference)
	if reference == "" {
		return fmt.Errorf("--reference is required: the bank transfer or other reference the payment arrived with")
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		return fmt.Errorf("--reason is required; it is recorded on the audit event")
	}
	tenantID, email, err := req.Target.resolve(ctx, lookup, jwt)
	if err != nil {
		return err
	}
	cctx, cancel := adminRPCContextTimeout(ctx, jwt, 30*time.Second)
	defer cancel()
	resp, err := p.AdminRecordInvoicePayment(cctx, &purserpb.AdminRecordInvoicePaymentRequest{
		TenantId: tenantID, InvoiceId: invoiceID, Reference: reference, Reason: reason,
	})
	if err != nil {
		return fmt.Errorf("record payment for invoice %s: %w", invoiceID, err)
	}
	if outputJSON {
		return writeProtoJSON(w, resp)
	}
	ux.Success(w, fmt.Sprintf("Recorded %s %s against invoice %s", resp.GetCurrency(), resp.GetAmount(), invoiceID))
	fields := []ux.ResultField{{Key: "tenant", OK: true, Detail: tenantID}}
	if email != "" {
		fields = append(fields, ux.ResultField{Key: "email", OK: true, Detail: email})
	}
	fields = append(fields,
		ux.ResultField{Key: "payment", OK: true, Detail: resp.GetPaymentId()},
		ux.ResultField{Key: "invoice status", OK: true, Detail: resp.GetInvoiceStatus()},
	)
	ux.Result(w, fields)
	return nil
}

func newAdminBillingRecordPaymentCmd() *cobra.Command {
	var target billingTenantTarget
	var req billingRecordPaymentRequest
	cmd := &cobra.Command{
		Use:   "record-payment",
		Short: "Record a bank transfer that paid a pending or overdue invoice in full",
		Long: `Record a payment you received outside Stripe and Mollie, such as a bank
transfer for a tenant on --collection invoice. The payment covers the invoice's
full open balance in the currency it was presented in, and marks it paid.`,
		Example: `  frameworks admin billing record-payment --tenant-id <uuid> --invoice-id <uuid> --reference "NL91ABNA 2026-10-03" --reason "bank transfer received"`,
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
			return runBillingRecordPayment(cmd.Context(), cmd.OutOrStdout(), p, lookup, jwt, req, output == "json")
		},
	}
	cmd.Flags().StringVar(&target.TenantID, "tenant-id", "", "tenant UUID")
	cmd.Flags().StringVar(&target.Email, "email", "", "email of a user in the tenant (needs a platform-operator session)")
	cmd.Flags().StringVar(&req.InvoiceID, "invoice-id", "", "invoice UUID (required)")
	cmd.Flags().StringVar(&req.Reference, "reference", "", "the reference the payment arrived with (required)")
	cmd.Flags().StringVar(&req.Reason, "reason", "", "why; recorded on the audit event (required)")
	return cmd
}

type billingGrantListClient interface {
	AdminListBillingGrants(context.Context, *purserpb.AdminListBillingGrantsRequest) (*purserpb.AdminListBillingGrantsResponse, error)
}

func runBillingGrantList(ctx context.Context, w io.Writer, client billingGrantListClient, jwt string, req *purserpb.AdminListBillingGrantsRequest, jsonOutput bool) error {
	callCtx, cancel := adminRPCContext(ctx, jwt)
	defer cancel()
	response, err := client.AdminListBillingGrants(callCtx, req)
	if err != nil {
		return err
	}
	if jsonOutput {
		return writeProtoJSON(w, response)
	}
	if len(response.GetGrants()) == 0 {
		fmt.Fprintln(w, "No operator grants found.")
	}
	if len(response.GetGrants()) > 0 {
		fmt.Fprintln(w, "TENANT  TIER  FALLBACK  BASE FEE  USAGE WAIVED  EXPIRES  STATE  REASON  EXPIRY FAILURES  EXPIRY ERROR")
	}
	for _, entry := range response.GetGrants() {
		grant := entry.GetGrant()
		until := "until revoked"
		state := "active"
		if grant.GetExpiresAt() != nil {
			until = grant.GetExpiresAt().AsTime().UTC().Format(time.RFC3339)
		}
		if !grant.GetActive() {
			state = "ended"
		}
		fallback := ""
		if grant.GetFallbackTierName() != "" {
			fallback = grant.GetFallbackTierName()
		}
		base := "tier price"
		if grant.BasePrice != nil {
			base = "EUR " + grant.GetBasePrice()
		}
		fmt.Fprintf(w, "%s  %s  %s  %s  %t  %s  %s  %q  %d  %q\n", entry.GetTenantId(), entry.GetTierName(), fallback, base, grant.GetWaiveUsage(), until, state, grant.GetReason(), grant.GetExpiryFailures(), grant.GetExpiryError())
	}
	if response.GetHasMore() {
		fmt.Fprintln(w, "More grants available; increase --offset for the next page.")
	}
	return nil
}

func newAdminBillingGrantListCmd() *cobra.Command {
	var expiring string
	req := &purserpb.AdminListBillingGrantsRequest{}
	cmd := &cobra.Command{Use: "list", Short: "List operator grants and retained grant history", RunE: func(cmd *cobra.Command, _ []string) error {
		if expiring != "" {
			normalized, err := normalizeDuration(expiring)
			if err != nil {
				return err
			}
			duration, err := time.ParseDuration(normalized)
			if err != nil || duration <= 0 {
				return fmt.Errorf("--expiring must be a positive duration")
			}
			req.ExpiringWithinSeconds = int64(duration / time.Second)
		}
		client, cfg, cleanup, err := purserGRPCClientFromContext(cmd.Context())
		if err != nil {
			return err
		}
		defer cleanup()
		defer client.Close()
		return runBillingGrantList(cmd.Context(), cmd.OutOrStdout(), client, cfg.Auth.JWT, req, output == "json")
	}}
	cmd.Flags().StringVar(&expiring, "expiring", "", "only grants expiring within this duration, e.g. 7d")
	cmd.Flags().BoolVar(&req.IncludeExpired, "include-expired", false, "include expired and replaced grants")
	cmd.Flags().Int32Var(&req.Limit, "limit", 100, "page size (1..500)")
	cmd.Flags().Int32Var(&req.Offset, "offset", 0, "rows to skip")
	return cmd
}
