package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	fwcfg "frameworks/cli/internal/config"
	qmapi "frameworks/pkg/api/quartermaster"
	commodore "frameworks/pkg/clients/commodore"
	qmclient "frameworks/pkg/clients/quartermaster"
	models "frameworks/pkg/models"
	"github.com/spf13/cobra"
)

func newAdminCmd() *cobra.Command {
	adm := &cobra.Command{Use: "admin", Short: "Provider/admin operations"}
	adm.AddCommand(newAdminTokensCmd())
	adm.AddCommand(newAdminBootstrapTokensCmd())
	return adm
}

func newAdminTokensCmd() *cobra.Command {
	tok := &cobra.Command{Use: "tokens", Short: "Manage API tokens (developer)"}
	tok.AddCommand(newAdminTokensCreateCmd())
	tok.AddCommand(newAdminTokensListCmd())
	tok.AddCommand(newAdminTokensRevokeCmd())
	return tok
}

func commodoreClientFromContext() (*commodore.Client, fwcfg.Context, error) {
	cfg, _, err := fwcfg.Load()
	if err != nil {
		return nil, fwcfg.Context{}, err
	}
	ctx := fwcfg.GetCurrent(cfg)
	base := strings.TrimRight(ctx.Endpoints.GatewayURL, "/") + "/developer"
	cli := commodore.NewClient(commodore.Config{BaseURL: base, Timeout: 15 * time.Second})
	return cli, ctx, nil
}

func newAdminTokensCreateCmd() *cobra.Command {
	var name string
	var expires string
	var perms string
	cmd := &cobra.Command{Use: "create", Short: "Create developer API token (via Gateway)", RunE: func(cmd *cobra.Command, args []string) error {
		cli, ctx, err := commodoreClientFromContext()
		if err != nil {
			return err
		}
		if strings.TrimSpace(ctx.Auth.JWT) == "" {
			return fmt.Errorf("JWT required; run 'frameworks login --email ...' first")
		}
		req := &models.CreateAPITokenRequest{TokenName: name}
		if strings.TrimSpace(perms) != "" {
			pp := strings.Split(perms, ",")
			req.Permissions = pp
		}
		if strings.TrimSpace(expires) != "" {
			d, err := time.ParseDuration(expires)
			if err != nil {
				return fmt.Errorf("invalid --expires: %w", err)
			}
			t := time.Now().Add(d)
			req.ExpiresAt = &t
		}
		cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		resp, err := cli.CreateAPIToken(cctx, ctx.Auth.JWT, req)
		if err != nil {
			return err
		}
		if output == "json" {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(resp)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Created token %q (id=%s)\n", resp.TokenName, resp.ID)
		if resp.TokenValue != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "Token value: %s\n", resp.TokenValue)
		}
		return nil
	}}
	cmd.Flags().StringVar(&name, "name", "", "token name (label)")
	cmd.Flags().StringVar(&expires, "expires", "", "expiry duration (e.g., 720h)")
	cmd.Flags().StringVar(&perms, "perms", "", "comma-separated permissions")
	return cmd
}

func newAdminTokensListCmd() *cobra.Command {
	return &cobra.Command{Use: "list", Short: "List developer API tokens (via Gateway)", RunE: func(cmd *cobra.Command, args []string) error {
		cli, ctx, err := commodoreClientFromContext()
		if err != nil {
			return err
		}
		if strings.TrimSpace(ctx.Auth.JWT) == "" {
			return fmt.Errorf("JWT required; run 'frameworks login --email ...' first")
		}
		cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		resp, err := cli.GetAPITokens(cctx, ctx.Auth.JWT)
		if err != nil {
			return err
		}
		if output == "json" {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(resp)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Tokens (%d)\n", resp.Count)
		for _, t := range resp.Tokens {
			fmt.Fprintf(cmd.OutOrStdout(), " - %s (%s) status=%s\n", t.TokenName, t.ID, t.Status)
		}
		return nil
	}}
}

func newAdminTokensRevokeCmd() *cobra.Command {
	return &cobra.Command{Use: "revoke <id>", Short: "Revoke developer API token (via Gateway)", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cli, ctx, err := commodoreClientFromContext()
		if err != nil {
			return err
		}
		if strings.TrimSpace(ctx.Auth.JWT) == "" {
			return fmt.Errorf("JWT required; run 'frameworks login --email ...' first")
		}
		id := args[0]
		cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, err = cli.RevokeAPIToken(cctx, ctx.Auth.JWT, id)
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Revoked token %s\n", id)
		return nil
	}}
}

// === Quartermaster Bootstrap Tokens ===

func newAdminBootstrapTokensCmd() *cobra.Command {
	bt := &cobra.Command{Use: "bootstrap-tokens", Short: "Manage Quartermaster bootstrap tokens"}
	bt.AddCommand(newAdminBootstrapTokensCreateCmd())
	bt.AddCommand(newAdminBootstrapTokensListCmd())
	bt.AddCommand(newAdminBootstrapTokensRevokeCmd())
	return bt
}

func qmClientFromContext() (*qmclient.Client, fwcfg.Context, error) {
	cfg, _, err := fwcfg.Load()
	if err != nil {
		return nil, fwcfg.Context{}, err
	}
	ctxCfg := fwcfg.GetCurrent(cfg)
	qm := qmclient.NewClient(qmclient.Config{BaseURL: ctxCfg.Endpoints.QuartermasterURL, ServiceToken: ctxCfg.Auth.ServiceToken, Timeout: 15 * time.Second})
	return qm, ctxCfg, nil
}

func newAdminBootstrapTokensCreateCmd() *cobra.Command {
	var kind string
	var tenantID string
	var clusterID string
	var expectedIP string
	var ttl string
	var metadata string
	cmd := &cobra.Command{Use: "create", Short: "Create bootstrap token (edge_node|service)", RunE: func(cmd *cobra.Command, args []string) error {
		qm, ctxCfg, err := qmClientFromContext()
		if err != nil {
			return err
		}
		if strings.TrimSpace(ctxCfg.Auth.ServiceToken) == "" && strings.TrimSpace(ctxCfg.Auth.JWT) == "" {
			return fmt.Errorf("service token or JWT required; run 'frameworks login' first")
		}
		req := &qmapi.CreateBootstrapTokenRequest{Kind: kind, TTL: ttl, Metadata: map[string]interface{}{}}
		if tenantID != "" {
			req.TenantID = &tenantID
		}
		if clusterID != "" {
			req.ClusterID = &clusterID
		}
		if expectedIP != "" {
			req.ExpectedIP = &expectedIP
		}
		if strings.TrimSpace(metadata) != "" {
			// parse simple key=value,key2=value2
			parts := strings.Split(metadata, ",")
			for _, p := range parts {
				kv := strings.SplitN(p, "=", 2)
				if len(kv) == 2 {
					req.Metadata[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
				}
			}
		}
		cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if ctxCfg.Auth.JWT != "" {
			cctx = context.WithValue(cctx, "jwt_token", ctxCfg.Auth.JWT)
		}
		resp, err := qm.CreateBootstrapToken(cctx, req)
		if err != nil {
			return err
		}
		if output == "json" {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(resp)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Created bootstrap token: %s (kind=%s) expires=%s\n", resp.Token.Token, resp.Token.Kind, resp.Token.ExpiresAt.Format(time.RFC3339))
		return nil
	}}
	cmd.Flags().StringVar(&kind, "kind", "edge_node", "token kind: edge_node|service")
	cmd.Flags().StringVar(&tenantID, "tenant-id", "", "tenant id (required for edge_node)")
	cmd.Flags().StringVar(&clusterID, "cluster-id", "", "cluster id (optional)")
	cmd.Flags().StringVar(&expectedIP, "expected-ip", "", "expected client IP (optional)")
	cmd.Flags().StringVar(&ttl, "ttl", "24h", "time-to-live (e.g., 24h)")
	cmd.Flags().StringVar(&metadata, "metadata", "", "comma-separated key=value metadata")
	return cmd
}

func newAdminBootstrapTokensListCmd() *cobra.Command {
	return &cobra.Command{Use: "list", Short: "List bootstrap tokens", RunE: func(cmd *cobra.Command, args []string) error {
		qm, ctxCfg, err := qmClientFromContext()
		if err != nil {
			return err
		}
		cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if ctxCfg.Auth.JWT != "" {
			cctx = context.WithValue(cctx, "jwt_token", ctxCfg.Auth.JWT)
		}
		resp, err := qm.ListBootstrapTokens(cctx)
		if err != nil {
			return err
		}
		if output == "json" {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(resp)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Bootstrap tokens (%d)\n", resp.Count)
		for _, t := range resp.Tokens {
			used := ""
			if t.UsedAt != nil {
				used = " used"
			}
			tenant := ""
			if t.TenantID != nil {
				tenant = *t.TenantID
			}
			cluster := ""
			if t.ClusterID != nil {
				cluster = *t.ClusterID
			}
			fmt.Fprintf(cmd.OutOrStdout(), " - id=%s kind=%s tenant=%s cluster=%s expires=%s%s\n", t.ID, t.Kind, tenant, cluster, t.ExpiresAt.Format(time.RFC3339), used)
		}
		return nil
	}}
}

func newAdminBootstrapTokensRevokeCmd() *cobra.Command {
	return &cobra.Command{Use: "revoke <id>", Short: "Revoke bootstrap token by id", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		qm, ctxCfg, err := qmClientFromContext()
		if err != nil {
			return err
		}
		cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if ctxCfg.Auth.JWT != "" {
			cctx = context.WithValue(cctx, "jwt_token", ctxCfg.Auth.JWT)
		}
		if err := qm.RevokeBootstrapToken(cctx, args[0]); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Revoked bootstrap token %s\n", args[0])
		return nil
	}}
}
