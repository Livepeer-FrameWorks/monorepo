package cmd

import (
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"strings"

	"frameworks/cli/pkg/bootstrap"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/remoteaccess"
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
		data, err := clusterRuntimeData(ctx, rc.Manifest, sharedEnv, manifestDir, sshKey)
		if err != nil {
			remembered := strings.TrimSpace(rc.ContextSystemTenantID)
			contextSourced := rc.Source == inventory.SourceContext || rc.Source == inventory.SourceContextLastManifest
			if remembered == "" || !contextSourced || anyClusterRunsLivepeerGateway(rc.Manifest) {
				rc.runtimeDataErr = err
				return
			}
			if data, rc.runtimeDataErr = provisionRuntimeData(rc.Manifest, manifestDir, sharedEnv); rc.runtimeDataErr != nil {
				return
			}
			data["system_tenant_id"] = remembered
			if qmAddr, addrErr := resolveServiceGRPCAddr(rc.Manifest, "quartermaster", defaultGRPCPort("quartermaster")); addrErr == nil {
				data["quartermaster_grpc_addr"] = qmAddr
			}
		}
		if id, ok := data["system_tenant_id"].(string); !ok || id == "" {
			resolved, tenantErr := rc.ResolveSystemTenantID(ctx)
			if tenantErr != nil {
				rc.runtimeDataErr = fmt.Errorf("resolve system tenant for service configuration: %w", tenantErr)
				return
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
