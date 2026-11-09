package cmd

import (
	"context"
	"fmt"
	"io"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/spf13/cobra"
)

type billingTaxReviewClient interface {
	AdminListBillingTaxReviews(context.Context, *purserpb.AdminListBillingTaxReviewsRequest) (*purserpb.AdminListBillingTaxReviewsResponse, error)
	AdminAcknowledgeBillingTaxReview(context.Context, *purserpb.AdminAcknowledgeBillingTaxReviewRequest) (*purserpb.AdminAcknowledgeBillingTaxReviewResponse, error)
}

func newAdminBillingTaxReviewsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "reviews", Short: "Inspect and acknowledge retained billing tax reviews", Long: "Inspect tenant-owned tax reviews and their original evidence. Acknowledgement records an operator decision with a reason; it does not verify or change issued documents, collect payment, or dismiss funds/scanner/VIES verification faults."}
	var tenant, provider, category, reviewStatus string
	list := &cobra.Command{Use: "list", Short: "List tax reviews with evidence hashes and retained acknowledgements", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		p, cfg, cleanup, err := purserGRPCClientFromContext(cmd.Context())
		if err != nil {
			return err
		}
		defer cleanup()
		defer p.Close()
		if err = requireOperatorSession(cfg.Auth.JWT); err != nil {
			return err
		}
		ctx := context.WithValue(cmd.Context(), ctxkeys.KeyJWTToken, cfg.Auth.JWT)
		return listBillingTaxReviews(ctx, cmd.OutOrStdout(), p, tenant, provider, category, reviewStatus)
	}}
	list.Flags().StringVar(&tenant, "tenant-id", "", "Customer tenant UUID (required)")
	list.Flags().StringVar(&provider, "provider", "", "Exact retained provider/network, for example stripe, mollie or tax")
	list.Flags().StringVar(&category, "category", "", "Exact retained tax-review kind; omit to include every tax category")
	list.Flags().StringVar(&reviewStatus, "status", "open", "open, resolved or all")
	if err := list.MarkFlagRequired("tenant-id"); err != nil {
		panic(err)
	}
	var ackTenant, id, hash, reason string
	ack := &cobra.Command{Use: "acknowledge", Short: "Acknowledge exactly the reviewed evidence with an audited reason", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		p, cfg, cleanup, err := purserGRPCClientFromContext(cmd.Context())
		if err != nil {
			return err
		}
		defer cleanup()
		defer p.Close()
		if err = requireOperatorSession(cfg.Auth.JWT); err != nil {
			return err
		}
		ctx, cancel := adminRPCContext(cmd.Context(), cfg.Auth.JWT)
		defer cancel()
		out, err := p.AdminAcknowledgeBillingTaxReview(ctx, &purserpb.AdminAcknowledgeBillingTaxReviewRequest{TenantId: ackTenant, ReviewId: id, EvidenceSha256: hash, Reason: reason})
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), string(out.GetRecordJson()))
		return err
	}}
	ack.Flags().StringVar(&ackTenant, "tenant-id", "", "Customer tenant UUID (required)")
	ack.Flags().StringVar(&id, "review-id", "", "Retained review UUID")
	ack.Flags().StringVar(&hash, "evidence-sha256", "", "evidence_sha256 from the inspected record")
	ack.Flags().StringVar(&reason, "reason", "", "Operator review decision, retained with actor identity")
	for _, name := range []string{"tenant-id", "review-id", "evidence-sha256", "reason"} {
		if err := ack.MarkFlagRequired(name); err != nil {
			panic(err)
		}
	}
	cmd.AddCommand(list, ack)
	return cmd
}

func listBillingTaxReviews(ctx context.Context, w io.Writer, p billingTaxReviewClient, tenant, provider, category, reviewStatus string) error {
	seen := map[string]bool{}
	after := ""
	for {
		pageCtx, cancel := adminRPCContext(ctx, ctxkeys.GetJWTToken(ctx))
		page, err := p.AdminListBillingTaxReviews(pageCtx, &purserpb.AdminListBillingTaxReviewsRequest{TenantId: tenant, Provider: provider, Category: category, Status: reviewStatus, AfterId: after, Limit: 100})
		cancel()
		if err != nil {
			return err
		}
		for _, row := range page.GetRecordsJson() {
			if _, err = fmt.Fprintln(w, string(row)); err != nil {
				return err
			}
		}
		next := page.GetNextCursor()
		if next == "" {
			return nil
		}
		if seen[next] {
			return fmt.Errorf("tax review listing repeated cursor %s", next)
		}
		seen[next] = true
		after = next
	}
}
