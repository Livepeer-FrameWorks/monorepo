package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"frameworks/cli/internal/ux"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type billingGrantRequest struct {
	Target     billingTenantTarget
	Tier       string
	BaseFee    string
	WaiveUsage bool
	Collection string
	Until      string
	Reason     string
}

// parseGrantUntil reads --until: a date means through that day (the grant
// ends at the next UTC midnight); an RFC 3339 time is taken as given.
func parseGrantUntil(value string, now time.Time) (*timestamppb.Timestamp, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	var until time.Time
	if day, err := time.Parse(time.DateOnly, value); err == nil {
		until = day.AddDate(0, 0, 1)
	} else if at, err := time.Parse(time.RFC3339, value); err == nil {
		until = at
	} else {
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
	if base := strings.TrimSpace(r.BaseFee); base != "" {
		if m := billingAmountPattern.FindStringSubmatch(base); m == nil || m[1] != "" || len(m[3]) > 2 {
			return nil, fmt.Errorf("--base-fee %q is not a non-negative EUR amount with at most two decimals, like 0 or 9.50", r.BaseFee)
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

func runBillingGrantRevoke(ctx context.Context, w io.Writer, p adminBillingClient, lookup adminUserLookupClient, jwt string, target billingTenantTarget, reason string, outputJSON bool) error {
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
	resp, err := p.AdminRevokeBillingGrant(cctx, tenantID, reason)
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
		fields = append(fields, ux.ResultField{Key: "tier", OK: true, Detail: fmt.Sprintf("%s (%s)", tier.GetTierName(), tier.GetBillingModel())})
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
		if !grant.GetActive() {
			fields = append(fields, ux.ResultField{Key: "state", OK: false, Detail: "expired; the tier and the tenant's own payment setup apply"})
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
                  RFC 3339 time. After it the tier and the tenant's own payment
                  setup apply again.

A tenant that owes nothing (no base fee and usage waived) needs no collection
setup. Setting a grant replaces the previous one.`,
	}
	cmd.AddCommand(newAdminBillingGrantSetCmd(), newAdminBillingGrantShowCmd(), newAdminBillingGrantRevokeCmd())
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
	cmd.Flags().StringVar(&req.Until, "until", "", "end of the arrangement: 2026-12-31 (through that day, UTC) or an RFC 3339 time")
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
	var reason string
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
			return runBillingGrantRevoke(cmd.Context(), cmd.OutOrStdout(), p, lookup, jwt, target, reason, output == "json")
		},
	}
	cmd.Flags().StringVar(&target.TenantID, "tenant-id", "", "tenant UUID")
	cmd.Flags().StringVar(&target.Email, "email", "", "email of a user in the tenant (needs a platform-operator session)")
	cmd.Flags().StringVar(&reason, "reason", "", "why; recorded on the audit event (required)")
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
