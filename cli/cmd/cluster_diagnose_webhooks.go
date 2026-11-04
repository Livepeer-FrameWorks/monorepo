package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"frameworks/cli/internal/controlplane"
	"frameworks/cli/internal/ux"
	bosunclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/bosun"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	bosunpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/bosun"
	"github.com/spf13/cobra"
)

type webhookDiagnosisClient interface {
	DiagnoseWebhooks(context.Context, string) (*bosunpb.DiagnoseWebhooksResponse, error)
}

func newClusterDiagnoseWebhooksCmd() *cobra.Command {
	var tenant string
	cmd := &cobra.Command{Use: "webhooks --tenant-id <uuid>", Short: "Inspect webhook endpoints, recent failures, and pending retries", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			tenant = strings.TrimSpace(tenant)
			if err := validateUUID(tenant); err != nil {
				return fmt.Errorf("--tenant-id: %w", err)
			}
			if err := recordDiagnosticRead(cmd.Context(), "webhooks", tenant, "bosun"); err != nil {
				return err
			}
			cfg, err := activeContextWithAuth(cmd.Context())
			if err != nil {
				return err
			}
			ep, err := controlplane.ResolveGRPC(cmd.Context(), cfg, "bosun")
			if err != nil {
				return err
			}
			defer ep.Cleanup()
			p, err := bosunclient.NewGRPCClient(bosunclient.GRPCConfig{GRPCAddr: ep.Address, Timeout: 30 * time.Second, Logger: logging.NewLogger(), ServiceToken: cfg.Auth.ServiceToken, AllowInsecure: ep.AllowInsecure, CACertFile: ep.CACertFile, CACertPEM: ep.CACertPEM, ServerName: ep.ServerName})
			if err != nil {
				return err
			}
			defer p.Close()
			return runWebhooksDiagnose(cmd.Context(), cmd.OutOrStdout(), p, cfg.Auth.JWT, tenant, output == "json")
		}}
	cmd.Flags().StringVar(&tenant, "tenant-id", "", "tenant UUID (required)")
	return cmd
}

func runWebhooksDiagnose(ctx context.Context, w io.Writer, p webhookDiagnosisClient, jwt, tenant string, jsonOutput bool) error {
	if err := requireOperatorSession(jwt); err != nil {
		return err
	}
	ctx, cancel := adminRPCContext(ctx, jwt)
	defer cancel()
	d, err := p.DiagnoseWebhooks(ctx, tenant)
	if err != nil {
		return fmt.Errorf("diagnose webhooks: %w", err)
	}
	if jsonOutput {
		return writeProtoJSON(w, d)
	}
	ux.Heading(w, "Webhooks for "+d.GetTenantId())
	for _, item := range d.GetEndpoints() {
		ep := item.GetEndpoint()
		fmt.Fprintf(w, "\n  %s %s state=%s disabled_reason=%s failures=%d events=%s\n", ep.GetId(), ep.GetUrl(), ep.GetStatus(), ep.GetDisabledReason(), ep.GetConsecutiveFailures(), strings.Join(ep.GetEventTypes(), ","))
		fmt.Fprintf(w, "  last_success=%s last_failure=%s deliveries=%d of %d\n", formatOptionalTime(ep.GetLastSuccessAt()), formatOptionalTime(ep.GetLastFailureAt()), len(item.GetDeliveries()), item.GetDeliveryCount())
		for _, d := range item.GetDeliveries() {
			fmt.Fprintf(w, "    %s event=%s status=%s attempts=%d http=%d error=%s next=%s\n", d.GetId(), d.GetEventType(), d.GetStatus(), d.GetAttempts(), d.GetLastStatusCode(), dashIfEmpty(d.GetLastErrorClass()), formatOptionalTime(d.GetNextAttemptAt()))
		}
	}
	if len(d.GetEndpoints()) == 0 {
		fmt.Fprintln(w, "  No endpoints configured.")
	}
	return nil
}
