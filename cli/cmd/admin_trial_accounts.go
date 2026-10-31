package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"frameworks/cli/internal/ux"

	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

type accountActivationClient interface {
	AdminSetUserActive(context.Context, *commodorepb.AdminSetUserActiveRequest) (*commodorepb.AdminSetUserActiveResponse, error)
	AdminLookupUserByEmail(context.Context, string) (*commodorepb.AdminLookupUserByEmailResponse, error)
}

func runAccountUserActive(ctx context.Context, w io.Writer, client accountActivationClient, jwt string, req *commodorepb.AdminSetUserActiveRequest, jsonOutput bool) error {
	callCtx, cancel := adminRPCContext(ctx, jwt)
	defer cancel()
	if req.TenantId == "" {
		if req.Email == "" {
			return fmt.Errorf("--tenant-id is required with --user-id")
		}
		user, err := client.AdminLookupUserByEmail(callCtx, req.Email)
		if err != nil {
			return err
		}
		req.TenantId = user.GetTenantId()
	}
	response, err := client.AdminSetUserActive(callCtx, req)
	if err != nil {
		return err
	}
	if jsonOutput {
		return writeProtoJSON(w, response)
	}
	ux.Result(w, []ux.ResultField{{Key: "user", OK: true, Detail: response.GetEmail()}, {Key: "active", OK: true, Detail: fmt.Sprint(response.GetIsActive())}})
	return nil
}

// newAdminUsersActiveCmd changes user access while preserving tenant-owned media.
func newAdminUsersActiveCmd(active bool) *cobra.Command {
	verb := "deactivate"
	if active {
		verb = "activate"
	}
	req := &commodorepb.AdminSetUserActiveRequest{Active: active}
	cmd := &cobra.Command{Use: verb, Short: strings.ToUpper(verb[:1]) + verb[1:] + " a user's access", RunE: func(cmd *cobra.Command, _ []string) error {
		if strings.TrimSpace(req.Reason) == "" {
			return fmt.Errorf("--reason is required")
		}
		if (req.Email == "") == (req.UserId == "") {
			return fmt.Errorf("exactly one of --email or --user-id is required")
		}
		if req.UserId != "" && req.TenantId == "" {
			return fmt.Errorf("--tenant-id is required with --user-id")
		}
		client, cfg, cleanup, err := commodoreGRPCClientFromContext(cmd.Context())
		if err != nil {
			return err
		}
		defer cleanup()
		defer client.Close()
		return runAccountUserActive(cmd.Context(), cmd.OutOrStdout(), client, cfg.Auth.JWT, req, output == "json")
	}}
	cmd.Flags().StringVar(&req.TenantId, "tenant-id", "", "tenant UUID (resolved from --email when omitted)")
	cmd.Flags().StringVar(&req.UserId, "user-id", "", "user UUID")
	cmd.Flags().StringVar(&req.Email, "email", "", "user email")
	cmd.Flags().StringVar(&req.Reason, "reason", "", "reason recorded in the private operator audit (required)")
	return cmd
}

// newAdminTenantsDeactivateCmd requires an existing tenant and an exact name confirmation.
func newAdminTenantsDeactivateCmd() *cobra.Command {
	var reason, tenantName string
	var yes bool
	cmd := &cobra.Command{Use: "deactivate <tenant-id>", Short: "Permanently close a tenant account", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateUUID(args[0]); err != nil {
			return err
		}
		args[0] = uuid.MustParse(args[0]).String()
		if strings.TrimSpace(reason) == "" {
			return fmt.Errorf("--reason is required")
		}
		qm, qcfg, qcleanup, err := qmGRPCClientFromContext(cmd.Context())
		if err != nil {
			return err
		}
		defer qcleanup()
		defer qm.Close()
		lookupCtx, lookupCancel := adminRPCContext(cmd.Context(), qcfg.Auth.JWT)
		defer lookupCancel()
		tenant, err := qm.GetTenant(lookupCtx, args[0])
		if err != nil {
			return err
		}
		if tenant.GetTenant().GetId() != args[0] {
			return fmt.Errorf("tenant not found")
		}
		client, cfg, cleanup, err := commodoreGRPCClientFromContext(cmd.Context())
		if err != nil {
			return err
		}
		defer cleanup()
		defer client.Close()
		return runTenantClosure(cmd, client, cfg.Auth.JWT, args[0], tenant.GetTenant().GetName(), tenantName, reason, yes, output == "json")
	}}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "confirm permanent closure with --tenant-name")
	cmd.Flags().StringVar(&tenantName, "tenant-name", "", "exact tenant name required with --yes")
	cmd.Flags().StringVar(&reason, "reason", "", "reason recorded in the private operator audit (required)")
	return cmd
}

type tenantClosureClient interface {
	AdminDeactivateTenant(context.Context, *commodorepb.AdminDeactivateTenantRequest) error
}

// runTenantClosure confirms the tenant name on stderr and emits one result after the RPC.
func runTenantClosure(cmd *cobra.Command, client tenantClosureClient, jwt, tenantID, actualName, confirmedName, reason string, yes, jsonOutput bool) error {
	id, err := uuid.Parse(tenantID)
	if err != nil {
		return err
	}
	if !yes && confirmedName != "" {
		return fmt.Errorf("--tenant-name requires --yes; omit both flags to confirm interactively")
	}
	if !yes {
		fmt.Fprintf(cmd.ErrOrStderr(), "Permanently deactivate %s (%s). Type the tenant name to confirm: ", actualName, id)
		confirmedName, err = bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
		if err != nil {
			return fmt.Errorf("read tenant name confirmation: %w", err)
		}
		confirmedName = strings.TrimSuffix(strings.TrimSuffix(confirmedName, "\n"), "\r")
	}
	if err := confirmTenantClosure(actualName, confirmedName, yes); err != nil {
		return err
	}
	callCtx, cancel := adminRPCContext(cmd.Context(), jwt)
	defer cancel()
	if err := client.AdminDeactivateTenant(callCtx, &commodorepb.AdminDeactivateTenantRequest{TenantId: id.String(), TenantName: confirmedName, Reason: reason}); err != nil {
		return err
	}
	if jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"tenant_id": id.String(), "active": false})
	}
	ux.Success(cmd.OutOrStdout(), "Tenant permanently deactivated")
	return nil
}

// confirmTenantClosure requires the exact current name for interactive and unattended closure.
func confirmTenantClosure(actual, confirmed string, yes bool) error {
	if yes && confirmed == "" {
		return fmt.Errorf("--yes requires --tenant-name with the exact current name")
	}
	if confirmed == "" || confirmed != actual {
		return fmt.Errorf("tenant name confirmation does not match; --yes requires --tenant-name with the exact current name")
	}
	return nil
}

// newAdminTenantsGetCmd reads one tenant by UUID without scanning the tenant list.
func newAdminTenantsGetCmd() *cobra.Command {
	return &cobra.Command{Use: "get <tenant-id>", Short: "Read a tenant by UUID", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		id, err := uuid.Parse(args[0])
		if err != nil {
			return err
		}
		client, cfg, cleanup, err := qmGRPCClientFromContext(cmd.Context())
		if err != nil {
			return err
		}
		defer cleanup()
		defer client.Close()
		callCtx, cancel := adminRPCContext(cmd.Context(), cfg.Auth.JWT)
		defer cancel()
		response, err := client.GetTenant(callCtx, id.String())
		if err != nil {
			return err
		}
		if output == "json" {
			return writeProtoJSON(cmd.OutOrStdout(), response)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s (%s)\n", response.GetTenant().GetName(), response.GetTenant().GetId())
		return nil
	}}
}

// newAdminTenantsHistoryCmd reads private operator lifecycle reasons with stable pagination.
func newAdminTenantsHistoryCmd() *cobra.Command {
	request := &commodorepb.AdminListAccountHistoryRequest{}
	command := &cobra.Command{Use: "history <tenant-id>", Short: "Read private operator account history", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		id, err := uuid.Parse(args[0])
		if err != nil {
			return err
		}
		request.TenantId = id.String()
		client, cfg, cleanup, err := commodoreGRPCClientFromContext(cmd.Context())
		if err != nil {
			return err
		}
		defer cleanup()
		defer client.Close()
		callCtx, cancel := adminRPCContext(cmd.Context(), cfg.Auth.JWT)
		defer cancel()
		response, err := client.AdminListAccountHistory(callCtx, request)
		if err != nil {
			return err
		}
		if output == "json" {
			return writeProtoJSON(cmd.OutOrStdout(), response)
		}
		if len(response.GetEntries()) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "No account history found.")
		}
		for _, entry := range response.GetEntries() {
			fmt.Fprintf(cmd.OutOrStdout(), "%s  %s  target=%s  actor=%s  %s\n", entry.GetCreatedAt().AsTime().UTC().Format("2006-01-02T15:04:05Z"), entry.GetAction(), entry.GetTargetId(), entry.GetActorUserId(), strconv.Quote(entry.GetReason()))
		}
		if response.GetHasMore() {
			fmt.Fprintf(cmd.OutOrStdout(), "More history available; pass --before %s for the next page.\n", response.GetNextCursor())
		}
		return nil
	}}
	command.Flags().Int32Var(&request.Limit, "limit", 100, "page size (1..500)")
	command.Flags().StringVar(&request.Before, "before", "", "cursor from the preceding page")
	return command
}
