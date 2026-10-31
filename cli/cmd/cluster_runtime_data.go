package cmd

import (
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"strings"

	"frameworks/cli/pkg/bootstrap"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
	"frameworks/cli/pkg/remoteaccess"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// clusterRuntimeData is the runtime data every task of an existing cluster
// renders with: provisionRuntimeData plus the control-plane identities
// Quartermaster owns (system_tenant_id, owner_tenant_ids_by_alias) and
// quartermaster_grpc_addr. `cluster diff`, `cluster apply`, `cluster upgrade`
// and `release apply` (its host convergence and service upgrades) all render
// from it, so they produce the same config `cluster provision` deploys once
// its bootstrap has resolved the same identities.
func clusterRuntimeData(ctx context.Context, manifest *inventory.Manifest, sharedEnv map[string]string, manifestDir, sshKey string) (map[string]any, error) {
	runtimeData, err := provisionRuntimeData(manifest, manifestDir, sharedEnv)
	if err != nil {
		return nil, err
	}
	qm, hasQuartermaster := manifest.Services["quartermaster"]
	if !hasQuartermaster || !qm.Enabled {
		return runtimeData, nil
	}
	if strings.TrimSpace(sharedEnv["SERVICE_TOKEN"]) == "" {
		return nil, fmt.Errorf("build authoritative desired state: SERVICE_TOKEN is required to resolve control-plane identities")
	}
	sess, err := remoteaccess.OpenSession(remoteaccess.Options{
		Manifest:      manifest,
		SSHKeyPath:    sshKey,
		AllowInsecure: isDevProfile(manifest),
	})
	if err != nil {
		return nil, fmt.Errorf("build authoritative desired state: open control-plane session: %w", err)
	}
	defer sess.Close()
	ownerTenantIDs, err := resolveClusterOwnerTenantIDs(ctx, manifest, runtimeData, sess)
	if err != nil {
		return nil, fmt.Errorf("build authoritative desired state: resolve cluster owners: %w", err)
	}
	systemTenantID := strings.TrimSpace(ownerTenantIDs[bootstrap.SystemTenantAlias])
	if systemTenantID == "" {
		return nil, fmt.Errorf("build authoritative desired state: Quartermaster returned no system tenant UUID")
	}
	runtimeData["system_tenant_id"] = systemTenantID
	runtimeData["owner_tenant_ids_by_alias"] = ownerTenantIDs
	if qmAddr, addrErr := resolveServiceGRPCAddr(manifest, "quartermaster", defaultGRPCPort("quartermaster")); addrErr == nil {
		runtimeData["quartermaster_grpc_addr"] = qmAddr
	}
	return runtimeData, nil
}

// clusterRuntimeDataFn is the control-plane rendering boundary for recovery tests.
var clusterRuntimeDataFn = clusterRuntimeData

// provisionNeedsExistingClusterOwners reports whether a provision renders a
// Livepeer gateway without running the Quartermaster bootstrap that resolves
// cluster owners, as `--only-services livepeer-gateway-*` does. Such a run
// reads the owners from the running Quartermaster first.
func provisionNeedsExistingClusterOwners(plan *orchestrator.ExecutionPlan) bool {
	if plan == nil {
		return false
	}
	return remainingBatchesContainService(plan.Batches, "livepeer-gateway") && !remainingBatchesContainService(plan.Batches, "quartermaster")
}

// resolveExistingClusterOwners is the read-only lookup of the identities the
// Quartermaster bootstrap step otherwise resolves for gateway rendering.
func resolveExistingClusterOwners(ctx context.Context, manifest *inventory.Manifest, runtimeData map[string]any, sess *remoteaccess.Session) error {
	owners, err := resolveClusterOwnerTenantIDs(ctx, manifest, runtimeData, sess)
	if err != nil {
		return err
	}
	runtimeData["owner_tenant_ids_by_alias"] = owners
	if systemTenantID := strings.TrimSpace(owners[bootstrap.SystemTenantAlias]); systemTenantID != "" {
		runtimeData["system_tenant_id"] = systemTenantID
	}
	return nil
}

// RuntimeData returns clusterRuntimeData for this invocation, resolved once
// and cloned per caller, so a release that upgrades many services dials
// Quartermaster once.
//
// When Quartermaster cannot answer (upgrading or recovering Quartermaster
// itself), the identity a prior context-sourced run saved still supplies
// system_tenant_id. That only reproduces the full render when no cluster runs
// the Livepeer gateway, the one consumer of owner_tenant_ids_by_alias, so a
// manifest with a gateway still fails. A manifest that does not run
// Quartermaster resolves system_tenant_id through its context's control plane.
func (rc *resolvedCluster) RuntimeData(ctx context.Context, sshKey string) (map[string]any, error) {
	rc.runtimeDataOnce.Do(func() {
		sharedEnv, err := rc.PreparedSharedEnv()
		if err != nil {
			rc.runtimeDataErr = fmt.Errorf("load manifest env_files: %w", err)
			return
		}
		manifestDir := filepath.Dir(rc.ManifestPath)
		data, err := clusterRuntimeDataFn(ctx, rc.Manifest, sharedEnv, manifestDir, sshKey)
		if err != nil {
			remembered := contextSystemTenantID(rc)
			if remembered == "" || !quartermasterRecoveryUnavailable(err) {
				rc.runtimeDataErr = err
				return
			}
			// Gateway ownership needs live alias resolution as well as a system identity.
			if anyClusterRunsLivepeerGateway(rc.Manifest) {
				rc.runtimeDataErr = fmt.Errorf("authoritative owner aliases are required for Livepeer gateway recovery rendering: %w", err)
				return
			}
			if data, rc.runtimeDataErr = provisionRuntimeData(rc.Manifest, manifestDir, sharedEnv); rc.runtimeDataErr != nil {
				return
			}
			rc.warnSavedSystemTenantRecovery()
			data["system_tenant_id"] = remembered
			if qmAddr, addrErr := resolveServiceGRPCAddr(rc.Manifest, "quartermaster", defaultGRPCPort("quartermaster")); addrErr == nil {
				data["quartermaster_grpc_addr"] = qmAddr
			}
		}
		if id, ok := data["system_tenant_id"].(string); !ok || id == "" {
			resolved, tenantErr := rc.ResolveSystemTenantID(ctx)
			if tenantErr != nil {
				remembered := contextSystemTenantID(rc)
				// A failed identity lookup can reuse a complete live owner-alias snapshot.
				owners, aliasesKnown := data["owner_tenant_ids_by_alias"].(map[string]string)
				if remembered == "" || !quartermasterRecoveryUnavailable(tenantErr) || (anyClusterRunsLivepeerGateway(rc.Manifest) && (!aliasesKnown || len(owners) == 0)) {
					rc.runtimeDataErr = fmt.Errorf("resolve system tenant for service configuration: %w", tenantErr)
					return
				}
				rc.warnSavedSystemTenantRecovery()
				resolved = remembered
			}
			data["system_tenant_id"] = resolved
		}
		rc.runtimeData = data
	})
	if rc.runtimeDataErr != nil {
		return nil, rc.runtimeDataErr
	}
	return maps.Clone(rc.runtimeData), nil
}

// warnSavedSystemTenantRecovery identifies rendering that uses the saved identity.
func (rc *resolvedCluster) warnSavedSystemTenantRecovery() {
	fmt.Fprintln(rc.warningOutput(), "Warning: Quartermaster is unavailable; using the saved system tenant ID for recovery rendering")
}

// quartermasterRecoveryUnavailable excludes authoritative alias and permission failures.
func quartermasterRecoveryUnavailable(err error) bool {
	return status.Code(err) == codes.Unavailable || status.Code(err) == codes.DeadlineExceeded || remoteaccess.IsUnavailable(err)
}
