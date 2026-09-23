package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"frameworks/cli/internal/ux"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// operatorListPageSize is the page size operator list commands request while
// walking every page of a cross-tenant listing.
const operatorListPageSize = 500

// requireOperatorSession fails before dialing when no user session is stored.
// These RPCs refuse the manifest service token, so without a login the call
// could only be denied.
func requireOperatorSession(jwt string) error {
	if strings.TrimSpace(jwt) == "" {
		return fmt.Errorf("this command needs a platform-operator session; run 'frameworks login' with a platform_operator account")
	}
	return nil
}

// collectPages walks a forward cursor listing until the server reports no next
// page. A server that repeats a cursor would loop forever, so that is an error.
func collectPages(ctx context.Context, jwt string, fetch func(ctx context.Context, page *commonpb.CursorPaginationRequest) (*commonpb.CursorPaginationResponse, error)) error {
	var after *string
	seen := map[string]bool{}
	for {
		cctx, cancel := adminRPCContext(ctx, jwt)
		pagination, err := fetch(cctx, &commonpb.CursorPaginationRequest{First: operatorListPageSize, After: after})
		cancel()
		if err != nil {
			return err
		}
		if !pagination.GetHasNextPage() || pagination.GetEndCursor() == "" {
			return nil
		}
		cursor := pagination.GetEndCursor()
		if seen[cursor] {
			return fmt.Errorf("listing did not advance past cursor %q", cursor)
		}
		seen[cursor] = true
		after = &cursor
	}
}

func formatOptionalTime(ts *timestamppb.Timestamp) string {
	if ts == nil {
		return "-"
	}
	return ts.AsTime().UTC().Format(time.RFC3339)
}

// === All-tenant API token listing ===

type adminAllTenantTokensClient interface {
	AdminListAPITokens(ctx context.Context, tenantID string, unsupportedScopesOnly bool, pagination *commonpb.CursorPaginationRequest) (*commodorepb.AdminListAPITokensResponse, error)
}

type adminAllTenantTokensFilter struct {
	TenantID              string
	UnsupportedScopesOnly bool
}

func runTokensListAllTenants(ctx context.Context, w io.Writer, cli adminAllTenantTokensClient, jwt string, filter adminAllTenantTokensFilter, outputJSON bool) error {
	if err := requireOperatorSession(jwt); err != nil {
		return err
	}
	var tokens []*commodorepb.AdminAPITokenInfo
	err := collectPages(ctx, jwt, func(cctx context.Context, page *commonpb.CursorPaginationRequest) (*commonpb.CursorPaginationResponse, error) {
		resp, err := cli.AdminListAPITokens(cctx, filter.TenantID, filter.UnsupportedScopesOnly, page)
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, resp.GetTokens()...)
		return resp.GetPagination(), nil
	})
	if err != nil {
		return err
	}
	if outputJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(&commodorepb.AdminListAPITokensResponse{Tokens: tokens})
	}
	heading := fmt.Sprintf("API tokens across tenants (%d)", len(tokens))
	if filter.UnsupportedScopesOnly {
		heading = fmt.Sprintf("API tokens with unsupported scopes (%d)", len(tokens))
	}
	ux.Heading(w, heading)
	for _, t := range tokens {
		_, _ = fmt.Fprintf(w, " - %s (%s) tenant=%s status=%s scopes=%s", t.GetTokenName(), t.GetId(), t.GetTenantId(), t.GetStatus(), strings.Join(t.GetPermissions(), ","))
		if len(t.GetUnsupportedPermissions()) > 0 {
			_, _ = fmt.Fprintf(w, " unsupported=%s", strings.Join(t.GetUnsupportedPermissions(), ","))
		}
		_, _ = fmt.Fprintf(w, " created=%s last_used=%s expires=%s\n", formatOptionalTime(t.GetCreatedAt()), formatOptionalTime(t.GetLastUsedAt()), formatOptionalTime(t.GetExpiresAt()))
	}
	return nil
}

// === Node fingerprint bindings ===

type adminNodeFingerprintsClient interface {
	ListNodeFingerprints(ctx context.Context, clusterID string, duplicatesOnly bool, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ListNodeFingerprintsResponse, error)
	UnbindNodeFingerprint(ctx context.Context, nodeID, fingerprintID, reason string) (*quartermasterpb.UnbindNodeFingerprintResponse, error)
}

func newAdminNodesFingerprintsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "fingerprints", Short: "Inspect and unbind node fingerprint bindings (platform operator)"}
	cmd.AddCommand(newAdminNodesFingerprintsListCmd())
	cmd.AddCommand(newAdminNodesFingerprintsUnbindCmd())
	return cmd
}

func newAdminNodesFingerprintsListCmd() *cobra.Command {
	var clusterID string
	var duplicates bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List node fingerprint bindings across tenants",
		Long: `List node fingerprint bindings across every tenant.

--duplicates shows only bindings whose machine-ID or MAC hash another binding
shares: the rows Quartermaster's unique fingerprint indexes reject.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			qm, ctxCfg, cleanup, err := qmGRPCClientFromContext(cmd.Context())
			if err != nil {
				return err
			}
			defer cleanup()
			defer func() { _ = qm.Close() }()
			return runNodeFingerprintsList(cmd.Context(), cmd.OutOrStdout(), qm, ctxCfg.Auth.JWT, strings.TrimSpace(clusterID), duplicates, output == "json")
		},
	}
	cmd.Flags().StringVar(&clusterID, "cluster-id", "", "only bindings of nodes in this cluster")
	cmd.Flags().BoolVar(&duplicates, "duplicates", false, "only bindings that share a machine-ID or MAC hash with another binding")
	return cmd
}

func newAdminNodesFingerprintsUnbindCmd() *cobra.Command {
	var reason string
	var yes bool
	cmd := &cobra.Command{
		Use:   "unbind <node-id> <fingerprint-id>",
		Short: "Delete a stale node fingerprint binding",
		Long: `Delete one node's fingerprint binding. The node loses its stored machine
and MAC hashes and its identity key, and must enroll again to rebind.
Quartermaster records node.fingerprint_unbound with your user ID and --reason.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			qm, ctxCfg, cleanup, err := qmGRPCClientFromContext(cmd.Context())
			if err != nil {
				return err
			}
			defer cleanup()
			defer func() { _ = qm.Close() }()
			confirm := func(nodeID, fingerprintID string) bool {
				return promptConfirm(fmt.Sprintf("Unbind fingerprint %s from node %s? The node must enroll again", fingerprintID, nodeID), yes)
			}
			return runNodeFingerprintUnbind(cmd.Context(), cmd.OutOrStdout(), qm, ctxCfg.Auth.JWT, args[0], args[1], reason, confirm)
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "why the binding is removed (required; recorded on the audit event)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip confirmation prompt")
	return cmd
}

func runNodeFingerprintsList(ctx context.Context, w io.Writer, qm adminNodeFingerprintsClient, jwt, clusterID string, duplicatesOnly, outputJSON bool) error {
	if err := requireOperatorSession(jwt); err != nil {
		return err
	}
	var bindings []*quartermasterpb.NodeFingerprintBinding
	err := collectPages(ctx, jwt, func(cctx context.Context, page *commonpb.CursorPaginationRequest) (*commonpb.CursorPaginationResponse, error) {
		resp, err := qm.ListNodeFingerprints(cctx, clusterID, duplicatesOnly, page)
		if err != nil {
			return nil, err
		}
		bindings = append(bindings, resp.GetFingerprints()...)
		return resp.GetPagination(), nil
	})
	if err != nil {
		return err
	}
	if outputJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(&quartermasterpb.ListNodeFingerprintsResponse{Fingerprints: bindings})
	}
	heading := fmt.Sprintf("Node fingerprint bindings (%d)", len(bindings))
	if duplicatesOnly {
		heading = fmt.Sprintf("Duplicate node fingerprint bindings (%d)", len(bindings))
	}
	ux.Heading(w, heading)
	for _, b := range bindings {
		cluster := b.GetClusterId()
		if cluster == "" {
			cluster = "(node row missing)"
		}
		key := "no"
		if b.GetHasIdentityKey() {
			key = "yes"
		}
		_, _ = fmt.Fprintf(w, " - node=%s fingerprint=%s cluster=%s tenant=%s machine=%s macs=%s identity_key=%s last_seen=%s\n",
			b.GetNodeId(), b.GetFingerprintId(), cluster, b.GetTenantId(),
			fingerprintHashLabel(b.GetFingerprintMachineSha256(), b.GetMachineDuplicateCount()),
			fingerprintHashLabel(b.GetFingerprintMacsSha256(), b.GetMacsDuplicateCount()),
			key, formatOptionalTime(b.GetLastSeen()))
	}
	if duplicatesOnly && len(bindings) > 0 {
		ux.PrintNextSteps(w, []ux.NextStep{{
			Cmd: `frameworks admin nodes fingerprints unbind <node-id> <fingerprint-id> --reason "..."`,
			Why: "Remove the stale binding of each duplicate group so the unique fingerprint indexes can build.",
		}})
	}
	return nil
}

// fingerprintHashLabel shortens a sha256 hex for display and marks how many
// bindings share it.
func fingerprintHashLabel(hash string, duplicates int32) string {
	hash = strings.TrimSpace(hash)
	if hash == "" {
		return "-"
	}
	if len(hash) > 12 {
		hash = hash[:12]
	}
	if duplicates > 1 {
		return fmt.Sprintf("%s(x%d)", hash, duplicates)
	}
	return hash
}

func runNodeFingerprintUnbind(ctx context.Context, w io.Writer, qm adminNodeFingerprintsClient, jwt, nodeID, fingerprintID, reason string, confirm func(nodeID, fingerprintID string) bool) error {
	nodeID = strings.TrimSpace(nodeID)
	fingerprintID = strings.TrimSpace(fingerprintID)
	reason = strings.TrimSpace(reason)
	if nodeID == "" {
		return fmt.Errorf("node ID is required")
	}
	if err := validateUUID(fingerprintID); err != nil {
		return fmt.Errorf("fingerprint ID: %w", err)
	}
	if reason == "" {
		return fmt.Errorf("--reason is required")
	}
	if err := requireOperatorSession(jwt); err != nil {
		return err
	}
	if !confirm(nodeID, fingerprintID) {
		_, _ = fmt.Fprintln(w, "Cancelled")
		return nil
	}
	cctx, cancel := adminRPCContext(ctx, jwt)
	defer cancel()
	if _, err := qm.UnbindNodeFingerprint(cctx, nodeID, fingerprintID, reason); err != nil {
		return err
	}
	ux.Success(w, fmt.Sprintf("Unbound fingerprint %s from node %s", fingerprintID, nodeID))
	return nil
}
