package cmd

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/shopspring/decimal"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type checkoutRecoveryClient interface {
	AdminListCheckoutRecovery(context.Context, *purserpb.AdminListCheckoutRecoveryRequest) (*purserpb.AdminListCheckoutRecoveryResponse, error)
	AdminRecoverCheckout(context.Context, *purserpb.AdminRecoverCheckoutRequest) (*purserpb.AdminRecoverCheckoutResponse, error)
	AdminRetainDeliveredCheckout(context.Context, *purserpb.AdminRecoverCheckoutRequest) (*purserpb.AdminRecoverCheckoutResponse, error)
}

func runCheckoutRecoveryList(ctx context.Context, w io.Writer, p checkoutRecoveryClient, jwt string, r *purserpb.AdminListCheckoutRecoveryRequest, jsonOutput bool) error {
	if r.GetTenantId() != "" {
		if err := validateUUID(r.GetTenantId()); err != nil {
			return err
		}
	}
	if r.GetLimit() < 1 || r.GetLimit() > 100 || r.GetOffset() < 0 {
		return fmt.Errorf("limit must be 1..100 and offset non-negative")
	}
	if r.GetHistory() && r.GetTenantId() == "" {
		return fmt.Errorf("--history requires --tenant-id")
	}
	cctx, cancel := adminRPCContext(ctx, jwt)
	defer cancel()
	res, err := p.AdminListCheckoutRecovery(cctx, r)
	if err != nil {
		return err
	}
	if jsonOutput {
		return writeProtoJSON(w, res)
	}
	if r.GetHistory() {
		if len(res.GetEntries()) == 0 {
			fmt.Fprintln(w, "No checkout recovery history found.")
		} else {
			fmt.Fprintln(w, "REQUEST  RESERVATION  OPERATOR  OUTCOME  REASON  DETAIL  CREATED")
		}
		for _, entry := range res.GetEntries() {
			fmt.Fprintf(w, "%s  %s  %s  %s  %q  %q  %s\n", entry.GetId(), entry.GetReservationId(), entry.GetActorId(), entry.GetOutcome(), entry.GetReason(), entry.GetDetail(), recoveryTimestamp(entry.GetCreatedAt()))
		}
	} else {
		if len(res.GetItems()) == 0 {
			fmt.Fprintln(w, "No provider recovery work found.")
		} else {
			fmt.Fprintln(w, "WORK ID  PROVIDER  TENANT  RESERVATION  STATE  AMOUNT  NEXT ATTEMPT  DETAIL")
		}
		for _, item := range res.GetItems() {
			next := "-"
			if item.GetNextAttemptAt() != nil {
				next = item.GetNextAttemptAt().AsTime().UTC().Format(time.RFC3339)
			}
			amount := "unknown"
			if item.GetAmountKnown() {
				amount = recoveryAmount(item.GetAmountCents(), item.GetCurrency())
			}
			fmt.Fprintf(w, "%s  %s  %s  %s  %s/%s  %s  %s  %q\n", item.GetId(), item.GetProvider(), item.GetTenantId(), item.GetReservationId(), item.GetReservationStatus(), item.GetStatus(), amount, next, item.GetLastError())
		}
		for _, item := range res.GetItems() {
			if item.GetProvider() == "free_fallback" {
				fmt.Fprintln(w, "A scheduled Free fallback can be cancelled with admin billing set-tier --tenant-id <tenant> --tier <tier> --reason <reason>. A provider_stopping fallback must finish reconciliation first.")
				break
			}
		}
		if r.GetOffset() == 0 {
			writeHeldDowngrades(w, res.GetHeldDowngrades(), res.GetHeldDowngradesHasMore())
		}
	}
	if res.GetHasMore() {
		fmt.Fprintf(w, "More results: repeat with --offset %d\n", r.GetOffset()+r.GetLimit())
	}
	return nil
}

func runCheckoutRecoveryNow(ctx context.Context, w io.Writer, p checkoutRecoveryClient, jwt string, r *purserpb.AdminRecoverCheckoutRequest, jsonOutput, retainDelivered bool) error {
	if err := validateUUID(r.GetTenantId()); err != nil {
		return err
	}
	if err := validateUUID(r.GetReservationId()); err != nil {
		return err
	}
	if strings.TrimSpace(r.GetReason()) == "" {
		return fmt.Errorf("--reason is required")
	}
	if !r.GetConfirmed() {
		return fmt.Errorf("checkout recovery requires confirmation")
	}
	cctx, cancel := adminRPCContextTimeout(ctx, jwt, 40*time.Second)
	defer cancel()
	var res *purserpb.AdminRecoverCheckoutResponse
	var err error
	if retainDelivered {
		res, err = p.AdminRetainDeliveredCheckout(cctx, r)
	} else {
		res, err = p.AdminRecoverCheckout(cctx, r)
	}
	if err != nil {
		return err
	}
	if jsonOutput {
		return writeProtoJSON(w, res)
	}
	_, err = fmt.Fprintf(w, "Checkout recovery: %s (status %s, audit %s)\n%s\n", res.GetOutcome(), res.GetStatus(), res.GetAuditId(), res.GetDetail())
	return err
}

func confirmCheckoutRecovery(cmd *cobra.Command, res *purserpb.AdminRecoverCheckoutResponse, yes, retainDelivered bool) error {
	amount := "unknown (no provider amount observed)"
	if res.GetAmountKnown() {
		amount = recoveryAmount(res.GetAmountCents(), res.GetCurrency())
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "Checkout provider: %s; state: %s; amount: %s. %s\n", res.GetProvider(), res.GetStatus(), amount, res.GetDetail())
	if retainDelivered {
		fmt.Fprintln(cmd.ErrOrStderr(), "Confirm that this payment funded delivered service. It will be retained without refund; your identity and reason will be recorded.")
	} else {
		fmt.Fprintln(cmd.ErrOrStderr(), "Recovery may cancel the subscription and request refunds.")
	}
	if yes {
		return nil
	}
	fmt.Fprint(cmd.ErrOrStderr(), "Type yes to confirm: ")
	answer, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil || !strings.EqualFold(strings.TrimSpace(answer), "yes") {
		return fmt.Errorf("checkout recovery confirmation declined; use --yes for non-interactive confirmation")
	}
	return nil
}

func newAdminBillingRecoveryCmd() *cobra.Command {
	parent := &cobra.Command{Use: "recovery", Short: "Inspect provider work and reconcile unresolved purchases"}
	listReq := &purserpb.AdminListCheckoutRecoveryRequest{}
	list := &cobra.Command{Use: "list", Short: "List provider work, errors, held downgrades and historical payment delivery reviews", RunE: func(cmd *cobra.Command, _ []string) error {
		p, cfg, cleanup, err := purserGRPCClientFromContext(cmd.Context())
		if err != nil {
			return err
		}
		defer cleanup()
		defer p.Close()
		return runCheckoutRecoveryList(cmd.Context(), cmd.OutOrStdout(), p, cfg.Auth.JWT, listReq, output == "json")
	}}
	list.Flags().StringVar(&listReq.TenantId, "tenant-id", "", "optional tenant UUID")
	list.Flags().Int32Var(&listReq.Limit, "limit", 50, "page size (1..100)")
	list.Flags().BoolVar(&listReq.History, "history", false, "list operator requests and outcomes (requires --tenant-id)")
	list.Flags().Int32Var(&listReq.Offset, "offset", 0, "rows to skip")
	req := &purserpb.AdminRecoverCheckoutRequest{}
	var yes, retainDelivered bool
	recoverCmd := &cobra.Command{Use: "checkout", Short: "Recover one checkout immediately; may cancel and refund", RunE: func(cmd *cobra.Command, _ []string) error {
		req.Confirmed = false
		p, cfg, cleanup, err := purserGRPCClientFromContext(cmd.Context())
		if err != nil {
			return err
		}
		defer cleanup()
		defer p.Close()
		if err = validateUUID(req.GetTenantId()); err != nil {
			return err
		}
		if err = validateUUID(req.GetReservationId()); err != nil {
			return err
		}
		if strings.TrimSpace(req.GetReason()) == "" {
			return fmt.Errorf("--reason is required")
		}
		cctx, cancel := adminRPCContext(cmd.Context(), cfg.Auth.JWT)
		defer cancel()
		var preview *purserpb.AdminRecoverCheckoutResponse
		if retainDelivered {
			preview, err = p.AdminRetainDeliveredCheckout(cctx, req)
		} else {
			preview, err = p.AdminRecoverCheckout(cctx, req)
		}
		if err != nil {
			return err
		}
		if err = confirmCheckoutRecovery(cmd, preview, yes, retainDelivered); err != nil {
			return err
		}
		req.Confirmed = true
		return runCheckoutRecoveryNow(cmd.Context(), cmd.OutOrStdout(), p, cfg.Auth.JWT, req, output == "json", retainDelivered)
	}}
	recoverCmd.Flags().BoolVarP(&yes, "yes", "y", false, "confirm the displayed cancellation/refund risk without prompting")
	recoverCmd.Flags().BoolVar(&retainDelivered, "retain-delivered", false, "attest an ambiguous historical Mollie payment funded delivered service; retain it without refund")
	recoverCmd.Flags().StringVar(&req.TenantId, "tenant-id", "", "owning tenant UUID (required)")
	recoverCmd.Flags().StringVar(&req.ReservationId, "reservation-id", "", "checkout reservation UUID (required)")
	recoverCmd.Flags().StringVar(&req.Reason, "reason", "", "operator reason, retained with the recovery request (required)")
	parent.AddCommand(list, recoverCmd, newAdminProviderWebhookRetryCmd(), newAdminTierUpgradeRecoveryCmd(), newAdminTierDowngradeRecoveryCmd(), newAdminFallbackNoticeRecoveryCmd())
	return parent
}

func recoveryAmount(cents int64, currency string) string {
	return decimal.NewFromInt(cents).Shift(-2).StringFixed(2) + " " + currency
}

func recoveryTimestamp(value *timestamppb.Timestamp) string {
	if value == nil {
		return "-"
	}
	return value.AsTime().UTC().Format(time.RFC3339)
}
