package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	fwcfg "frameworks/cli/internal/config"
	commodore "frameworks/pkg/clients/commodore"
	qmclient "frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/types/known/structpb"
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
	validKinds := map[string]bool{"edge_node": true, "service": true, "infrastructure_node": true}
	if !validKinds[kind] {
		return fmt.Errorf("invalid token kind %q: must be 'edge_node', 'service', or 'infrastructure_node'", kind)
	}
	return nil
}

func normalizeDuration(d string) (string, error) {
	if d == "" {
		return "", nil
	}
	normalized := d
	if strings.HasSuffix(d, "d") {
		daysStr := strings.TrimSuffix(d, "d")
		days, err := strconv.Atoi(daysStr)
		if err != nil {
			return "", fmt.Errorf("invalid duration %q: %w", d, err)
		}
		if days <= 0 {
			return "", fmt.Errorf("invalid duration %q: must be greater than 0", d)
		}
		normalized = fmt.Sprintf("%dh", days*24)
	}
	_, err := time.ParseDuration(normalized)
	if err != nil {
		return "", fmt.Errorf("invalid duration %q: %w", d, err)
	}
	return normalized, nil
}

func parseCommaList(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func parseStructJSON(value string) (*structpb.Struct, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(value), &m); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	return structpb.NewStruct(m)
}

func optionalStringFlag(cmd *cobra.Command, name, value string) *string {
	if cmd.Flags().Changed(name) {
		v := value
		return &v
	}
	return nil
}

func optionalInt32Flag(cmd *cobra.Command, name string, value int) *int32 {
	if cmd.Flags().Changed(name) {
		v := int32(value)
		return &v
	}
	return nil
}

func optionalFloat64Flag(cmd *cobra.Command, name string, value float64) *float64 {
	if cmd.Flags().Changed(name) {
		v := value
		return &v
	}
	return nil
}

func optionalBoolFlag(cmd *cobra.Command, name string, value bool) *bool {
	if cmd.Flags().Changed(name) {
		v := value
		return &v
	}
	return nil
}

// promptConfirm asks user for confirmation. Returns true if user confirms.
// If skipConfirm is true (--yes flag), returns true without prompting.
func promptConfirm(prompt string, skipConfirm bool) bool {
	if skipConfirm {
		return true
	}

	_, _ = fmt.Fprintf(os.Stderr, "%s [y/N]: ", prompt)
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
	ctxCfg.Auth = fwcfg.ResolveAuth(ctxCfg)

	if ctxCfg.Auth.ServiceToken == "" {
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
		ServiceToken: ctxCfg.Auth.ServiceToken,
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
		normalizedExpires, err := normalizeDuration(expires)
		if err != nil {
			return fmt.Errorf("--expires: %w", err)
		}

		cli, _, err := commodoreGRPCClientFromContext()
		if err != nil {
			return err
		}
		defer func() { _ = cli.Close() }()

		req := &pb.CreateAPITokenRequest{TokenName: name}
		if strings.TrimSpace(perms) != "" {
			req.Permissions = strings.Split(perms, ",")
		}
		if strings.TrimSpace(normalizedExpires) != "" {
			d, _ := time.ParseDuration(normalizedExpires) // already validated above
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
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Created token %q (id=%s)\n", resp.TokenName, resp.Id)
		if resp.TokenValue != "" {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Token value: %s\n", resp.TokenValue)
		}
		return nil
	}}
	cmd.Flags().StringVar(&name, "name", "", "token name (label)")
	cmd.Flags().StringVar(&expires, "expires", "", "expiry duration (e.g., 24h, 7d, 720h)")
	cmd.Flags().StringVar(&perms, "perms", "", "comma-separated permissions")
	return cmd
}

func newAdminTokensListCmd() *cobra.Command {
	return &cobra.Command{Use: "list", Short: "List developer API tokens", RunE: func(cmd *cobra.Command, args []string) error {
		cli, _, err := commodoreGRPCClientFromContext()
		if err != nil {
			return err
		}
		defer func() { _ = cli.Close() }()

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
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Tokens (%d)\n", len(resp.Tokens))
		for _, t := range resp.Tokens {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), " - %s (%s) status=%s\n", t.TokenName, t.Id, t.Status)
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
		defer func() { _ = cli.Close() }()

		cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		var tokenID string
		var tokenName string
		if len(args) > 0 {
			tokenID = args[0]
			// Validate token ID format
			if errValidate := validateUUID(tokenID); errValidate != nil {
				return fmt.Errorf("token ID: %w", errValidate)
			}
		} else if name != "" {
			// Look up token ID by name
			resp, errList := cli.ListAPITokens(cctx, nil)
			if errList != nil {
				return fmt.Errorf("failed to list tokens: %w", errList)
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
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Cancelled")
			return nil
		}

		_, err = cli.RevokeAPIToken(cctx, tokenID)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Revoked token %s\n", tokenID)
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
	ctxCfg.Auth = fwcfg.ResolveAuth(ctxCfg)

	grpcAddr, err := fwcfg.RequireEndpoint(ctxCfg, "quartermaster_grpc_addr", ctxCfg.Endpoints.QuartermasterGRPCAddr, false)
	if err != nil {
		return nil, fwcfg.Context{}, err
	}

	qm, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{
		GRPCAddr:     grpcAddr,
		Timeout:      15 * time.Second,
		Logger:       logging.NewLogger(),
		ServiceToken: ctxCfg.Auth.ServiceToken,
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
	cmd := &cobra.Command{Use: "create", Short: "Create bootstrap token (edge_node|service|infrastructure_node)", RunE: func(cmd *cobra.Command, args []string) error {
		// Validate inputs first
		if err := validateTokenName(name); err != nil {
			return fmt.Errorf("--name: %w", err)
		}
		if err := validateBootstrapTokenKind(kind); err != nil {
			return fmt.Errorf("--kind: %w", err)
		}
		normalizedTTL, err := normalizeDuration(ttl)
		if err != nil {
			return fmt.Errorf("--ttl: %w", err)
		}
		// edge_node requires tenant-id and cluster-id
		if kind == "edge_node" && strings.TrimSpace(tenantID) == "" {
			return fmt.Errorf("--tenant-id is required for edge_node tokens")
		}
		if kind == "edge_node" && strings.TrimSpace(clusterID) == "" {
			return fmt.Errorf("--cluster-id is required for edge_node tokens (binds token to specific cluster)")
		}
		// Validate optional UUIDs if provided
		if tenantID != "" {
			if errValidate := validateUUID(tenantID); errValidate != nil {
				return fmt.Errorf("--tenant-id: %w", errValidate)
			}
		}
		// cluster_id is a string identifier; do not enforce UUID format
		// Validate expected IP if provided
		if expectedIP != "" {
			if errValidate := validateIP(expectedIP); errValidate != nil {
				return fmt.Errorf("--expected-ip: %w", errValidate)
			}
		}
		if usageLimit < 0 {
			return fmt.Errorf("--usage-limit cannot be negative")
		}

		qm, ctxCfg, err := qmGRPCClientFromContext()
		if err != nil {
			return err
		}
		defer func() { _ = qm.Close() }()
		if strings.TrimSpace(ctxCfg.Auth.ServiceToken) == "" && strings.TrimSpace(ctxCfg.Auth.JWT) == "" {
			return fmt.Errorf("service token or JWT required; run 'frameworks login' first")
		}
		req := &pb.CreateBootstrapTokenRequest{
			Name: name,
			Kind: kind,
			Ttl:  normalizedTTL,
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
			cctx = context.WithValue(cctx, ctxkeys.KeyJWTToken, ctxCfg.Auth.JWT)
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
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Created bootstrap token: %s (kind=%s) expires=%s\n", resp.Token.Token, resp.Token.Kind, resp.Token.ExpiresAt.AsTime().Format(time.RFC3339))
		return nil
	}}
	cmd.Flags().StringVar(&kind, "kind", "edge_node", "token kind: edge_node|service|infrastructure_node")
	cmd.Flags().StringVar(&tenantID, "tenant-id", "", "tenant id (required for edge_node)")
	cmd.Flags().StringVar(&clusterID, "cluster-id", "", "cluster id (required for edge_node, binds token to cluster)")
	cmd.Flags().StringVar(&expectedIP, "expected-ip", "", "expected client IP (optional)")
	cmd.Flags().StringVar(&ttl, "ttl", "24h", "time-to-live (e.g., 24h, 7d)")
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
		defer func() { _ = qm.Close() }()
		cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if ctxCfg.Auth.JWT != "" {
			cctx = context.WithValue(cctx, ctxkeys.KeyJWTToken, ctxCfg.Auth.JWT)
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
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Bootstrap tokens (%d)\n", len(resp.Tokens))
		for _, t := range resp.Tokens {
			used := ""
			if t.UsedAt != nil {
				used = " used"
			}
			tenant := "<any>"
			if t.TenantId != nil {
				tenant = *t.TenantId
			}
			cluster := "<any>"
			if t.ClusterId != nil {
				cluster = *t.ClusterId
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), " - %s (id=%s) kind=%s tenant=%s cluster=%s expires=%s%s\n", t.Name, t.Id, t.Kind, tenant, cluster, t.ExpiresAt.AsTime().Format(time.RFC3339), used)
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
		defer func() { _ = qm.Close() }()
		cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if ctxCfg.Auth.JWT != "" {
			cctx = context.WithValue(cctx, ctxkeys.KeyJWTToken, ctxCfg.Auth.JWT)
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
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Cancelled")
			return nil
		}

		if err := qm.RevokeBootstrapToken(cctx, tokenID); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Revoked bootstrap token %s\n", tokenID)
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
		defer func() { _ = qm.Close() }()
		cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if ctxCfg.Auth.JWT != "" {
			cctx = context.WithValue(cctx, ctxkeys.KeyJWTToken, ctxCfg.Auth.JWT)
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
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Tenants (%d)\n", len(resp.Tenants))
		for _, t := range resp.Tenants {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), " - %s (id=%s) tier=%s\n", t.Name, t.Id, t.DeploymentTier)
		}
		return nil
	}}
}

// === Quartermaster Clusters ===

func newAdminClustersCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "clusters", Short: "Manage clusters"}
	cmd.AddCommand(newAdminClustersListCmd())
	cmd.AddCommand(newAdminClustersCreateCmd())
	cmd.AddCommand(newAdminClustersUpdateCmd())
	cmd.AddCommand(newAdminClustersAccessCmd())
	cmd.AddCommand(newAdminClustersInvitesCmd())
	cmd.AddCommand(newAdminClustersSubscriptionsCmd())
	return cmd
}

func newAdminClustersListCmd() *cobra.Command {
	return &cobra.Command{Use: "list", Short: "List all clusters", RunE: func(cmd *cobra.Command, args []string) error {
		qm, ctxCfg, err := qmGRPCClientFromContext()
		if err != nil {
			return err
		}
		defer func() { _ = qm.Close() }()
		cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if ctxCfg.Auth.JWT != "" {
			cctx = context.WithValue(cctx, ctxkeys.KeyJWTToken, ctxCfg.Auth.JWT)
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
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Clusters (%d)\n", len(resp.Clusters))
		for _, c := range resp.Clusters {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), " - %s (id=%s) type=%s url=%s\n", c.ClusterName, c.ClusterId, c.ClusterType, c.BaseUrl)
		}
		return nil
	}}
}

func newAdminClustersCreateCmd() *cobra.Command {
	var clusterID string
	var clusterName string
	var clusterType string
	var baseURL string
	var databaseURL string
	var periscopeURL string
	var kafkaBrokers string
	var maxStreams int
	var maxViewers int
	var maxBandwidth int
	var ownerTenantID string
	var deploymentModel string
	cmd := &cobra.Command{Use: "create", Short: "Create a cluster", RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(clusterID) == "" {
			return fmt.Errorf("--cluster-id is required")
		}
		if strings.TrimSpace(clusterName) == "" {
			return fmt.Errorf("--cluster-name is required")
		}
		if strings.TrimSpace(clusterType) == "" {
			return fmt.Errorf("--cluster-type is required")
		}
		if strings.TrimSpace(baseURL) == "" {
			return fmt.Errorf("--base-url is required")
		}
		if ownerTenantID != "" {
			if err := validateUUID(ownerTenantID); err != nil {
				return fmt.Errorf("--owner-tenant-id: %w", err)
			}
		}
		if deploymentModel == "" {
			deploymentModel = "managed"
		}
		if deploymentModel != "managed" && deploymentModel != "shared" {
			return fmt.Errorf("--deployment-model must be 'managed' or 'shared'")
		}

		qm, ctxCfg, err := qmGRPCClientFromContext()
		if err != nil {
			return err
		}
		defer func() { _ = qm.Close() }()
		cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if ctxCfg.Auth.JWT != "" {
			cctx = context.WithValue(cctx, ctxkeys.KeyJWTToken, ctxCfg.Auth.JWT)
		}

		req := &pb.CreateClusterRequest{
			ClusterId:            clusterID,
			ClusterName:          clusterName,
			ClusterType:          clusterType,
			BaseUrl:              baseURL,
			KafkaBrokers:         parseCommaList(kafkaBrokers),
			MaxConcurrentStreams: int32(maxStreams),
			MaxConcurrentViewers: int32(maxViewers),
			MaxBandwidthMbps:     int32(maxBandwidth),
			DeploymentModel:      deploymentModel,
		}
		if databaseURL != "" {
			req.DatabaseUrl = &databaseURL
		}
		if periscopeURL != "" {
			req.PeriscopeUrl = &periscopeURL
		}
		if ownerTenantID != "" {
			req.OwnerTenantId = &ownerTenantID
		}

		resp, err := qm.CreateCluster(cctx, req)
		if err != nil {
			return err
		}
		if output == "json" {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(resp)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Created cluster %s (%s)\n", resp.Cluster.ClusterName, resp.Cluster.ClusterId)
		return nil
	}}
	cmd.Flags().StringVar(&clusterID, "cluster-id", "", "cluster id (required)")
	cmd.Flags().StringVar(&clusterName, "cluster-name", "", "cluster name (required)")
	cmd.Flags().StringVar(&clusterType, "cluster-type", "", "cluster type (required)")
	cmd.Flags().StringVar(&baseURL, "base-url", "", "base URL (required)")
	cmd.Flags().StringVar(&databaseURL, "database-url", "", "database URL (optional)")
	cmd.Flags().StringVar(&periscopeURL, "periscope-url", "", "periscope URL (optional)")
	cmd.Flags().StringVar(&kafkaBrokers, "kafka-brokers", "", "comma-separated Kafka brokers (host:port)")
	cmd.Flags().IntVar(&maxStreams, "max-concurrent-streams", 0, "max concurrent streams")
	cmd.Flags().IntVar(&maxViewers, "max-concurrent-viewers", 0, "max concurrent viewers")
	cmd.Flags().IntVar(&maxBandwidth, "max-bandwidth-mbps", 0, "max bandwidth (Mbps)")
	cmd.Flags().StringVar(&ownerTenantID, "owner-tenant-id", "", "owner tenant id (UUID, optional)")
	cmd.Flags().StringVar(&deploymentModel, "deployment-model", "managed", "deployment model: managed|shared")
	return cmd
}

func newAdminClustersUpdateCmd() *cobra.Command {
	var clusterID string
	var clusterName string
	var baseURL string
	var databaseURL string
	var periscopeURL string
	var kafkaBrokers string
	var maxStreams int
	var maxViewers int
	var maxBandwidth int
	var currentStreams int
	var currentViewers int
	var currentBandwidth int
	var healthStatus string
	var isActive bool
	var ownerTenantID string
	var deploymentModel string
	cmd := &cobra.Command{Use: "update", Short: "Update a cluster", RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(clusterID) == "" {
			return fmt.Errorf("--cluster-id is required")
		}
		if cmd.Flags().Changed("owner-tenant-id") && ownerTenantID != "" {
			if err := validateUUID(ownerTenantID); err != nil {
				return fmt.Errorf("--owner-tenant-id: %w", err)
			}
		}
		if cmd.Flags().Changed("deployment-model") && deploymentModel != "" && deploymentModel != "managed" && deploymentModel != "shared" {
			return fmt.Errorf("--deployment-model must be 'managed' or 'shared'")
		}

		qm, ctxCfg, err := qmGRPCClientFromContext()
		if err != nil {
			return err
		}
		defer func() { _ = qm.Close() }()
		cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if ctxCfg.Auth.JWT != "" {
			cctx = context.WithValue(cctx, ctxkeys.KeyJWTToken, ctxCfg.Auth.JWT)
		}

		req := &pb.UpdateClusterRequest{
			ClusterId: clusterID,
		}
		if v := optionalStringFlag(cmd, "cluster-name", clusterName); v != nil {
			req.ClusterName = v
		}
		if v := optionalStringFlag(cmd, "base-url", baseURL); v != nil {
			req.BaseUrl = v
		}
		if v := optionalStringFlag(cmd, "database-url", databaseURL); v != nil {
			req.DatabaseUrl = v
		}
		if v := optionalStringFlag(cmd, "periscope-url", periscopeURL); v != nil {
			req.PeriscopeUrl = v
		}
		if cmd.Flags().Changed("kafka-brokers") {
			req.KafkaBrokers = parseCommaList(kafkaBrokers)
		}
		if v := optionalInt32Flag(cmd, "max-concurrent-streams", maxStreams); v != nil {
			req.MaxConcurrentStreams = v
		}
		if v := optionalInt32Flag(cmd, "max-concurrent-viewers", maxViewers); v != nil {
			req.MaxConcurrentViewers = v
		}
		if v := optionalInt32Flag(cmd, "max-bandwidth-mbps", maxBandwidth); v != nil {
			req.MaxBandwidthMbps = v
		}
		if v := optionalInt32Flag(cmd, "current-stream-count", currentStreams); v != nil {
			req.CurrentStreamCount = v
		}
		if v := optionalInt32Flag(cmd, "current-viewer-count", currentViewers); v != nil {
			req.CurrentViewerCount = v
		}
		if v := optionalInt32Flag(cmd, "current-bandwidth-mbps", currentBandwidth); v != nil {
			req.CurrentBandwidthMbps = v
		}
		if v := optionalStringFlag(cmd, "health-status", healthStatus); v != nil {
			req.HealthStatus = v
		}
		if v := optionalBoolFlag(cmd, "is-active", isActive); v != nil {
			req.IsActive = v
		}
		if v := optionalStringFlag(cmd, "owner-tenant-id", ownerTenantID); v != nil {
			req.OwnerTenantId = v
		}
		if v := optionalStringFlag(cmd, "deployment-model", deploymentModel); v != nil {
			req.DeploymentModel = v
		}

		resp, err := qm.UpdateCluster(cctx, req)
		if err != nil {
			return err
		}
		if output == "json" {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(resp)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Updated cluster %s (%s)\n", resp.Cluster.ClusterName, resp.Cluster.ClusterId)
		return nil
	}}
	cmd.Flags().StringVar(&clusterID, "cluster-id", "", "cluster id (required)")
	cmd.Flags().StringVar(&clusterName, "cluster-name", "", "cluster name")
	cmd.Flags().StringVar(&baseURL, "base-url", "", "base URL")
	cmd.Flags().StringVar(&databaseURL, "database-url", "", "database URL")
	cmd.Flags().StringVar(&periscopeURL, "periscope-url", "", "periscope URL")
	cmd.Flags().StringVar(&kafkaBrokers, "kafka-brokers", "", "comma-separated Kafka brokers (host:port)")
	cmd.Flags().IntVar(&maxStreams, "max-concurrent-streams", 0, "max concurrent streams")
	cmd.Flags().IntVar(&maxViewers, "max-concurrent-viewers", 0, "max concurrent viewers")
	cmd.Flags().IntVar(&maxBandwidth, "max-bandwidth-mbps", 0, "max bandwidth (Mbps)")
	cmd.Flags().IntVar(&currentStreams, "current-stream-count", 0, "current stream count")
	cmd.Flags().IntVar(&currentViewers, "current-viewer-count", 0, "current viewer count")
	cmd.Flags().IntVar(&currentBandwidth, "current-bandwidth-mbps", 0, "current bandwidth (Mbps)")
	cmd.Flags().StringVar(&healthStatus, "health-status", "", "health status")
	cmd.Flags().BoolVar(&isActive, "is-active", false, "set cluster active flag")
	cmd.Flags().StringVar(&ownerTenantID, "owner-tenant-id", "", "owner tenant id (UUID, empty clears)")
	cmd.Flags().StringVar(&deploymentModel, "deployment-model", "", "deployment model: managed|shared")
	return cmd
}

func newAdminClustersAccessCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "access", Short: "Manage tenant access to clusters"}
	cmd.AddCommand(newAdminClustersAccessListCmd())
	cmd.AddCommand(newAdminClustersAccessGrantCmd())
	return cmd
}

func newAdminClustersAccessListCmd() *cobra.Command {
	var tenantID string
	cmd := &cobra.Command{Use: "list", Short: "List clusters accessible to a tenant", RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(tenantID) == "" {
			return fmt.Errorf("--tenant-id is required")
		}
		if err := validateUUID(tenantID); err != nil {
			return fmt.Errorf("--tenant-id: %w", err)
		}
		qm, ctxCfg, err := qmGRPCClientFromContext()
		if err != nil {
			return err
		}
		defer func() { _ = qm.Close() }()
		cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if ctxCfg.Auth.JWT != "" {
			cctx = context.WithValue(cctx, ctxkeys.KeyJWTToken, ctxCfg.Auth.JWT)
		}
		resp, err := qm.ListClustersForTenant(cctx, tenantID, nil)
		if err != nil {
			return err
		}
		if output == "json" {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(resp)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Accessible clusters (%d)\n", len(resp.Clusters))
		for _, c := range resp.Clusters {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), " - %s (id=%s) access=%s\n", c.ClusterName, c.ClusterId, c.AccessLevel)
		}
		return nil
	}}
	cmd.Flags().StringVar(&tenantID, "tenant-id", "", "tenant id (required)")
	return cmd
}

func newAdminClustersAccessGrantCmd() *cobra.Command {
	var tenantID string
	var clusterID string
	var accessLevel string
	var resourceLimits string
	var expiresAt string
	cmd := &cobra.Command{Use: "grant", Short: "Grant cluster access to a tenant", RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(tenantID) == "" {
			return fmt.Errorf("--tenant-id is required")
		}
		if strings.TrimSpace(clusterID) == "" {
			return fmt.Errorf("--cluster-id is required")
		}
		if err := validateUUID(tenantID); err != nil {
			return fmt.Errorf("--tenant-id: %w", err)
		}
		var expires *timestamppb.Timestamp
		if strings.TrimSpace(expiresAt) != "" {
			t, err := time.Parse(time.RFC3339, expiresAt)
			if err != nil {
				return fmt.Errorf("--expires-at must be RFC3339: %w", err)
			}
			expires = timestamppb.New(t)
		}
		limits, err := parseStructJSON(resourceLimits)
		if err != nil {
			return fmt.Errorf("--resource-limits: %w", err)
		}

		qm, ctxCfg, err := qmGRPCClientFromContext()
		if err != nil {
			return err
		}
		defer func() { _ = qm.Close() }()
		cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if ctxCfg.Auth.JWT != "" {
			cctx = context.WithValue(cctx, ctxkeys.KeyJWTToken, ctxCfg.Auth.JWT)
		}

		req := &pb.GrantClusterAccessRequest{
			TenantId:       tenantID,
			ClusterId:      clusterID,
			AccessLevel:    accessLevel,
			ResourceLimits: limits,
		}
		if expires != nil {
			req.ExpiresAt = expires
		}
		if err := qm.GrantClusterAccess(cctx, req); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Granted access to cluster %s for tenant %s\n", clusterID, tenantID)
		return nil
	}}
	cmd.Flags().StringVar(&tenantID, "tenant-id", "", "tenant id (required)")
	cmd.Flags().StringVar(&clusterID, "cluster-id", "", "cluster id (required)")
	cmd.Flags().StringVar(&accessLevel, "access-level", "", "access level (optional)")
	cmd.Flags().StringVar(&resourceLimits, "resource-limits", "", "resource limits JSON (optional)")
	cmd.Flags().StringVar(&expiresAt, "expires-at", "", "expires at (RFC3339, optional)")
	return cmd
}

func newAdminClustersInvitesCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "invites", Short: "Manage cluster invites"}
	cmd.AddCommand(newAdminClustersInvitesCreateCmd())
	cmd.AddCommand(newAdminClustersInvitesListCmd())
	cmd.AddCommand(newAdminClustersInvitesRevokeCmd())
	cmd.AddCommand(newAdminClustersInvitesListMineCmd())
	cmd.AddCommand(newAdminClustersInvitesAcceptCmd())
	return cmd
}

func newAdminClustersInvitesCreateCmd() *cobra.Command {
	var clusterID string
	var ownerTenantID string
	var invitedTenantID string
	var accessLevel string
	var resourceLimits string
	var expiresInDays int
	cmd := &cobra.Command{Use: "create", Short: "Create a cluster invite", RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(clusterID) == "" {
			return fmt.Errorf("--cluster-id is required")
		}
		if strings.TrimSpace(ownerTenantID) == "" {
			return fmt.Errorf("--owner-tenant-id is required")
		}
		if strings.TrimSpace(invitedTenantID) == "" {
			return fmt.Errorf("--invited-tenant-id is required")
		}
		if err := validateUUID(ownerTenantID); err != nil {
			return fmt.Errorf("--owner-tenant-id: %w", err)
		}
		if err := validateUUID(invitedTenantID); err != nil {
			return fmt.Errorf("--invited-tenant-id: %w", err)
		}
		limits, err := parseStructJSON(resourceLimits)
		if err != nil {
			return fmt.Errorf("--resource-limits: %w", err)
		}

		qm, ctxCfg, err := qmGRPCClientFromContext()
		if err != nil {
			return err
		}
		defer func() { _ = qm.Close() }()
		cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if ctxCfg.Auth.JWT != "" {
			cctx = context.WithValue(cctx, ctxkeys.KeyJWTToken, ctxCfg.Auth.JWT)
		}

		req := &pb.CreateClusterInviteRequest{
			ClusterId:       clusterID,
			OwnerTenantId:   ownerTenantID,
			InvitedTenantId: invitedTenantID,
			AccessLevel:     accessLevel,
			ResourceLimits:  limits,
		}
		if expiresInDays > 0 {
			req.ExpiresInDays = int32(expiresInDays)
		}
		resp, err := qm.CreateClusterInvite(cctx, req)
		if err != nil {
			return err
		}
		if output == "json" {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(resp)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Created invite %s for tenant %s (token=%s)\n", resp.Id, resp.InvitedTenantId, resp.InviteToken)
		return nil
	}}
	cmd.Flags().StringVar(&clusterID, "cluster-id", "", "cluster id (required)")
	cmd.Flags().StringVar(&ownerTenantID, "owner-tenant-id", "", "owner tenant id (required)")
	cmd.Flags().StringVar(&invitedTenantID, "invited-tenant-id", "", "invited tenant id (required)")
	cmd.Flags().StringVar(&accessLevel, "access-level", "", "access level (optional)")
	cmd.Flags().StringVar(&resourceLimits, "resource-limits", "", "resource limits JSON (optional)")
	cmd.Flags().IntVar(&expiresInDays, "expires-in-days", 0, "expires in days (default 30)")
	return cmd
}

func newAdminClustersInvitesListCmd() *cobra.Command {
	var clusterID string
	var ownerTenantID string
	cmd := &cobra.Command{Use: "list", Short: "List invites for a cluster (owner)", RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(clusterID) == "" {
			return fmt.Errorf("--cluster-id is required")
		}
		if strings.TrimSpace(ownerTenantID) == "" {
			return fmt.Errorf("--owner-tenant-id is required")
		}
		if err := validateUUID(ownerTenantID); err != nil {
			return fmt.Errorf("--owner-tenant-id: %w", err)
		}
		qm, ctxCfg, err := qmGRPCClientFromContext()
		if err != nil {
			return err
		}
		defer func() { _ = qm.Close() }()
		cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if ctxCfg.Auth.JWT != "" {
			cctx = context.WithValue(cctx, ctxkeys.KeyJWTToken, ctxCfg.Auth.JWT)
		}

		resp, err := qm.ListClusterInvites(cctx, &pb.ListClusterInvitesRequest{
			ClusterId:     clusterID,
			OwnerTenantId: ownerTenantID,
		})
		if err != nil {
			return err
		}
		if output == "json" {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(resp)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Invites (%d)\n", len(resp.Invites))
		for _, inv := range resp.Invites {
			expires := "-"
			if inv.ExpiresAt != nil {
				expires = inv.ExpiresAt.AsTime().Format(time.RFC3339)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), " - %s tenant=%s status=%s expires=%s\n",
				inv.Id, inv.InvitedTenantId, inv.Status, expires)
		}
		return nil
	}}
	cmd.Flags().StringVar(&clusterID, "cluster-id", "", "cluster id (required)")
	cmd.Flags().StringVar(&ownerTenantID, "owner-tenant-id", "", "owner tenant id (required)")
	return cmd
}

func newAdminClustersInvitesRevokeCmd() *cobra.Command {
	var inviteID string
	var ownerTenantID string
	cmd := &cobra.Command{Use: "revoke", Short: "Revoke an invite (owner)", RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(inviteID) == "" {
			return fmt.Errorf("--invite-id is required")
		}
		if strings.TrimSpace(ownerTenantID) == "" {
			return fmt.Errorf("--owner-tenant-id is required")
		}
		if err := validateUUID(ownerTenantID); err != nil {
			return fmt.Errorf("--owner-tenant-id: %w", err)
		}
		qm, ctxCfg, err := qmGRPCClientFromContext()
		if err != nil {
			return err
		}
		defer func() { _ = qm.Close() }()
		cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if ctxCfg.Auth.JWT != "" {
			cctx = context.WithValue(cctx, ctxkeys.KeyJWTToken, ctxCfg.Auth.JWT)
		}

		if err := qm.RevokeClusterInvite(cctx, &pb.RevokeClusterInviteRequest{
			InviteId:      inviteID,
			OwnerTenantId: ownerTenantID,
		}); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Revoked invite %s\n", inviteID)
		return nil
	}}
	cmd.Flags().StringVar(&inviteID, "invite-id", "", "invite id (required)")
	cmd.Flags().StringVar(&ownerTenantID, "owner-tenant-id", "", "owner tenant id (required)")
	return cmd
}

func newAdminClustersInvitesListMineCmd() *cobra.Command {
	var tenantID string
	cmd := &cobra.Command{Use: "list-mine", Short: "List pending invites for a tenant", RunE: func(cmd *cobra.Command, args []string) error {
		if tenantID != "" {
			if err := validateUUID(tenantID); err != nil {
				return fmt.Errorf("--tenant-id: %w", err)
			}
		}
		qm, ctxCfg, err := qmGRPCClientFromContext()
		if err != nil {
			return err
		}
		defer func() { _ = qm.Close() }()
		cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if ctxCfg.Auth.JWT != "" {
			cctx = context.WithValue(cctx, ctxkeys.KeyJWTToken, ctxCfg.Auth.JWT)
		}

		resp, err := qm.ListMyClusterInvites(cctx, &pb.ListMyClusterInvitesRequest{
			TenantId: tenantID,
		})
		if err != nil {
			return err
		}
		if output == "json" {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(resp)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Invites (%d)\n", len(resp.Invites))
		for _, inv := range resp.Invites {
			expires := "-"
			if inv.ExpiresAt != nil {
				expires = inv.ExpiresAt.AsTime().Format(time.RFC3339)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), " - %s cluster=%s status=%s expires=%s\n",
				inv.Id, inv.ClusterId, inv.Status, expires)
		}
		return nil
	}}
	cmd.Flags().StringVar(&tenantID, "tenant-id", "", "tenant id (optional; uses auth context if omitted)")
	return cmd
}

func newAdminClustersInvitesAcceptCmd() *cobra.Command {
	var tenantID string
	var inviteToken string
	cmd := &cobra.Command{Use: "accept", Short: "Accept a cluster invite", RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(inviteToken) == "" {
			return fmt.Errorf("--invite-token is required")
		}
		if tenantID != "" {
			if err := validateUUID(tenantID); err != nil {
				return fmt.Errorf("--tenant-id: %w", err)
			}
		}
		qm, ctxCfg, err := qmGRPCClientFromContext()
		if err != nil {
			return err
		}
		defer func() { _ = qm.Close() }()
		cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if ctxCfg.Auth.JWT != "" {
			cctx = context.WithValue(cctx, ctxkeys.KeyJWTToken, ctxCfg.Auth.JWT)
		}

		resp, err := qm.AcceptClusterInvite(cctx, &pb.AcceptClusterInviteRequest{
			TenantId:    tenantID,
			InviteToken: inviteToken,
		})
		if err != nil {
			return err
		}
		if output == "json" {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(resp)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Accepted invite: cluster=%s tenant=%s access=%s\n", resp.ClusterId, resp.TenantId, resp.AccessLevel)
		return nil
	}}
	cmd.Flags().StringVar(&tenantID, "tenant-id", "", "tenant id (optional; uses auth context if omitted)")
	cmd.Flags().StringVar(&inviteToken, "invite-token", "", "invite token (required)")
	return cmd
}

func newAdminClustersSubscriptionsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "subscriptions", Short: "Manage cluster subscriptions"}
	cmd.AddCommand(newAdminClustersSubscriptionsListCmd())
	return cmd
}

func newAdminClustersSubscriptionsListCmd() *cobra.Command {
	var tenantID string
	cmd := &cobra.Command{Use: "list", Short: "List cluster subscriptions for a tenant", RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(tenantID) == "" {
			return fmt.Errorf("--tenant-id is required")
		}
		if err := validateUUID(tenantID); err != nil {
			return fmt.Errorf("--tenant-id: %w", err)
		}
		qm, ctxCfg, err := qmGRPCClientFromContext()
		if err != nil {
			return err
		}
		defer func() { _ = qm.Close() }()
		cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if ctxCfg.Auth.JWT != "" {
			cctx = context.WithValue(cctx, ctxkeys.KeyJWTToken, ctxCfg.Auth.JWT)
		}
		resp, err := qm.ListMySubscriptions(cctx, &pb.ListMySubscriptionsRequest{TenantId: tenantID})
		if err != nil {
			return err
		}
		if output == "json" {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(resp)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Subscriptions (%d)\n", len(resp.Clusters))
		for _, c := range resp.Clusters {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), " - %s (id=%s) type=%s\n", c.ClusterName, c.ClusterId, c.ClusterType)
		}
		return nil
	}}
	cmd.Flags().StringVar(&tenantID, "tenant-id", "", "tenant id (required)")
	return cmd
}

// === Quartermaster Nodes ===

func newAdminNodesCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "nodes", Short: "Manage nodes"}
	cmd.AddCommand(newAdminNodesListCmd())
	cmd.AddCommand(newAdminNodesCreateCmd())
	cmd.AddCommand(newAdminNodesHardwareCmd())
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
		defer func() { _ = qm.Close() }()
		cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if ctxCfg.Auth.JWT != "" {
			cctx = context.WithValue(cctx, ctxkeys.KeyJWTToken, ctxCfg.Auth.JWT)
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
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Nodes (%d)\n", len(resp.Nodes))
		for _, n := range resp.Nodes {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), " - %s (id=%s) type=%s cluster=%s\n", n.NodeName, n.NodeId, n.NodeType, n.ClusterId)
		}
		return nil
	}}
	cmd.Flags().StringVar(&clusterID, "cluster-id", "", "filter by cluster ID")
	cmd.Flags().StringVar(&nodeType, "type", "", "filter by node type (edge, api, app, website, docs, forms, etc.)")
	cmd.Flags().StringVar(&region, "region", "", "filter by region")
	return cmd
}

func newAdminNodesCreateCmd() *cobra.Command {
	var nodeID string
	var clusterID string
	var nodeName string
	var nodeType string
	var internalIP string
	var externalIP string
	var wireguardIP string
	var wireguardKey string
	var region string
	var availabilityZone string
	var latitude float64
	var longitude float64
	var cpuCores int
	var memoryGB int
	var diskGB int
	var tags string
	var metadata string
	cmd := &cobra.Command{Use: "create", Short: "Create a node", RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(clusterID) == "" {
			return fmt.Errorf("--cluster-id is required")
		}
		if strings.TrimSpace(nodeName) == "" {
			return fmt.Errorf("--node-name is required")
		}
		if strings.TrimSpace(nodeType) == "" {
			return fmt.Errorf("--node-type is required")
		}
		if internalIP != "" {
			if err := validateIP(internalIP); err != nil {
				return fmt.Errorf("--internal-ip: %w", err)
			}
		}
		if externalIP != "" {
			if err := validateIP(externalIP); err != nil {
				return fmt.Errorf("--external-ip: %w", err)
			}
		}
		if wireguardIP != "" {
			if err := validateIP(wireguardIP); err != nil {
				return fmt.Errorf("--wireguard-ip: %w", err)
			}
		}

		if nodeID == "" {
			nodeID = nodeName
		}
		if nodeID == "" {
			nodeID = uuid.New().String()
		}

		tagsStruct, err := parseStructJSON(tags)
		if err != nil {
			return fmt.Errorf("--tags: %w", err)
		}
		metaStruct, err := parseStructJSON(metadata)
		if err != nil {
			return fmt.Errorf("--metadata: %w", err)
		}

		qm, ctxCfg, err := qmGRPCClientFromContext()
		if err != nil {
			return err
		}
		defer func() { _ = qm.Close() }()
		cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if ctxCfg.Auth.JWT != "" {
			cctx = context.WithValue(cctx, ctxkeys.KeyJWTToken, ctxCfg.Auth.JWT)
		}

		req := &pb.CreateNodeRequest{
			NodeId:    nodeID,
			ClusterId: clusterID,
			NodeName:  nodeName,
			NodeType:  nodeType,
			Tags:      tagsStruct,
			Metadata:  metaStruct,
		}
		if internalIP != "" {
			req.InternalIp = &internalIP
		}
		if externalIP != "" {
			req.ExternalIp = &externalIP
		}
		if wireguardIP != "" {
			req.WireguardIp = &wireguardIP
		}
		if wireguardKey != "" {
			req.WireguardPublicKey = &wireguardKey
		}
		if region != "" {
			req.Region = &region
		}
		if availabilityZone != "" {
			req.AvailabilityZone = &availabilityZone
		}
		if v := optionalFloat64Flag(cmd, "latitude", latitude); v != nil {
			req.Latitude = v
		}
		if v := optionalFloat64Flag(cmd, "longitude", longitude); v != nil {
			req.Longitude = v
		}
		if v := optionalInt32Flag(cmd, "cpu-cores", cpuCores); v != nil {
			req.CpuCores = v
		}
		if v := optionalInt32Flag(cmd, "memory-gb", memoryGB); v != nil {
			req.MemoryGb = v
		}
		if v := optionalInt32Flag(cmd, "disk-gb", diskGB); v != nil {
			req.DiskGb = v
		}

		resp, err := qm.CreateNode(cctx, req)
		if err != nil {
			return err
		}
		if output == "json" {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(resp)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Created node %s (id=%s)\n", resp.Node.NodeName, resp.Node.NodeId)
		return nil
	}}
	cmd.Flags().StringVar(&nodeID, "node-id", "", "node id (defaults to node-name)")
	cmd.Flags().StringVar(&clusterID, "cluster-id", "", "cluster id (required)")
	cmd.Flags().StringVar(&nodeName, "node-name", "", "node name (required)")
	cmd.Flags().StringVar(&nodeType, "node-type", "", "node type (required)")
	cmd.Flags().StringVar(&internalIP, "internal-ip", "", "internal IP (optional)")
	cmd.Flags().StringVar(&externalIP, "external-ip", "", "external IP (optional)")
	cmd.Flags().StringVar(&wireguardIP, "wireguard-ip", "", "wireguard IP (optional)")
	cmd.Flags().StringVar(&wireguardKey, "wireguard-public-key", "", "wireguard public key (optional)")
	cmd.Flags().StringVar(&region, "region", "", "region (optional)")
	cmd.Flags().StringVar(&availabilityZone, "availability-zone", "", "availability zone (optional)")
	cmd.Flags().Float64Var(&latitude, "latitude", 0, "latitude (optional)")
	cmd.Flags().Float64Var(&longitude, "longitude", 0, "longitude (optional)")
	cmd.Flags().IntVar(&cpuCores, "cpu-cores", 0, "CPU cores (optional)")
	cmd.Flags().IntVar(&memoryGB, "memory-gb", 0, "memory GB (optional)")
	cmd.Flags().IntVar(&diskGB, "disk-gb", 0, "disk GB (optional)")
	cmd.Flags().StringVar(&tags, "tags", "", "tags JSON (optional)")
	cmd.Flags().StringVar(&metadata, "metadata", "", "metadata JSON (optional)")
	return cmd
}

func newAdminNodesHardwareCmd() *cobra.Command {
	var nodeID string
	var cpuCores int
	var memoryGB int
	var diskGB int
	cmd := &cobra.Command{Use: "hardware", Short: "Update node hardware specs", RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(nodeID) == "" {
			return fmt.Errorf("--node-id is required")
		}
		qm, ctxCfg, err := qmGRPCClientFromContext()
		if err != nil {
			return err
		}
		defer func() { _ = qm.Close() }()
		cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if ctxCfg.Auth.JWT != "" {
			cctx = context.WithValue(cctx, ctxkeys.KeyJWTToken, ctxCfg.Auth.JWT)
		}
		req := &pb.UpdateNodeHardwareRequest{
			NodeId: nodeID,
		}
		if v := optionalInt32Flag(cmd, "cpu-cores", cpuCores); v != nil {
			req.CpuCores = v
		}
		if v := optionalInt32Flag(cmd, "memory-gb", memoryGB); v != nil {
			req.MemoryGb = v
		}
		if v := optionalInt32Flag(cmd, "disk-gb", diskGB); v != nil {
			req.DiskGb = v
		}
		if err := qm.UpdateNodeHardware(cctx, req); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Updated node hardware for %s\n", nodeID)
		return nil
	}}
	cmd.Flags().StringVar(&nodeID, "node-id", "", "node id (required)")
	cmd.Flags().IntVar(&cpuCores, "cpu-cores", 0, "CPU cores (optional)")
	cmd.Flags().IntVar(&memoryGB, "memory-gb", 0, "memory GB (optional)")
	cmd.Flags().IntVar(&diskGB, "disk-gb", 0, "disk GB (optional)")
	return cmd
}
