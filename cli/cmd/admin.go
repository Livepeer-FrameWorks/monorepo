package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"regexp"
	"strings"
	"time"

	fwcfg "frameworks/cli/internal/config"
	commodore "frameworks/pkg/clients/commodore"
	qmclient "frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// uuidRegex validates UUID format (with or without hyphens)
var uuidRegex = regexp.MustCompile(`^[0-9a-fA-F]{8}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{12}$`)

// validateUUID checks if a string is a valid UUID
func validateUUID(id string) error {
	if !uuidRegex.MatchString(id) {
		return fmt.Errorf("invalid UUID format: %q", id)
	}
	return nil
}

// validateIP checks if a string is a valid IPv4 or IPv6 address
func validateIP(ip string) error {
	if net.ParseIP(ip) == nil {
		return fmt.Errorf("invalid IP address: %q", ip)
	}
	return nil
}

// validateTokenName ensures token name is not empty and reasonable length
func validateTokenName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("token name cannot be empty")
	}
	if len(name) > 256 {
		return fmt.Errorf("token name too long (max 256 characters)")
	}
	return nil
}

// validateBootstrapTokenKind ensures kind is valid
func validateBootstrapTokenKind(kind string) error {
	validKinds := map[string]bool{"edge_node": true, "service": true}
	if !validKinds[kind] {
		return fmt.Errorf("invalid token kind %q: must be 'edge_node' or 'service'", kind)
	}
	return nil
}

// validateDuration ensures duration string is valid
func validateDuration(d string) error {
	if d == "" {
		return nil // empty is ok, uses default
	}
	_, err := time.ParseDuration(d)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", d, err)
	}
	return nil
}

// promptConfirm asks user for confirmation. Returns true if user confirms.
// If skipConfirm is true (--yes flag), returns true without prompting.
func promptConfirm(prompt string, skipConfirm bool) bool {
	if skipConfirm {
		return true
	}

	fmt.Fprintf(os.Stderr, "%s [y/N]: ", prompt)
	reader := bufio.NewReader(os.Stdin)
	response, err := reader.ReadString('\n')
	if err != nil {
		return false
	}

	response = strings.TrimSpace(strings.ToLower(response))
	return response == "y" || response == "yes"
}

func newAdminCmd() *cobra.Command {
	adm := &cobra.Command{Use: "admin", Short: "Provider/admin operations"}
	adm.AddCommand(newAdminTokensCmd())
	adm.AddCommand(newAdminBootstrapTokensCmd())
	adm.AddCommand(newAdminTenantsCmd())
	adm.AddCommand(newAdminClustersCmd())
	adm.AddCommand(newAdminNodesCmd())
	return adm
}

func newAdminTokensCmd() *cobra.Command {
	tok := &cobra.Command{Use: "tokens", Short: "Manage API tokens (developer)"}
	tok.AddCommand(newAdminTokensCreateCmd())
	tok.AddCommand(newAdminTokensListCmd())
	tok.AddCommand(newAdminTokensRevokeCmd())
	return tok
}

func commodoreGRPCClientFromContext() (*commodore.GRPCClient, fwcfg.Context, error) {
	cfg, _, err := fwcfg.Load()
	if err != nil {
		return nil, fwcfg.Context{}, err
	}
	ctxCfg := fwcfg.GetCurrent(cfg)

	// Load API token from env
	envMap, _ := fwcfg.LoadEnvFile()
	apiToken := fwcfg.GetEnvValue("FW_API_TOKEN", envMap)
	if apiToken == "" {
		return nil, fwcfg.Context{}, fmt.Errorf("API token required; run 'frameworks login' first")
	}

	grpcAddr, err := fwcfg.RequireEndpoint(ctxCfg, "commodore_grpc_addr", ctxCfg.Endpoints.CommodoreGRPCAddr, false)
	if err != nil {
		return nil, fwcfg.Context{}, err
	}

	cli, err := commodore.NewGRPCClient(commodore.GRPCConfig{
		GRPCAddr:     grpcAddr,
		Timeout:      15 * time.Second,
		Logger:       logging.NewLogger(),
		ServiceToken: apiToken, // Use API token for auth
	})
	if err != nil {
		return nil, fwcfg.Context{}, fmt.Errorf("failed to connect to Commodore gRPC: %w", err)
	}
	return cli, ctxCfg, nil
}

func newAdminTokensCreateCmd() *cobra.Command {
	var name string
	var expires string
	var perms string
	cmd := &cobra.Command{Use: "create", Short: "Create developer API token", RunE: func(cmd *cobra.Command, args []string) error {
		// Validate inputs
		if err := validateTokenName(name); err != nil {
			return fmt.Errorf("--name: %w", err)
		}
		if err := validateDuration(expires); err != nil {
			return fmt.Errorf("--expires: %w", err)
		}

		cli, _, err := commodoreGRPCClientFromContext()
		if err != nil {
			return err
		}
		defer cli.Close()

		req := &pb.CreateAPITokenRequest{TokenName: name}
		if strings.TrimSpace(perms) != "" {
			req.Permissions = strings.Split(perms, ",")
		}
		if strings.TrimSpace(expires) != "" {
			d, _ := time.ParseDuration(expires) // already validated above
			expiresAt := timestamppb.New(time.Now().Add(d))
			req.ExpiresAt = expiresAt
		}

		cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		resp, err := cli.CreateAPIToken(cctx, req)
		if err != nil {
			return err
		}
		if output == "json" {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(resp)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Created token %q (id=%s)\n", resp.TokenName, resp.Id)
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
	return &cobra.Command{Use: "list", Short: "List developer API tokens", RunE: func(cmd *cobra.Command, args []string) error {
		cli, _, err := commodoreGRPCClientFromContext()
		if err != nil {
			return err
		}
		defer cli.Close()

		cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		resp, err := cli.ListAPITokens(cctx, nil)
		if err != nil {
			return err
		}
		if output == "json" {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(resp)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Tokens (%d)\n", len(resp.Tokens))
		for _, t := range resp.Tokens {
			fmt.Fprintf(cmd.OutOrStdout(), " - %s (%s) status=%s\n", t.TokenName, t.Id, t.Status)
		}
		return nil
	}}
}

func newAdminTokensRevokeCmd() *cobra.Command {
	var name string
	var yes bool
	cmd := &cobra.Command{Use: "revoke [id]", Short: "Revoke developer API token by ID or name", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cli, _, err := commodoreGRPCClientFromContext()
		if err != nil {
			return err
		}
		defer cli.Close()

		cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		var tokenID string
		var tokenName string
		if len(args) > 0 {
			tokenID = args[0]
			// Validate token ID format
			if err := validateUUID(tokenID); err != nil {
				return fmt.Errorf("token ID: %w", err)
			}
		} else if name != "" {
			// Look up token ID by name
			resp, err := cli.ListAPITokens(cctx, nil)
			if err != nil {
				return fmt.Errorf("failed to list tokens: %w", err)
			}
			for _, t := range resp.Tokens {
				if t.TokenName == name {
					tokenID = t.Id
					tokenName = t.TokenName
					break
				}
			}
			if tokenID == "" {
				return fmt.Errorf("no token found with name %q", name)
			}
		} else {
			return fmt.Errorf("either token ID or --name is required")
		}

		// Confirm revocation
		displayName := tokenID
		if tokenName != "" {
			displayName = fmt.Sprintf("%s (%s)", tokenName, tokenID)
		}
		if !promptConfirm(fmt.Sprintf("Revoke API token %s? This cannot be undone", displayName), yes) {
			fmt.Fprintln(cmd.OutOrStdout(), "Cancelled")
			return nil
		}

		_, err = cli.RevokeAPIToken(cctx, tokenID)
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Revoked token %s\n", tokenID)
		return nil
	}}
	cmd.Flags().StringVar(&name, "name", "", "revoke token by name instead of ID")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip confirmation prompt")
	return cmd
}

// === Quartermaster Bootstrap Tokens ===

func newAdminBootstrapTokensCmd() *cobra.Command {
	bt := &cobra.Command{Use: "bootstrap-tokens", Short: "Manage Quartermaster bootstrap tokens"}
	bt.AddCommand(newAdminBootstrapTokensCreateCmd())
	bt.AddCommand(newAdminBootstrapTokensListCmd())
	bt.AddCommand(newAdminBootstrapTokensRevokeCmd())
	return bt
}

func qmGRPCClientFromContext() (*qmclient.GRPCClient, fwcfg.Context, error) {
	cfg, _, err := fwcfg.Load()
	if err != nil {
		return nil, fwcfg.Context{}, err
	}
	ctxCfg := fwcfg.GetCurrent(cfg)

	grpcAddr, err := fwcfg.RequireEndpoint(ctxCfg, "quartermaster_grpc_addr", ctxCfg.Endpoints.QuartermasterGRPCAddr, false)
	if err != nil {
		return nil, fwcfg.Context{}, err
	}

	qm, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{
		GRPCAddr: grpcAddr,
		Timeout:  15 * time.Second,
		Logger:   logging.NewLogger(),
	})
	if err != nil {
		return nil, fwcfg.Context{}, fmt.Errorf("failed to connect to Quartermaster gRPC: %w", err)
	}
	return qm, ctxCfg, nil
}

func newAdminBootstrapTokensCreateCmd() *cobra.Command {
	var kind string
	var tenantID string
	var clusterID string
	var expectedIP string
	var ttl string
	var name string
	var usageLimit int
	cmd := &cobra.Command{Use: "create", Short: "Create bootstrap token (edge_node|service)", RunE: func(cmd *cobra.Command, args []string) error {
		// Validate inputs first
		if err := validateTokenName(name); err != nil {
			return fmt.Errorf("--name: %w", err)
		}
		if err := validateBootstrapTokenKind(kind); err != nil {
			return fmt.Errorf("--kind: %w", err)
		}
		if err := validateDuration(ttl); err != nil {
			return fmt.Errorf("--ttl: %w", err)
		}
		// edge_node requires tenant-id
		if kind == "edge_node" && strings.TrimSpace(tenantID) == "" {
			return fmt.Errorf("--tenant-id is required for edge_node tokens")
		}
		// Validate optional UUIDs if provided
		if tenantID != "" {
			if err := validateUUID(tenantID); err != nil {
				return fmt.Errorf("--tenant-id: %w", err)
			}
		}
		if clusterID != "" {
			if err := validateUUID(clusterID); err != nil {
				return fmt.Errorf("--cluster-id: %w", err)
			}
		}
		// Validate expected IP if provided
		if expectedIP != "" {
			if err := validateIP(expectedIP); err != nil {
				return fmt.Errorf("--expected-ip: %w", err)
			}
		}
		if usageLimit < 0 {
			return fmt.Errorf("--usage-limit cannot be negative")
		}

		qm, ctxCfg, err := qmGRPCClientFromContext()
		if err != nil {
			return err
		}
		defer qm.Close()
		if strings.TrimSpace(ctxCfg.Auth.ServiceToken) == "" && strings.TrimSpace(ctxCfg.Auth.JWT) == "" {
			return fmt.Errorf("service token or JWT required; run 'frameworks login' first")
		}
		req := &pb.CreateBootstrapTokenRequest{
			Name: name,
			Kind: kind,
			Ttl:  ttl,
		}
		if tenantID != "" {
			req.TenantId = &tenantID
		}
		if clusterID != "" {
			req.ClusterId = &clusterID
		}
		if expectedIP != "" {
			req.ExpectedIp = &expectedIP
		}
		if usageLimit > 0 {
			ul := int32(usageLimit)
			req.UsageLimit = &ul
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
		fmt.Fprintf(cmd.OutOrStdout(), "Created bootstrap token: %s (kind=%s) expires=%s\n", resp.Token.Token, resp.Token.Kind, resp.Token.ExpiresAt.AsTime().Format(time.RFC3339))
		return nil
	}}
	cmd.Flags().StringVar(&kind, "kind", "edge_node", "token kind: edge_node|service")
	cmd.Flags().StringVar(&tenantID, "tenant-id", "", "tenant id (required for edge_node)")
	cmd.Flags().StringVar(&clusterID, "cluster-id", "", "cluster id (optional)")
	cmd.Flags().StringVar(&expectedIP, "expected-ip", "", "expected client IP (optional)")
	cmd.Flags().StringVar(&ttl, "ttl", "24h", "time-to-live (e.g., 24h)")
	cmd.Flags().StringVar(&name, "name", "Bootstrap Token", "display name for the token")
	cmd.Flags().IntVar(&usageLimit, "usage-limit", 0, "maximum allowed uses (default 0 = single use)")
	return cmd
}

func newAdminBootstrapTokensListCmd() *cobra.Command {
	return &cobra.Command{Use: "list", Short: "List bootstrap tokens", RunE: func(cmd *cobra.Command, args []string) error {
		qm, ctxCfg, err := qmGRPCClientFromContext()
		if err != nil {
			return err
		}
		defer qm.Close()
		cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if ctxCfg.Auth.JWT != "" {
			cctx = context.WithValue(cctx, "jwt_token", ctxCfg.Auth.JWT)
		}
		resp, err := qm.ListBootstrapTokens(cctx, "", "", nil)
		if err != nil {
			return err
		}
		if output == "json" {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(resp)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Bootstrap tokens (%d)\n", len(resp.Tokens))
		for _, t := range resp.Tokens {
			used := ""
			if t.UsedAt != nil {
				used = " used"
			}
			tenant := t.TenantId
			cluster := t.ClusterId
			fmt.Fprintf(cmd.OutOrStdout(), " - %s (id=%s) kind=%s tenant=%s cluster=%s expires=%s%s\n", t.Name, t.Id, t.Kind, tenant, cluster, t.ExpiresAt.AsTime().Format(time.RFC3339), used)
		}
		return nil
	}}
}

func newAdminBootstrapTokensRevokeCmd() *cobra.Command {
	var name string
	var yes bool
	cmd := &cobra.Command{Use: "revoke [id]", Short: "Revoke bootstrap token by ID or name", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		qm, ctxCfg, err := qmGRPCClientFromContext()
		if err != nil {
			return err
		}
		defer qm.Close()
		cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if ctxCfg.Auth.JWT != "" {
			cctx = context.WithValue(cctx, "jwt_token", ctxCfg.Auth.JWT)
		}

		var tokenID string
		var tokenName string
		if len(args) > 0 {
			tokenID = args[0]
			// Validate token ID format
			if err := validateUUID(tokenID); err != nil {
				return fmt.Errorf("token ID: %w", err)
			}
		} else if name != "" {
			// Look up token ID by name
			resp, err := qm.ListBootstrapTokens(cctx, "", "", nil)
			if err != nil {
				return fmt.Errorf("failed to list bootstrap tokens: %w", err)
			}
			for _, t := range resp.Tokens {
				if t.Name == name {
					tokenID = t.Id
					tokenName = t.Name
					break
				}
			}
			if tokenID == "" {
				return fmt.Errorf("no bootstrap token found with name %q", name)
			}
		} else {
			return fmt.Errorf("either token ID or --name is required")
		}

		// Confirm revocation
		displayName := tokenID
		if tokenName != "" {
			displayName = fmt.Sprintf("%s (%s)", tokenName, tokenID)
		}
		if !promptConfirm(fmt.Sprintf("Revoke bootstrap token %s? This cannot be undone", displayName), yes) {
			fmt.Fprintln(cmd.OutOrStdout(), "Cancelled")
			return nil
		}

		if err := qm.RevokeBootstrapToken(cctx, tokenID); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Revoked bootstrap token %s\n", tokenID)
		return nil
	}}
	cmd.Flags().StringVar(&name, "name", "", "revoke token by name instead of ID")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip confirmation prompt")
	return cmd
}

// === Quartermaster Tenants ===

func newAdminTenantsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "tenants", Short: "Manage tenants"}
	cmd.AddCommand(newAdminTenantsListCmd())
	return cmd
}

func newAdminTenantsListCmd() *cobra.Command {
	return &cobra.Command{Use: "list", Short: "List all tenants", RunE: func(cmd *cobra.Command, args []string) error {
		qm, ctxCfg, err := qmGRPCClientFromContext()
		if err != nil {
			return err
		}
		defer qm.Close()
		cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if ctxCfg.Auth.JWT != "" {
			cctx = context.WithValue(cctx, "jwt_token", ctxCfg.Auth.JWT)
		}
		resp, err := qm.ListTenants(cctx, nil)
		if err != nil {
			return err
		}
		if output == "json" {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(resp)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Tenants (%d)\n", len(resp.Tenants))
		for _, t := range resp.Tenants {
			fmt.Fprintf(cmd.OutOrStdout(), " - %s (id=%s) tier=%s\n", t.Name, t.Id, t.DeploymentTier)
		}
		return nil
	}}
}

// === Quartermaster Clusters ===

func newAdminClustersCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "clusters", Short: "Manage clusters"}
	cmd.AddCommand(newAdminClustersListCmd())
	return cmd
}

func newAdminClustersListCmd() *cobra.Command {
	return &cobra.Command{Use: "list", Short: "List all clusters", RunE: func(cmd *cobra.Command, args []string) error {
		qm, ctxCfg, err := qmGRPCClientFromContext()
		if err != nil {
			return err
		}
		defer qm.Close()
		cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if ctxCfg.Auth.JWT != "" {
			cctx = context.WithValue(cctx, "jwt_token", ctxCfg.Auth.JWT)
		}
		resp, err := qm.ListClusters(cctx, nil)
		if err != nil {
			return err
		}
		if output == "json" {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(resp)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Clusters (%d)\n", len(resp.Clusters))
		for _, c := range resp.Clusters {
			fmt.Fprintf(cmd.OutOrStdout(), " - %s (id=%s) type=%s url=%s\n", c.ClusterName, c.ClusterId, c.ClusterType, c.BaseUrl)
		}
		return nil
	}}
}

// === Quartermaster Nodes ===

func newAdminNodesCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "nodes", Short: "Manage nodes"}
	cmd.AddCommand(newAdminNodesListCmd())
	return cmd
}

func newAdminNodesListCmd() *cobra.Command {
	var clusterID string
	var nodeType string
	var region string
	cmd := &cobra.Command{Use: "list", Short: "List nodes", RunE: func(cmd *cobra.Command, args []string) error {
		qm, ctxCfg, err := qmGRPCClientFromContext()
		if err != nil {
			return err
		}
		defer qm.Close()
		cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if ctxCfg.Auth.JWT != "" {
			cctx = context.WithValue(cctx, "jwt_token", ctxCfg.Auth.JWT)
		}
		resp, err := qm.ListNodes(cctx, clusterID, nodeType, region, nil)
		if err != nil {
			return err
		}
		if output == "json" {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(resp)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Nodes (%d)\n", len(resp.Nodes))
		for _, n := range resp.Nodes {
			fmt.Fprintf(cmd.OutOrStdout(), " - %s (id=%s) type=%s cluster=%s status=%s\n", n.NodeName, n.NodeId, n.NodeType, n.ClusterId, n.Status)
		}
		return nil
	}}
	cmd.Flags().StringVar(&clusterID, "cluster-id", "", "filter by cluster ID")
	cmd.Flags().StringVar(&nodeType, "type", "", "filter by node type (edge, regional, central)")
	cmd.Flags().StringVar(&region, "region", "", "filter by region")
	return cmd
}
