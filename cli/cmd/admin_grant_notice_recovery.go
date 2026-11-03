package cmd

import (
	"bufio"
	"context"
	"fmt"
	"strings"
	"time"

	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/spf13/cobra"
)

type grantNoticeRecoveryClient interface {
	AdminResendOperatorGrantNotice(context.Context, *purserpb.AdminResendOperatorGrantNoticeRequest) (*purserpb.AdminResendOperatorGrantNoticeResponse, error)
}

// runGrantNoticeRecovery previews the tenant's newest abandoned grant notice
// and requeues exactly that one once confirmed.
func runGrantNoticeRecovery(cmd *cobra.Command, p grantNoticeRecoveryClient, jwt, tenantID, reason string, yes, jsonOutput bool) error {
	if err := validateUUID(tenantID); err != nil {
		return err
	}
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("--reason is required")
	}
	ctx, cancel := adminRPCContextTimeout(cmd.Context(), jwt, 10*time.Second)
	defer cancel()
	preview, err := p.AdminResendOperatorGrantNotice(ctx, &purserpb.AdminResendOperatorGrantNoticeRequest{TenantId: tenantID})
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "Grant %s notice for %s to %s: %s. %s\nThis requeues email delivery only.\n",
		preview.GetKind(), preview.GetEndsAt().AsTime().UTC().Format(time.RFC3339), preview.GetRecipient(), preview.GetStatus(), preview.GetDetail())
	if !yes {
		fmt.Fprint(cmd.ErrOrStderr(), "Type yes to confirm: ")
		answer, readErr := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
		if readErr != nil || !strings.EqualFold(strings.TrimSpace(answer), "yes") {
			return fmt.Errorf("grant notice retry declined")
		}
	}
	confirmCtx, confirmCancel := adminRPCContextTimeout(cmd.Context(), jwt, 10*time.Second)
	defer confirmCancel()
	result, err := p.AdminResendOperatorGrantNotice(confirmCtx, &purserpb.AdminResendOperatorGrantNoticeRequest{
		TenantId: tenantID, Reason: reason, Confirmed: true, Kind: preview.GetKind(), EndsAt: preview.GetEndsAt(),
	})
	if err != nil {
		return err
	}
	if jsonOutput {
		return writeProtoJSON(cmd.OutOrStdout(), result)
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "Grant notice: %s (recipient %s)\n%s\n", result.GetStatus(), result.GetRecipient(), result.GetDetail())
	return err
}

func newAdminGrantNoticeRecoveryCmd() *cobra.Command {
	var tenantID, reason string
	var yes bool
	cmd := &cobra.Command{
		Use:   "notice",
		Short: "Preview and requeue an abandoned grant ending or ended email",
		Long: `Purser stops sending a grant ending or ended email after five failed attempts.
Fix the tenant's billing email or the mail service first, then requeue the
newest abandoned notice. The grant and the subscription are not changed.`,
		Example: `  frameworks admin billing grant notice --tenant-id <uuid> --reason "billing email fixed"`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, cfg, cleanup, err := purserGRPCClientFromContext(cmd.Context())
			if err != nil {
				return err
			}
			defer cleanup()
			defer p.Close()
			return runGrantNoticeRecovery(cmd, p, cfg.Auth.JWT, tenantID, reason, yes, output == "json")
		},
	}
	cmd.Flags().StringVar(&tenantID, "tenant-id", "", "owning tenant UUID (required)")
	cmd.Flags().StringVar(&reason, "reason", "", "operator reason retained in the billing audit (required)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "confirm the displayed notice retry without prompting")
	return cmd
}
