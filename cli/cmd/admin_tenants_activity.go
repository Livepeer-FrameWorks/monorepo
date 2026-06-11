package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	fwcfg "frameworks/cli/internal/config"
	"frameworks/cli/internal/controlplane"
	"frameworks/cli/internal/ux"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/periscope"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"

	"github.com/spf13/cobra"
)

// tenantActivityClient is the narrow Periscope surface the activity command
// uses (see adminTokensClient for the rationale behind cmd-local interfaces).
type tenantActivityClient interface {
	ListTenantActivity(ctx context.Context, timeRange *periscope.TimeRangeOpts, limit int32) (*periscopepb.ListTenantActivityResponse, error)
}

func periscopeGRPCClientFromContext(ctx context.Context) (*periscope.GRPCClient, fwcfg.Context, func(), error) {
	ctxCfg, err := activeContextWithAuth(ctx)
	if err != nil {
		return nil, fwcfg.Context{}, nil, err
	}

	ep, err := controlplane.ResolveGRPC(ctx, ctxCfg, "periscope")
	if err != nil {
		return nil, fwcfg.Context{}, nil, err
	}

	cli, err := periscope.NewGRPCClient(periscope.GRPCConfig{
		GRPCAddr:      ep.Address,
		Timeout:       15 * time.Second,
		Logger:        logging.NewLogger(),
		ServiceToken:  ctxCfg.Auth.ServiceToken,
		AllowInsecure: ep.AllowInsecure,
		ServerName:    ep.ServerName,
	})
	if err != nil {
		ep.Cleanup()
		return nil, fwcfg.Context{}, nil, fmt.Errorf("failed to connect to Periscope gRPC: %w", err)
	}
	return cli, ctxCfg, ep.Cleanup, nil
}

// runTenantsActivity joins tenant identity (Quartermaster ListTenants) with
// the cross-tenant activity rollup (Periscope ListTenantActivity) into the
// operator god view table. The Periscope call deliberately carries no user
// JWT: ListTenantActivity only answers service-credential calls, and the
// client falls back to the manifest service token when the context has no
// user identity.
func runTenantsActivity(ctx context.Context, w io.Writer, qm adminTenantsClient, ps tenantActivityClient, jwt string, since time.Duration, limit int32, outputJSON bool) error {
	qctx, qcancel := adminRPCContext(ctx, jwt)
	defer qcancel()
	tenantsResp, err := qm.ListTenants(qctx, nil)
	if err != nil {
		return fmt.Errorf("list tenants: %w", err)
	}
	tenantsByID := make(map[string]*quartermasterpb.Tenant, len(tenantsResp.Tenants))
	for _, t := range tenantsResp.Tenants {
		tenantsByID[t.Id] = t
	}

	actx, acancel := context.WithTimeout(ctx, 15*time.Second)
	defer acancel()
	activityResp, err := ps.ListTenantActivity(actx, &periscope.TimeRangeOpts{
		StartTime: time.Now().Add(-since),
		EndTime:   time.Now(),
	}, limit)
	if err != nil {
		return fmt.Errorf("list tenant activity: %w", err)
	}

	type tenantActivityRow struct {
		Name string `json:"name"`
		Tier string `json:"tier"`
		*periscopepb.TenantActivity
	}
	rows := make([]tenantActivityRow, 0, len(activityResp.Tenants))
	for _, a := range activityResp.Tenants {
		row := tenantActivityRow{TenantActivity: a}
		if t, ok := tenantsByID[a.TenantId]; ok {
			row.Name = t.Name
			row.Tier = t.DeploymentTier
		}
		rows = append(rows, row)
	}

	if outputJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}

	ux.Heading(w, fmt.Sprintf("Tenant activity — last %s (%d tenants with activity)", since, len(rows)))
	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "TENANT\tTIER\tLIVE\tVIEWERS NOW\tINGEST H\tVIEWER H\tEGRESS GB\tUNIQUES\tAPI REQS\tLAST STREAM")
	for _, row := range rows {
		name := row.Name
		if name == "" {
			name = row.TenantId
		}
		lastStream := "-"
		if row.LastStreamAt != nil && row.LastStreamAt.AsTime().Unix() > 0 {
			lastStream = row.LastStreamAt.AsTime().Format("2006-01-02")
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%.1f\t%.1f\t%.2f\t%d\t%d\t%s\n",
			name, row.Tier, row.LiveStreams, row.CurrentViewers,
			row.IngestHours, row.ViewerHours, row.EgressGb,
			row.UniqueViewers, row.ApiRequests, lastStream)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if quiet := len(tenantsResp.Tenants) - len(rows); quiet > 0 {
		_, _ = fmt.Fprintf(w, "\n%d tenant(s) had no activity in the window (use `admin tenants list` for the full roster)\n", quiet)
	}
	return nil
}

func newAdminTenantsActivityCmd() *cobra.Command {
	var since string
	var limit int32
	cmd := &cobra.Command{
		Use:   "activity",
		Short: "Show per-tenant activity rollup (god view)",
		Long: `Cross-tenant activity rollup for platform operators: live streams,
ingest/viewer hours, egress, unique viewers, and API usage per tenant,
joined with tenant names and tiers from Quartermaster.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			normalized, err := normalizeDuration(since)
			if err != nil {
				return fmt.Errorf("--since: %w", err)
			}
			window, err := time.ParseDuration(normalized)
			if err != nil {
				return fmt.Errorf("--since: %w", err)
			}

			qm, ctxCfg, qmCleanup, err := qmGRPCClientFromContext(cmd.Context())
			if err != nil {
				return err
			}
			defer qmCleanup()
			defer func() { _ = qm.Close() }()

			ps, _, psCleanup, err := periscopeGRPCClientFromContext(cmd.Context())
			if err != nil {
				return err
			}
			defer psCleanup()
			defer func() { _ = ps.Close() }()

			return runTenantsActivity(cmd.Context(), cmd.OutOrStdout(), qm, ps, ctxCfg.Auth.JWT, window, limit, output == "json")
		},
	}
	cmd.Flags().StringVar(&since, "since", "7d", "activity window (e.g. 24h, 7d, 30d)")
	cmd.Flags().Int32Var(&limit, "limit", 0, "max tenants returned (0 = server default)")
	return cmd
}
