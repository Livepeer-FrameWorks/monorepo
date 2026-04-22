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
	fwcredentials "frameworks/cli/internal/credentials"
	"frameworks/cli/internal/platformauth"
	"frameworks/cli/internal/ux"
	commodore "frameworks/pkg/clients/commodore"
	fhclient "frameworks/pkg/clients/foghorn"
	purserclient "frameworks/pkg/clients/purser"
	qmclient "frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"golang.org/x/term"
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
	adm.AddCommand(newAdminFoghornPoolCmd())
	adm.AddCommand(newAdminBillingCmd())
	adm.AddCommand(newAdminUsersCmd())
	return adm
}

func newAdminTokensCmd() *cobra.Command {
	tok := &cobra.Command{Use: "tokens", Short: "Manage API tokens (developer)"}
	tok.AddCommand(newAdminTokensCreateCmd())
	tok.AddCommand(newAdminTokensListCmd())
	tok.AddCommand(newAdminTokensRevokeCmd())
	return tok
}

// activeContextWithAuth is the shared entry point for admin gRPC client
// factories. It loads config, resolves the active context strictly
// (errors when none configured), fills JWT from the credential store
// (user identity), and resolves SERVICE_TOKEN from the active context's
// manifest/gitops source (platform configuration). Service tokens are
// never read from the credential store or env vars.
func activeContextWithAuth() (fwcfg.Context, error) {
	cfg, err := fwcfg.Load()
	if err != nil {
		return fwcfg.Context{}, err
	}
	rt := fwcfg.GetRuntimeOverrides()
	ctxCfg, err := fwcfg.ResolveActiveContext(rt, fwcfg.OSEnv{}, cfg)
	if err != nil {
		return fwcfg.Context{}, err
	}
	jwt, err := fwcredentials.ResolveUserAuth(fwcfg.OSEnv{}, fwcredentials.DefaultStore())
	if err != nil {
		return fwcfg.Context{}, err
	}
	token, err := platformauth.ResolveManifestServiceToken(context.Background(), ctxCfg, cfg)
	if err != nil {
		return fwcfg.Context{}, err
	}
	ctxCfg.Auth = fwcfg.Auth{JWT: jwt, ServiceToken: token}
	return ctxCfg, nil
}

func commodoreGRPCClientFromContext() (*commodore.GRPCClient, fwcfg.Context, error) {
	ctxCfg, err := activeContextWithAuth()
	if err != nil {
		return nil, fwcfg.Context{}, err
	}

	grpcAddr, err := fwcfg.RequireEndpoint(ctxCfg, "commodore_grpc_addr", ctxCfg.Endpoints.CommodoreGRPCAddr, false)
	if err != nil {
		return nil, fwcfg.Context{}, err
	}

	cli, err := commodore.NewGRPCClient(commodore.GRPCConfig{
		GRPCAddr:      grpcAddr,
		Timeout:       15 * time.Second,
		Logger:        logging.NewLogger(),
		ServiceToken:  ctxCfg.Auth.ServiceToken,
		AllowInsecure: true,
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
		ux.Success(cmd.OutOrStdout(), fmt.Sprintf("Created token %q (id=%s)", resp.TokenName, resp.Id))
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
		ux.Heading(cmd.OutOrStdout(), fmt.Sprintf("Tokens (%d)", len(resp.Tokens)))
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
		ux.Success(cmd.OutOrStdout(), fmt.Sprintf("Revoked token %s", tokenID))
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
	ctxCfg, err := activeContextWithAuth()
	if err != nil {
		return nil, fwcfg.Context{}, err
	}

	grpcAddr, err := fwcfg.RequireEndpoint(ctxCfg, "quartermaster_grpc_addr", ctxCfg.Endpoints.QuartermasterGRPCAddr, false)
	if err != nil {
		return nil, fwcfg.Context{}, err
	}

	qm, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{
		GRPCAddr:      grpcAddr,
		Timeout:       15 * time.Second,
		Logger:        logging.NewLogger(),
		ServiceToken:  ctxCfg.Auth.ServiceToken,
		AllowInsecure: true,
	})
	if err != nil {
		return nil, fwcfg.Context{}, fmt.Errorf("failed to connect to Quartermaster gRPC: %w", err)
	}
	return qm, ctxCfg, nil
}

func foghornGRPCClientFromContext() (*fhclient.GRPCClient, fwcfg.Context, error) {
	ctxCfg, err := activeContextWithAuth()
	if err != nil {
		return nil, fwcfg.Context{}, err
	}

	grpcAddr, err := fwcfg.RequireEndpoint(ctxCfg, "foghorn_grpc_addr", ctxCfg.Endpoints.FoghornGRPCAddr, false)
	if err != nil {
		return nil, fwcfg.Context{}, err
	}

	fh, err := fhclient.NewGRPCClient(fhclient.GRPCConfig{
		GRPCAddr:     grpcAddr,
		Timeout:      30 * time.Second,
		Logger:       logging.NewLogger(),
		ServiceToken: ctxCfg.Auth.ServiceToken,
	})
	if err != nil {
		return nil, fwcfg.Context{}, fmt.Errorf("failed to connect to Foghorn gRPC: %w", err)
	}
	return fh, ctxCfg, nil
}

func purserGRPCClientFromContext() (*purserclient.GRPCClient, fwcfg.Context, error) {
	ctxCfg, err := activeContextWithAuth()
	if err != nil {
		return nil, fwcfg.Context{}, err
	}

	grpcAddr, err := fwcfg.RequireEndpoint(ctxCfg, "purser_grpc_addr", ctxCfg.Endpoints.PurserGRPCAddr, false)
	if err != nil {
		return nil, fwcfg.Context{}, err
	}

	p, err := purserclient.NewGRPCClient(purserclient.GRPCConfig{
		GRPCAddr:      grpcAddr,
		Timeout:       15 * time.Second,
		Logger:        logging.NewLogger(),
		ServiceToken:  ctxCfg.Auth.ServiceToken,
		AllowInsecure: true,
	})
	if err != nil {
		return nil, fwcfg.Context{}, fmt.Errorf("failed to connect to Purser gRPC: %w", err)
	}
	return p, ctxCfg, nil
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
		out := cmd.OutOrStdout()
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
		// Fill tenant/cluster from active context when not specified.
		if strings.TrimSpace(tenantID) == "" {
			if ctxTenant := tenantIDFromContext(); ctxTenant != "" {
				tenantID = ctxTenant
				ux.ContextNotice(out, "tenant", tenantID)
			}
		}
		if strings.TrimSpace(clusterID) == "" {
			if ctxCluster := clusterIDFromContext(); ctxCluster != "" {
				clusterID = ctxCluster
				ux.ContextNotice(out, "cluster", clusterID)
			}
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
			enc := json.NewEncoder(out)
			enc.SetIndent("", "  ")
			return enc.Encode(resp)
		}
		ux.Result(out, []ux.ResultField{
			{Key: "token", OK: true, Detail: resp.Token.Token},
			{Key: "kind", OK: true, Detail: resp.Token.Kind},
			{Key: "expires", OK: true, Detail: resp.Token.ExpiresAt.AsTime().Format(time.RFC3339)},
		})
		if kind == "edge_node" {
			ux.PrintNextSteps(out, []ux.NextStep{
				{Cmd: fmt.Sprintf("frameworks edge deploy --ssh <user>@<host> --enrollment-token %s", resp.Token.Token), Why: "Use this token to enroll the target host."},
			})
		}
		return nil
	}}
	cmd.Flags().StringVar(&kind, "kind", "edge_node", "token kind: edge_node|service|infrastructure_node")
	cmd.Flags().StringVar(&tenantID, "tenant-id", "", "tenant id (defaults to active context's SystemTenantID)")
	cmd.Flags().StringVar(&clusterID, "cluster-id", "", "cluster id (defaults to active context's ClusterID)")
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
		ux.Heading(cmd.OutOrStdout(), fmt.Sprintf("Bootstrap tokens (%d)", len(resp.Tokens)))
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
		ux.Success(cmd.OutOrStdout(), fmt.Sprintf("Revoked bootstrap token %s", tokenID))
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
		ux.Heading(cmd.OutOrStdout(), fmt.Sprintf("Tenants (%d)", len(resp.Tenants)))
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
	cmd.AddCommand(newAdminClustersCertStatusCmd())
	cmd.AddCommand(newAdminClustersMigrateArtifactsCmd())
	cmd.AddCommand(newAdminClustersCreateEdgeCmd())
	cmd.AddCommand(newAdminClustersEnrollmentTokenCmd())
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
		ux.Heading(cmd.OutOrStdout(), fmt.Sprintf("Clusters (%d)", len(resp.Clusters)))
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
	var foghornCount int
	var isPlatformOfficial bool
	var isDefaultCluster bool
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
		if foghornCount > 0 {
			req.FoghornCount = int32(foghornCount)
		}
		if cmd.Flags().Changed("is-platform-official") {
			req.IsPlatformOfficial = &isPlatformOfficial
		}
		if cmd.Flags().Changed("is-default-cluster") {
			req.IsDefaultCluster = &isDefaultCluster
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
		ux.Success(cmd.OutOrStdout(), fmt.Sprintf("Created cluster %s (%s)", resp.Cluster.ClusterName, resp.Cluster.ClusterId))
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
	cmd.Flags().IntVar(&foghornCount, "foghorn-count", 0, "claim N idle Foghorn instances from pool (0 = skip)")
	cmd.Flags().BoolVar(&isPlatformOfficial, "is-platform-official", false, "mark as platform-operated cluster")
	cmd.Flags().BoolVar(&isDefaultCluster, "is-default-cluster", false, "mark as default cluster for new tenant auto-subscription")
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
	var healthStatus string
	var isActive bool
	var ownerTenantID string
	var deploymentModel string
	var isPlatformOfficial bool
	var isDefaultCluster bool
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
		if v := optionalBoolFlag(cmd, "is-platform-official", isPlatformOfficial); v != nil {
			req.IsPlatformOfficial = v
		}
		if v := optionalBoolFlag(cmd, "is-default-cluster", isDefaultCluster); v != nil {
			req.IsDefaultCluster = v
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
		ux.Success(cmd.OutOrStdout(), fmt.Sprintf("Updated cluster %s (%s)", resp.Cluster.ClusterName, resp.Cluster.ClusterId))
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
	cmd.Flags().StringVar(&healthStatus, "health-status", "", "health status")
	cmd.Flags().BoolVar(&isActive, "is-active", false, "set cluster active flag")
	cmd.Flags().StringVar(&ownerTenantID, "owner-tenant-id", "", "owner tenant id (UUID, empty clears)")
	cmd.Flags().StringVar(&deploymentModel, "deployment-model", "", "deployment model: managed|shared")
	cmd.Flags().BoolVar(&isPlatformOfficial, "is-platform-official", false, "set platform-operated cluster flag")
	cmd.Flags().BoolVar(&isDefaultCluster, "is-default-cluster", false, "set default cluster for new tenant auto-subscription")
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
		ux.Heading(cmd.OutOrStdout(), fmt.Sprintf("Accessible clusters (%d)", len(resp.Clusters)))
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
		ux.Success(cmd.OutOrStdout(), fmt.Sprintf("Granted access to cluster %s for tenant %s", clusterID, tenantID))
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
		ux.Success(cmd.OutOrStdout(), fmt.Sprintf("Created invite %s for tenant %s (token=%s)", resp.Id, resp.InvitedTenantId, resp.InviteToken))
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
		ux.Heading(cmd.OutOrStdout(), fmt.Sprintf("Invites (%d)", len(resp.Invites)))
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
		ux.Success(cmd.OutOrStdout(), fmt.Sprintf("Revoked invite %s", inviteID))
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
		ux.Heading(cmd.OutOrStdout(), fmt.Sprintf("Invites (%d)", len(resp.Invites)))
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
		out := cmd.OutOrStdout()
		if output == "json" {
			enc := json.NewEncoder(out)
			enc.SetIndent("", "  ")
			return enc.Encode(resp)
		}
		ux.Result(out, []ux.ResultField{
			{Key: "cluster", OK: true, Detail: resp.ClusterId},
			{Key: "tenant", OK: true, Detail: resp.TenantId},
			{Key: "access", OK: true, Detail: resp.AccessLevel},
		})
		ux.PrintNextSteps(out, []ux.NextStep{
			{Cmd: fmt.Sprintf("frameworks admin clusters subscriptions list --tenant-id %s", resp.TenantId), Why: "Verify your tenant's subscriptions on this cluster."},
		})
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
		ux.Heading(cmd.OutOrStdout(), fmt.Sprintf("Subscriptions (%d)", len(resp.Clusters)))
		for _, c := range resp.Clusters {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), " - %s (id=%s) type=%s\n", c.ClusterName, c.ClusterId, c.ClusterType)
		}
		return nil
	}}
	cmd.Flags().StringVar(&tenantID, "tenant-id", "", "tenant id (required)")
	return cmd
}

// === Cluster Cert Status ===

func newAdminClustersCertStatusCmd() *cobra.Command {
	var clusterID string
	cmd := &cobra.Command{Use: "cert-status", Short: "Show cluster readiness (health, Foghorn, cert)", RunE: func(cmd *cobra.Command, args []string) error {
		out := cmd.OutOrStdout()
		if strings.TrimSpace(clusterID) == "" {
			if ctxCluster := clusterIDFromContext(); ctxCluster != "" {
				clusterID = ctxCluster
				ux.ContextNotice(out, "cluster", clusterID)
			}
		}
		if strings.TrimSpace(clusterID) == "" {
			return fmt.Errorf("--cluster-id is required")
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

		cluster, err := qm.GetCluster(cctx, clusterID)
		if err != nil {
			return fmt.Errorf("get cluster: %w", err)
		}

		foghorns, err := qm.DiscoverServices(cctx, "foghorn", clusterID, nil)
		if err != nil {
			return fmt.Errorf("discover foghorns: %w", err)
		}

		c := cluster.Cluster
		w := cmd.OutOrStdout()
		_, _ = fmt.Fprintf(w, "Cluster: %s (%s)\n", c.ClusterName, c.ClusterId)
		_, _ = fmt.Fprintf(w, "  type:       %s\n", c.ClusterType)
		_, _ = fmt.Fprintf(w, "  base_url:   %s\n", c.BaseUrl)
		_, _ = fmt.Fprintf(w, "  health:     %s\n", c.HealthStatus)
		_, _ = fmt.Fprintf(w, "  active:     %v\n", c.IsActive)

		_, _ = fmt.Fprintf(w, "  foghorns:   %d registered\n", len(foghorns.Instances))
		for _, inst := range foghorns.Instances {
			_, _ = fmt.Fprintf(w, "    - %s:%d  status=%s\n", inst.GetHost(), inst.GetPort(), inst.GetStatus())
		}

		// Wildcard cert: *.{cluster_slug}.{base_url}
		// Cert readiness correlates with cluster health_status = 'active'
		if c.HealthStatus == "active" {
			_, _ = fmt.Fprintf(w, "  cert:       ready (cluster active)\n")
		} else {
			_, _ = fmt.Fprintf(w, "  cert:       pending (cluster %s)\n", c.HealthStatus)
		}
		return nil
	}}
	cmd.Flags().StringVar(&clusterID, "cluster-id", "", "cluster ID (required)")
	return cmd
}

func newAdminClustersCreateEdgeCmd() *cobra.Command {
	var tenantID string
	var clusterName string
	var shortDesc string
	cmd := &cobra.Command{Use: "create-edge", Short: "Create an edge cluster with Foghorn assignment and enrollment token", RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(clusterName) == "" {
			return fmt.Errorf("--cluster-name is required")
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
		cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if ctxCfg.Auth.JWT != "" {
			cctx = context.WithValue(cctx, ctxkeys.KeyJWTToken, ctxCfg.Auth.JWT)
		}

		req := &pb.EnableSelfHostingRequest{
			TenantId:    tenantID,
			ClusterName: clusterName,
		}
		if shortDesc != "" {
			req.ShortDescription = &shortDesc
		}
		resp, err := qm.EnableSelfHosting(cctx, req)
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		if output == "json" {
			enc := json.NewEncoder(out)
			enc.SetIndent("", "  ")
			return enc.Encode(resp)
		}
		c := resp.Cluster
		t := resp.BootstrapToken
		ux.Result(out, []ux.ResultField{
			{Key: "cluster", OK: true, Detail: fmt.Sprintf("%s (id=%s)", c.ClusterName, c.ClusterId)},
			{Key: "base url", OK: true, Detail: c.BaseUrl},
			{Key: "foghorn", OK: true, Detail: resp.FoghornAddr},
			{Key: "token", OK: true, Detail: t.Token},
			{Key: "expires", OK: true, Detail: t.ExpiresAt.AsTime().Format(time.RFC3339)},
		})
		ux.PrintNextSteps(out, []ux.NextStep{
			{Cmd: fmt.Sprintf("frameworks edge deploy --ssh <user>@<host> --enrollment-token %s", t.Token), Why: "Enroll an edge node against this new cluster."},
		})
		return nil
	}}
	cmd.Flags().StringVar(&tenantID, "tenant-id", "", "tenant id (defaults to JWT tenant)")
	cmd.Flags().StringVar(&clusterName, "cluster-name", "", "display name for the cluster (required)")
	cmd.Flags().StringVar(&shortDesc, "description", "", "short description (optional)")
	return cmd
}

func newAdminClustersEnrollmentTokenCmd() *cobra.Command {
	var clusterID string
	var tenantID string
	var name string
	var ttl string
	cmd := &cobra.Command{Use: "enrollment-token", Short: "Create an enrollment token for a cluster", RunE: func(cmd *cobra.Command, args []string) error {
		out := cmd.OutOrStdout()
		if strings.TrimSpace(clusterID) == "" {
			if ctxCluster := clusterIDFromContext(); ctxCluster != "" {
				clusterID = ctxCluster
				ux.ContextNotice(out, "cluster", clusterID)
			}
		}
		if strings.TrimSpace(clusterID) == "" {
			return fmt.Errorf("--cluster-id is required (no ClusterID in context; pass --cluster-id)")
		}
		if strings.TrimSpace(tenantID) == "" {
			if ctxTenant := tenantIDFromContext(); ctxTenant != "" {
				tenantID = ctxTenant
				ux.ContextNotice(out, "tenant", tenantID)
			}
		}
		if tenantID != "" {
			if err := validateUUID(tenantID); err != nil {
				return fmt.Errorf("--tenant-id: %w", err)
			}
		}
		if ttl != "" {
			if _, err := normalizeDuration(ttl); err != nil {
				return fmt.Errorf("--ttl: %w", err)
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

		req := &pb.CreateEnrollmentTokenRequest{
			ClusterId: clusterID,
		}
		if tenantID != "" {
			req.TenantId = &tenantID
		}
		if name != "" {
			req.Name = &name
		}
		if ttl != "" {
			normalized, _ := normalizeDuration(ttl)
			req.Ttl = &normalized
		}
		resp, err := qm.CreateEnrollmentToken(cctx, req)
		if err != nil {
			return err
		}
		if output == "json" {
			enc := json.NewEncoder(out)
			enc.SetIndent("", "  ")
			return enc.Encode(resp)
		}
		ux.Result(out, []ux.ResultField{
			{Key: "token", OK: true, Detail: resp.Token.Token},
			{Key: "cluster", OK: true, Detail: clusterID},
			{Key: "expires", OK: true, Detail: resp.Token.ExpiresAt.AsTime().Format(time.RFC3339)},
		})
		ux.PrintNextSteps(out, []ux.NextStep{
			{Cmd: fmt.Sprintf("frameworks edge deploy --ssh <user>@<host> --enrollment-token %s", resp.Token.Token), Why: "Enroll an edge node with this token."},
		})
		return nil
	}}
	cmd.Flags().StringVar(&clusterID, "cluster-id", "", "cluster id (defaults to active context's ClusterID)")
	cmd.Flags().StringVar(&tenantID, "tenant-id", "", "tenant id (defaults to active context's SystemTenantID)")
	cmd.Flags().StringVar(&name, "name", "", "token display name")
	cmd.Flags().StringVar(&ttl, "ttl", "", "time-to-live (e.g. 24h, 7d; default 30d)")
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
		ux.Heading(cmd.OutOrStdout(), fmt.Sprintf("Nodes (%d)", len(resp.Nodes)))
		for _, n := range resp.Nodes {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), " - %s (id=%s) type=%s cluster=%s\n", n.NodeName, n.NodeId, n.NodeType, n.ClusterId)
		}
		return nil
	}}
	cmd.Flags().StringVar(&clusterID, "cluster-id", "", "filter by cluster ID")
	cmd.Flags().StringVar(&nodeType, "type", "", "filter by node type (edge, bridge, chartroom, foredeck, logbook, steward, etc.)")
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
		ux.Success(cmd.OutOrStdout(), fmt.Sprintf("Created node %s (id=%s)", resp.Node.NodeName, resp.Node.NodeId))
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
	cmd.Flags().IntVar(&cpuCores, "cpu-cores", 0, "CPU cores (optional)")
	cmd.Flags().IntVar(&memoryGB, "memory-gb", 0, "memory GB (optional)")
	cmd.Flags().IntVar(&diskGB, "disk-gb", 0, "disk GB (optional)")
	cmd.Flags().StringVar(&tags, "tags", "", "tags JSON (optional)")
	cmd.Flags().StringVar(&metadata, "metadata", "", "metadata JSON (optional)")
	return cmd
}

// === Foghorn Pool ===

func newAdminFoghornPoolCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "foghorn-pool", Short: "Foghorn instance pool operations"}
	cmd.AddCommand(newAdminFoghornPoolStatusCmd())
	cmd.AddCommand(newAdminFoghornPoolAddCmd())
	cmd.AddCommand(newAdminFoghornPoolDrainCmd())
	cmd.AddCommand(newAdminFoghornPoolAssignCmd())
	cmd.AddCommand(newAdminFoghornPoolUnassignCmd())
	return cmd
}

func newAdminFoghornPoolStatusCmd() *cobra.Command {
	return &cobra.Command{Use: "status", Short: "Show Foghorn pool status", RunE: func(cmd *cobra.Command, args []string) error {
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

		resp, err := qm.GetFoghornPoolStatus(cctx)
		if err != nil {
			return err
		}
		if output == "json" {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(resp)
		}

		ux.Heading(cmd.OutOrStdout(), fmt.Sprintf("Foghorn pool: %d total, %d assigned, %d unassigned", resp.Total, resp.Assigned, resp.Unassigned))
		for _, c := range resp.Clusters {
			cid := c.ClusterId
			if cid == "" {
				cid = "(pool)"
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\n  %s (%d instances)\n", cid, c.InstanceCount)
			for _, inst := range c.Instances {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "    - %s:%d  status=%s  id=%s\n", inst.GetHost(), inst.GetPort(), inst.GetStatus(), inst.GetId())
			}
		}

		if len(resp.Assignments) > 0 {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\n  Assignments (many-to-many):\n")
			for _, a := range resp.Assignments {
				active := "active"
				if !a.IsActive {
					active = "inactive"
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "    foghorn=%s → cluster=%s  %s\n", a.FoghornInstanceId, a.ClusterId, active)
			}
		}
		return nil
	}}
}

func newAdminFoghornPoolAddCmd() *cobra.Command {
	var count int
	var fromCluster string
	var instanceIDs []string
	cmd := &cobra.Command{Use: "add", Short: "Release Foghorn instances back to pool", RunE: func(cmd *cobra.Command, args []string) error {
		if len(instanceIDs) == 0 && (count == 0 || fromCluster == "") {
			return fmt.Errorf("provide --instance-id or (--count + --from-cluster)")
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

		resp, err := qm.AddToFoghornPool(cctx, &pb.AddToFoghornPoolRequest{
			InstanceIds:   instanceIDs,
			Count:         int32(count),
			FromClusterId: fromCluster,
		})
		if err != nil {
			return err
		}
		ux.Success(cmd.OutOrStdout(), fmt.Sprintf("Released %d instance(s) to pool", resp.Released))
		return nil
	}}
	cmd.Flags().StringSliceVar(&instanceIDs, "instance-id", nil, "instance UUID(s) to release")
	cmd.Flags().IntVar(&count, "count", 0, "number of instances to release from cluster")
	cmd.Flags().StringVar(&fromCluster, "from-cluster", "", "cluster to release instances from")
	return cmd
}

func newAdminFoghornPoolDrainCmd() *cobra.Command {
	var instanceID string
	cmd := &cobra.Command{Use: "drain", Short: "Drain a Foghorn instance from its cluster", RunE: func(cmd *cobra.Command, args []string) error {
		if instanceID == "" {
			return fmt.Errorf("--instance-id is required")
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

		resp, err := qm.DrainFoghornInstance(cctx, instanceID)
		if err != nil {
			return err
		}
		prev := resp.PreviousClusterId
		if prev == "" || prev == "pool" {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Instance %s was already in pool\n", instanceID)
		} else {
			ux.Success(cmd.OutOrStdout(), fmt.Sprintf("Drained instance %s from cluster %s → pool", instanceID, prev))
		}
		return nil
	}}
	cmd.Flags().StringVar(&instanceID, "instance-id", "", "instance UUID to drain (required)")
	return cmd
}

func newAdminFoghornPoolAssignCmd() *cobra.Command {
	var clusterID string
	var instanceIDs []string
	var count int
	cmd := &cobra.Command{Use: "assign", Short: "Assign Foghorn instance(s) to a cluster", RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(clusterID) == "" {
			return fmt.Errorf("--cluster-id is required")
		}
		if len(instanceIDs) == 0 && count == 0 {
			return fmt.Errorf("provide --instance-id or --count")
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

		if err := qm.AssignFoghornToCluster(cctx, &pb.AssignFoghornToClusterRequest{
			ClusterId:          clusterID,
			FoghornInstanceIds: instanceIDs,
			Count:              int32(count),
		}); err != nil {
			return err
		}
		if len(instanceIDs) > 0 {
			ux.Success(cmd.OutOrStdout(), fmt.Sprintf("Assigned %d Foghorn instance(s) to cluster %s", len(instanceIDs), clusterID))
		} else {
			ux.Success(cmd.OutOrStdout(), fmt.Sprintf("Assigned %d Foghorn instance(s) to cluster %s (least-loaded)", count, clusterID))
		}
		return nil
	}}
	cmd.Flags().StringVar(&clusterID, "cluster-id", "", "cluster to assign Foghorn(s) to (required)")
	cmd.Flags().StringSliceVar(&instanceIDs, "instance-id", nil, "specific Foghorn instance UUID(s)")
	cmd.Flags().IntVar(&count, "count", 0, "pick N least-loaded Foghorn instances")
	return cmd
}

func newAdminFoghornPoolUnassignCmd() *cobra.Command {
	var clusterID string
	var instanceIDs []string
	cmd := &cobra.Command{Use: "unassign", Short: "Remove Foghorn instance(s) from a cluster", RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(clusterID) == "" {
			return fmt.Errorf("--cluster-id is required")
		}
		if len(instanceIDs) == 0 {
			return fmt.Errorf("--instance-id is required")
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

		if err := qm.UnassignFoghornFromCluster(cctx, &pb.UnassignFoghornFromClusterRequest{
			ClusterId:          clusterID,
			FoghornInstanceIds: instanceIDs,
		}); err != nil {
			return err
		}
		ux.Success(cmd.OutOrStdout(), fmt.Sprintf("Unassigned %d Foghorn instance(s) from cluster %s", len(instanceIDs), clusterID))
		return nil
	}}
	cmd.Flags().StringVar(&clusterID, "cluster-id", "", "cluster to unassign from (required)")
	cmd.Flags().StringSliceVar(&instanceIDs, "instance-id", nil, "Foghorn instance UUID(s) to remove (required)")
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
		ux.Success(cmd.OutOrStdout(), fmt.Sprintf("Updated node hardware for %s", nodeID))
		return nil
	}}
	cmd.Flags().StringVar(&nodeID, "node-id", "", "node id (required)")
	cmd.Flags().IntVar(&cpuCores, "cpu-cores", 0, "CPU cores (optional)")
	cmd.Flags().IntVar(&memoryGB, "memory-gb", 0, "memory GB (optional)")
	cmd.Flags().IntVar(&diskGB, "disk-gb", 0, "disk GB (optional)")
	return cmd
}

// === Billing Admin Commands ===

func newAdminBillingCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "billing", Short: "Billing and subscription management"}
	cmd.AddCommand(newAdminBillingTiersCmd())
	cmd.AddCommand(newAdminBillingInitPostpaidCmd())
	cmd.AddCommand(newAdminBillingPromoteCmd())
	cmd.AddCommand(newAdminBillingSetClusterPricingCmd())
	return cmd
}

func newAdminBillingTiersCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "tiers", Short: "List billing tiers", RunE: func(cmd *cobra.Command, args []string) error {
		p, ctxCfg, err := purserGRPCClientFromContext()
		if err != nil {
			return err
		}
		defer func() { _ = p.Close() }()
		cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if ctxCfg.Auth.JWT != "" {
			cctx = context.WithValue(cctx, ctxkeys.KeyJWTToken, ctxCfg.Auth.JWT)
		}
		resp, err := p.GetBillingTiers(cctx, false, nil)
		if err != nil {
			return err
		}
		if output == "json" {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(resp)
		}
		ux.Heading(cmd.OutOrStdout(), fmt.Sprintf("Billing tiers (%d)", len(resp.Tiers)))
		for _, t := range resp.Tiers {
			defaults := ""
			if t.IsDefaultPrepaid {
				defaults = " [default-prepaid]"
			}
			if t.IsDefaultPostpaid {
				defaults = " [default-postpaid]"
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  %s (level=%d) %s - %s%.2f/mo%s\n",
				t.TierName, t.TierLevel, t.DisplayName, t.Currency, t.BasePrice, defaults)
		}
		return nil
	}}
	return cmd
}

func newAdminBillingInitPostpaidCmd() *cobra.Command {
	var tenantID string
	cmd := &cobra.Command{Use: "init-postpaid", Short: "Initialize postpaid billing for a tenant", RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(tenantID) == "" {
			return fmt.Errorf("--tenant-id is required")
		}
		if err := validateUUID(tenantID); err != nil {
			return fmt.Errorf("--tenant-id: %w", err)
		}
		p, ctxCfg, err := purserGRPCClientFromContext()
		if err != nil {
			return err
		}
		defer func() { _ = p.Close() }()
		cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if ctxCfg.Auth.JWT != "" {
			cctx = context.WithValue(cctx, ctxkeys.KeyJWTToken, ctxCfg.Auth.JWT)
		}
		resp, err := p.InitializePostpaidAccount(cctx, tenantID)
		if err != nil {
			return err
		}
		if output == "json" {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(resp)
		}
		ux.Success(cmd.OutOrStdout(), "Initialized postpaid account")
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  Subscription: %s\n", resp.SubscriptionId)
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  Tier level:   %d\n", resp.TierLevel)
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  Primary cluster: %s\n", resp.PrimaryClusterId)
		if len(resp.EligibleClusterIds) > 0 {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  Eligible clusters: %s\n", strings.Join(resp.EligibleClusterIds, ", "))
		}
		return nil
	}}
	cmd.Flags().StringVar(&tenantID, "tenant-id", "", "tenant id (required)")
	return cmd
}

func newAdminBillingPromoteCmd() *cobra.Command {
	var tenantID string
	cmd := &cobra.Command{Use: "promote", Short: "Promote tenant from prepaid to postpaid billing", RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(tenantID) == "" {
			return fmt.Errorf("--tenant-id is required")
		}
		if err := validateUUID(tenantID); err != nil {
			return fmt.Errorf("--tenant-id: %w", err)
		}
		p, ctxCfg, err := purserGRPCClientFromContext()
		if err != nil {
			return err
		}
		defer func() { _ = p.Close() }()
		cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if ctxCfg.Auth.JWT != "" {
			cctx = context.WithValue(cctx, ctxkeys.KeyJWTToken, ctxCfg.Auth.JWT)
		}
		resp, err := p.PromoteToPaid(cctx, tenantID, "")
		if err != nil {
			return err
		}
		if output == "json" {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(resp)
		}
		ux.Success(cmd.OutOrStdout(), "Promoted to postpaid billing")
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  Subscription: %s\n", resp.SubscriptionId)
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  Billing model: %s\n", resp.NewBillingModel)
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  Credit balance: %d cents\n", resp.CreditBalanceCents)
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  Tier level: %d\n", resp.TierLevel)
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  Primary cluster: %s\n", resp.PrimaryClusterId)
		if len(resp.EligibleClusterIds) > 0 {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  Eligible clusters: %s\n", strings.Join(resp.EligibleClusterIds, ", "))
		}
		return nil
	}}
	cmd.Flags().StringVar(&tenantID, "tenant-id", "", "tenant id (required)")
	return cmd
}

func newAdminBillingSetClusterPricingCmd() *cobra.Command {
	var clusterID string
	var pricingModel string
	var requiredTierLevel int
	var allowFreeTier bool
	var defaultQuotas string
	cmd := &cobra.Command{Use: "set-cluster-pricing", Short: "Set or update pricing config for a cluster", RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(clusterID) == "" {
			return fmt.Errorf("--cluster-id is required")
		}
		if strings.TrimSpace(pricingModel) == "" {
			return fmt.Errorf("--pricing-model is required")
		}
		validModels := map[string]bool{"free_unmetered": true, "metered": true, "monthly": true, "tier_inherit": true, "custom": true}
		if !validModels[pricingModel] {
			return fmt.Errorf("--pricing-model must be one of: free_unmetered, metered, monthly, tier_inherit, custom")
		}

		p, ctxCfg, err := purserGRPCClientFromContext()
		if err != nil {
			return err
		}
		defer func() { _ = p.Close() }()
		cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if ctxCfg.Auth.JWT != "" {
			cctx = context.WithValue(cctx, ctxkeys.KeyJWTToken, ctxCfg.Auth.JWT)
		}

		req := &pb.SetClusterPricingRequest{
			ClusterId:    clusterID,
			PricingModel: pricingModel,
		}
		if cmd.Flags().Changed("required-tier-level") {
			v := int32(requiredTierLevel)
			req.RequiredTierLevel = &v
		}
		if cmd.Flags().Changed("allow-free-tier") {
			req.AllowFreeTier = &allowFreeTier
		}
		if defaultQuotas != "" {
			quotas, parseErr := parseStructJSON(defaultQuotas)
			if parseErr != nil {
				return fmt.Errorf("--default-quotas: %w", parseErr)
			}
			req.DefaultQuotas = quotas
		}

		resp, err := p.SetClusterPricing(cctx, req)
		if err != nil {
			return err
		}
		if output == "json" {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(resp)
		}
		ux.Success(cmd.OutOrStdout(), fmt.Sprintf("Set cluster pricing for %s (model=%s)", clusterID, resp.PricingModel))
		return nil
	}}
	cmd.Flags().StringVar(&clusterID, "cluster-id", "", "cluster id (required)")
	cmd.Flags().StringVar(&pricingModel, "pricing-model", "", "pricing model: free_unmetered|metered|monthly|tier_inherit|custom (required)")
	cmd.Flags().IntVar(&requiredTierLevel, "required-tier-level", 0, "minimum tier level (0-5)")
	cmd.Flags().BoolVar(&allowFreeTier, "allow-free-tier", false, "allow free-tier tenants to access this cluster")
	cmd.Flags().StringVar(&defaultQuotas, "default-quotas", "", `default quotas as JSON (e.g. '{"max_streams":5,"max_viewers":500}')`)
	return cmd
}

// === User Admin Commands ===

func newAdminUsersCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "users", Short: "Manage users"}
	cmd.AddCommand(newAdminUsersCreateCmd())
	return cmd
}

func newAdminUsersCreateCmd() *cobra.Command {
	var tenantID string
	var email string
	var password string
	var passwordStdin bool
	var firstName string
	var lastName string
	var role string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a user in an existing tenant",
		Long: `Create a user in an existing tenant.

Tenant-id defaults to the active context's SystemTenantID (populated by a
successful 'cluster provision'). Pass --tenant-id to target a different
tenant.

Password input methods, in precedence order:
  1. --password <plaintext>      (leaks into shell history — avoid)
  2. --password-stdin            (read from stdin; good for CI)
  3. interactive prompt          (hidden echo; default when run at a TTY)`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()

			if strings.TrimSpace(tenantID) == "" {
				tenantID = tenantIDFromContext()
				if tenantID != "" {
					ux.ContextNotice(out, "tenant", tenantID)
				}
			}
			if strings.TrimSpace(tenantID) == "" {
				return fmt.Errorf("--tenant-id is required (no SystemTenantID in context; run 'cluster provision' first or pass --tenant-id)")
			}
			if err := validateUUID(tenantID); err != nil {
				return fmt.Errorf("--tenant-id: %w", err)
			}
			if strings.TrimSpace(email) == "" {
				return fmt.Errorf("--email is required")
			}
			allowedRoles := map[string]bool{"owner": true, "member": true}
			if !allowedRoles[role] {
				return fmt.Errorf("--role must be 'owner' or 'member', got %q", role)
			}

			resolvedPW, err := resolveUserPassword(password, passwordStdin)
			if err != nil {
				return err
			}

			cli, ctxCfg, err := commodoreGRPCClientFromContext()
			if err != nil {
				return err
			}
			defer func() { _ = cli.Close() }()
			cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if ctxCfg.Auth.JWT != "" {
				cctx = context.WithValue(cctx, ctxkeys.KeyJWTToken, ctxCfg.Auth.JWT)
			}

			resp, err := cli.CreateUserInTenant(cctx, &pb.CreateUserInTenantRequest{
				TenantId:  tenantID,
				Email:     email,
				Password:  resolvedPW,
				FirstName: firstName,
				LastName:  lastName,
				Role:      role,
			})
			if err != nil {
				return err
			}
			if output == "json" {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(resp)
			}

			ux.Result(out, []ux.ResultField{
				{Key: "email", OK: true, Detail: resp.User.GetEmail()},
				{Key: "user id", OK: true, Detail: resp.User.GetId()},
				{Key: "tenant", OK: true, Detail: tenantID},
				{Key: "role", OK: true, Detail: resp.User.GetRole()},
			})
			ux.PrintNextSteps(out, []ux.NextStep{
				{Cmd: fmt.Sprintf("frameworks login --email %s", resp.User.GetEmail()), Why: "Log in with the new account."},
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&tenantID, "tenant-id", "", "existing tenant UUID (defaults to active context's SystemTenantID)")
	cmd.Flags().StringVar(&email, "email", "", "user email address (required)")
	cmd.Flags().StringVar(&password, "password", "", "user password (plaintext; prefer --password-stdin)")
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "read user password from stdin")
	cmd.Flags().StringVar(&firstName, "first-name", "", "first name")
	cmd.Flags().StringVar(&lastName, "last-name", "", "last name")
	cmd.Flags().StringVar(&role, "role", "owner", "user role: owner|member")
	return cmd
}

// tenantIDFromContext returns the active context's SystemTenantID, or "".
// Safe to call without any context configured (returns "").
func tenantIDFromContext() string {
	cfg, err := fwcfg.Load()
	if err != nil {
		return ""
	}
	active, mErr := fwcfg.MaybeActiveContext(fwcfg.GetRuntimeOverrides(), fwcfg.OSEnv{}, cfg)
	if mErr != nil {
		return ""
	}
	return active.SystemTenantID
}

// clusterIDFromContext returns the active context's ClusterID, or "".
func clusterIDFromContext() string {
	cfg, err := fwcfg.Load()
	if err != nil {
		return ""
	}
	active, mErr := fwcfg.MaybeActiveContext(fwcfg.GetRuntimeOverrides(), fwcfg.OSEnv{}, cfg)
	if mErr != nil {
		return ""
	}
	return active.ClusterID
}

// resolveUserPassword picks the password source in the documented precedence:
// --password flag > --password-stdin > interactive prompt on a TTY.
func resolveUserPassword(passwordFlag string, fromStdin bool) (string, error) {
	if passwordFlag != "" {
		return passwordFlag, nil
	}
	if fromStdin {
		data, err := readAllStdin()
		if err != nil {
			return "", fmt.Errorf("read password from stdin: %w", err)
		}
		pw := strings.TrimRight(strings.TrimRight(data, "\n"), "\r")
		if pw == "" {
			return "", fmt.Errorf("--password-stdin received empty input")
		}
		return pw, nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", fmt.Errorf("no password provided (use --password, --password-stdin, or run at a TTY for interactive prompt)")
	}
	fmt.Fprint(os.Stderr, "Password: ")
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	if len(b) == 0 {
		return "", fmt.Errorf("empty password")
	}
	return string(b), nil
}

func readAllStdin() (string, error) {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1024), 1024*1024)
	if scanner.Scan() {
		return scanner.Text(), nil
	}
	return "", scanner.Err()
}

func newAdminClustersMigrateArtifactsCmd() *cobra.Command {
	var tenantID string
	var fromCluster string
	cmd := &cobra.Command{Use: "migrate-artifacts", Short: "Migrate artifact metadata from a source cluster", RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(tenantID) == "" {
			return fmt.Errorf("--tenant-id is required")
		}
		if strings.TrimSpace(fromCluster) == "" {
			return fmt.Errorf("--from-cluster is required")
		}

		fh, ctxCfg, err := foghornGRPCClientFromContext()
		if err != nil {
			return err
		}
		defer func() { _ = fh.Close() }()

		cctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if ctxCfg.Auth.JWT != "" {
			cctx = context.WithValue(cctx, ctxkeys.KeyJWTToken, ctxCfg.Auth.JWT)
		}

		resp, err := fh.Federation().MigrateArtifactMetadata(cctx, &pb.MigrateArtifactMetadataRequest{
			TenantId:        tenantID,
			SourceClusterId: fromCluster,
		})
		if err != nil {
			return fmt.Errorf("migrate artifacts: %w", err)
		}

		if resp.Error != "" {
			return fmt.Errorf("migration error: %s", resp.Error)
		}

		out := cmd.OutOrStdout()
		if output == "json" {
			enc := json.NewEncoder(out)
			enc.SetIndent("", "  ")
			return enc.Encode(resp)
		}
		ux.Success(out, "Artifact metadata migration complete")
		ux.Result(out, []ux.ResultField{
			{Key: "migrated", OK: true, Detail: fmt.Sprintf("%d", resp.MigratedCount)},
			{Key: "already existed", OK: true, Detail: fmt.Sprintf("%d", resp.AlreadyExists)},
		})
		return nil
	}}
	cmd.Flags().StringVar(&tenantID, "tenant-id", "", "tenant id (required)")
	cmd.Flags().StringVar(&fromCluster, "from-cluster", "", "source cluster id to migrate artifacts from (required)")
	return cmd
}
