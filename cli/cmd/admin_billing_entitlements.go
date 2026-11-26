package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/spf13/cobra"
	"io"
	"maps"
	"strings"
)

type subscriptionTermsClient interface {
	GetSubscription(context.Context, string) (*purserpb.GetSubscriptionResponse, error)
	UpdateSubscriptionWithEntitlementReview(context.Context, *purserpb.UpdateSubscriptionRequest) (*purserpb.TenantSubscription, error)
}

func entitlementPatch(current map[string]string, values, remove []string) (map[string]string, error) {
	desired := maps.Clone(current)
	if desired == nil {
		desired = map[string]string{}
	}
	seen := map[string]bool{}
	for _, entry := range values {
		key, value, ok := strings.Cut(entry, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" || !json.Valid([]byte(value)) {
			return nil, fmt.Errorf("--set requires key=<JSON value>, got %q", entry)
		}
		if seen[key] {
			return nil, fmt.Errorf("entitlement %s occurs more than once", key)
		}
		desired[key] = value
		seen[key] = true
	}
	for _, key := range remove {
		key = strings.TrimSpace(key)
		if key == "" || seen[key] {
			return nil, fmt.Errorf("invalid or conflicting --unset %q", key)
		}
		delete(desired, key)
		seen[key] = true
	}
	return desired, nil
}

func runBillingEntitlements(ctx context.Context, w io.Writer, p subscriptionTermsClient, jwt, tenantID, reason string, values, remove []string, dryRun bool) error {
	if err := validateUUID(tenantID); err != nil {
		return err
	}
	writing := len(values)+len(remove) > 0
	if writing && strings.TrimSpace(reason) == "" {
		return fmt.Errorf("--reason is required for entitlement changes")
	}
	cctx, cancel := adminRPCContext(ctx, jwt)
	defer cancel()
	current, err := p.GetSubscription(cctx, tenantID)
	if err != nil {
		return err
	}
	if current.GetSubscription() == nil {
		return fmt.Errorf("tenant subscription not found")
	}
	if !writing {
		return writeProtoJSON(w, current.GetSubscription())
	}
	expected := current.GetSubscription().GetEntitlementOverrides()
	desired, err := entitlementPatch(expected, values, remove)
	if err != nil {
		return err
	}
	req := &purserpb.UpdateSubscriptionRequest{TenantId: tenantID, Reason: strings.TrimSpace(reason), EntitlementOverrides: desired, ClearEntitlementOverrides: len(desired) == 0, ExpectedEntitlementOverrides: expected, CheckEntitlementOverrides: true}
	if dryRun {
		return writeProtoJSON(w, req)
	}
	out, err := p.UpdateSubscriptionWithEntitlementReview(cctx, req)
	if err != nil {
		return err
	}
	return writeProtoJSON(w, out)
}

func newAdminBillingEntitlementsCmd() *cobra.Command {
	var target billingTenantTarget
	var reason string
	var values, remove []string
	var dryRun bool
	cmd := &cobra.Command{Use: "entitlements", Short: "Read or patch a tenant's entitlement overrides", Long: `Read the current subscription or patch its entitlement overrides without replacing unrelated keys.
Use --set key=<JSON value>, --unset key, and --reason for writes.
The write requires Purser's reviewed-map RPC (v0.3.16+) and checks the map under the subscription lock; concurrent edits are refused.
Zero and false are explicit values; --unset removes an override and restores its tier default.
A recording_retention_days cap of zero permits unlimited retention but does not change asset deletion policies.`, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := target.validate(); err != nil {
			return err
		}
		p, lookup, jwt, cleanup, err := billingTargetClients(cmd.Context(), target)
		if err != nil {
			return err
		}
		defer cleanup()
		tenantID, _, err := target.resolve(cmd.Context(), lookup, jwt)
		if err != nil {
			return err
		}
		terms, ok := p.(subscriptionTermsClient)
		if !ok {
			return fmt.Errorf("billing client does not support subscription terms")
		}
		return runBillingEntitlements(cmd.Context(), cmd.OutOrStdout(), terms, jwt, tenantID, reason, values, remove, dryRun)
	}}
	cmd.Flags().StringVar(&target.TenantID, "tenant-id", "", "tenant UUID")
	cmd.Flags().StringVar(&target.Email, "email", "", "email of a tenant user (operator session required)")
	cmd.Flags().StringArrayVar(&values, "set", nil, "entitlement key=<JSON value>; repeat for multiple keys")
	cmd.Flags().StringArrayVar(&remove, "unset", nil, "remove this override; repeat for multiple keys")
	cmd.Flags().StringVar(&reason, "reason", "", "audit reason for a change")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the exact reviewed request without changing it")
	return cmd
}
