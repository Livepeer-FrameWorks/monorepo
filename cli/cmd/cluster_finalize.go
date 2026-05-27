package cmd

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/credentials"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/remoteaccess"
	"frameworks/cli/pkg/ssh"

	"github.com/spf13/cobra"
)

type clusterFinalizeStep string

const (
	clusterFinalizeStepPurserBootstrap clusterFinalizeStep = "purser-bootstrap"
	clusterFinalizeStepPurserValidate  clusterFinalizeStep = "purser-validate"
	clusterFinalizeStepCommodore       clusterFinalizeStep = "commodore-bootstrap"
	clusterFinalizeStepAssignments     clusterFinalizeStep = "service-cluster-assignments"
	clusterFinalizeStepControlPlane    clusterFinalizeStep = "control-plane-validation"
	clusterFinalizeOnlyAll                                 = "all"
	clusterFinalizeOnlyPurser                              = "purser"
	clusterFinalizeOnlyCommodore                           = "commodore"
	clusterFinalizeOnlyAssignments                         = "assignments"
	clusterFinalizeOnlyValidation                          = "validation"
)

func newClusterFinalizeCmd() *cobra.Command {
	var only string
	var skipValidation bool
	var ignoreValidation bool

	cmd := &cobra.Command{
		Use:   "finalize",
		Short: "Run post-provision control-plane finalization only",
		Args:  cobra.NoArgs,
		Long: `Run the idempotent control-plane finalization steps normally executed
after cluster provisioning, without provisioning or restarting services.

This reconciles service-owned bootstrap state, service-cluster assignments, and
then validates the control plane. It is intended for resuming a failed provision
epilogue after fixing the underlying data/config issue.`,
		Example: `  # Resume all post-provision finalization steps
  frameworks cluster finalize --gitops-dir ../gitops --cluster production

  # Retry only Commodore bootstrap after fixing stale stream state
  frameworks cluster finalize --gitops-dir ../gitops --cluster production --only commodore

  # Reconcile public service assignments without restarting services
  frameworks cluster finalize --gitops-dir ../gitops --cluster production --only assignments

  # Run only the final control-plane validation gate
  frameworks cluster finalize --gitops-dir ../gitops --cluster production --only validation`,
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return err
			}
			defer rc.Cleanup()
			if err := requirePlatformIfImplicitManifest(rc, cmd.OutOrStdout()); err != nil {
				return err
			}
			return runClusterFinalize(cmd, rc, only, skipValidation, ignoreValidation)
		},
	}

	cmd.Flags().StringVar(&only, "only", clusterFinalizeOnlyAll, "Finalization slice to run (all|purser|commodore|assignments|validation)")
	cmd.Flags().BoolVar(&skipValidation, "skip-validation", false, "Skip final control-plane validation when --only=all")
	cmd.Flags().BoolVar(&ignoreValidation, "ignore-validation", false, "Continue even if control-plane validation has warnings")

	cmd.Flags().String("bootstrap-admin-email", "", "Create an initial operator user with this email")
	cmd.Flags().String("bootstrap-admin-password", "", "Plaintext password for bootstrap admin (prefer --bootstrap-admin-password-env or --bootstrap-admin-password-file)")
	cmd.Flags().String("bootstrap-admin-password-env", "", "Read bootstrap admin password from this environment variable")
	cmd.Flags().String("bootstrap-admin-password-file", "", "Read bootstrap admin password from this file")
	cmd.Flags().String("bootstrap-admin-first-name", "FrameWorks", "First name for bootstrap admin")
	cmd.Flags().String("bootstrap-admin-last-name", "Operator", "Last name for bootstrap admin")
	cmd.Flags().Bool("bootstrap-reset-credentials", false, "Allow bootstrap account entries with reset_credentials=true to update existing password hashes")

	cmd.Flags().Bool("strict-control-plane", false, "Fail (exit 1) if control-plane validation has warnings")

	return cmd
}

func runClusterFinalize(cmd *cobra.Command, rc *resolvedCluster, only string, skipValidation, _ bool) error {
	steps, err := clusterFinalizePlan(only, skipValidation)
	if err != nil {
		return err
	}

	manifest := rc.Manifest
	manifestDir := filepath.Dir(rc.ManifestPath)
	out := cmd.OutOrStdout()

	ux.Heading(out, fmt.Sprintf("Finalizing cluster from manifest: %s", rc.ManifestPath))
	fmt.Fprintf(out, "Step: %s\n\n", only)

	frozenManifest, releaseSelector, releaseVersion, err := freezeProvisionReleaseManifest(manifest, rc.ReleaseRepos)
	if err != nil {
		return err
	}
	manifest = frozenManifest
	rc.Manifest = frozenManifest
	if releaseSelector != releaseVersion {
		fmt.Fprintf(out, "Platform release: %s -> %s\n\n", releaseSelector, releaseVersion)
	} else {
		fmt.Fprintf(out, "Platform release: %s\n\n", releaseVersion)
	}

	sharedEnv, err := rc.SharedEnv()
	if err != nil {
		return fmt.Errorf("load manifest env_files: %w", err)
	}
	if isDevProfile(manifest) {
		if _, genErr := credentials.GenerateIfMissing(sharedEnv); genErr != nil {
			return fmt.Errorf("auto-generate dev secrets: %w", genErr)
		}
	} else if valErr := credentials.ValidateShared(sharedEnv); valErr != nil {
		return valErr
	}

	runtimeData := map[string]any{}
	if token := strings.TrimSpace(sharedEnv["SERVICE_TOKEN"]); token != "" {
		runtimeData["service_token"] = token
	} else {
		return fmt.Errorf("SERVICE_TOKEN missing from manifest env_files — add it to your gitops secrets")
	}
	if pkiRequired := internalPKIBootstrapRequired(manifest); pkiRequired {
		pki, pkiErr := loadInternalPKIBootstrap(sharedEnv, manifestDir)
		if pkiErr != nil {
			return fmt.Errorf("load internal PKI bootstrap material: %w", pkiErr)
		}
		runtimeData["internal_pki_bootstrap"] = pki
	}
	if qmAddr, addrErr := resolveServiceGRPCAddr(manifest, "quartermaster", defaultGRPCPort("quartermaster")); addrErr == nil {
		runtimeData["quartermaster_grpc_addr"] = qmAddr
	}

	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()

	sshKey := stringFlag(cmd, "ssh-key").Value
	sshPool := ssh.NewPool(30*time.Second, sshKey)
	defer sshPool.Close()

	raSession, err := remoteaccess.OpenSession(remoteaccess.Options{
		Manifest:      manifest,
		SSHKeyPath:    sshKey,
		AllowInsecure: isDevProfile(manifest),
	})
	if err != nil {
		return fmt.Errorf("open remote-access session: %w", err)
	}
	defer raSession.Close()

	needsBootstrapYAML := finalizeStepsContainBootstrap(steps)
	var bootstrapYAML []byte
	if needsBootstrapYAML {
		bootstrapYAML, err = renderBootstrapYAML(cmd, manifest, manifestDir, sharedEnv)
		if err != nil {
			return err
		}
	}

	if finalizeStepsContain(steps, clusterFinalizeStepControlPlane) {
		resolveCtx, resolveCancel := context.WithTimeout(ctx, provisionInitializeTimeout)
		systemTenantID, idErr := resolveSystemTenantIDViaQM(resolveCtx, manifest, runtimeData, raSession)
		resolveCancel()
		if idErr != nil {
			return fmt.Errorf("resolve system tenant: %w", idErr)
		}
		runtimeData["system_tenant_id"] = systemTenantID
		rememberSystemTenantID(cmd, systemTenantID)
	}

	for _, step := range steps {
		switch step {
		case clusterFinalizeStepPurserBootstrap:
			if err := runServiceBootstrap(ctx, cmd, manifest, sshPool, "purser", bootstrapYAML, nil); err != nil {
				return fmt.Errorf("purser bootstrap: %w", err)
			}
		case clusterFinalizeStepPurserValidate:
			if err := runServiceBootstrapValidate(ctx, cmd, manifest, sshPool, "purser"); err != nil {
				return fmt.Errorf("purser bootstrap validate: %w", err)
			}
		case clusterFinalizeStepCommodore:
			if err := runServiceBootstrap(ctx, cmd, manifest, sshPool, "commodore", bootstrapYAML, commodoreBootstrapExtraArgs(cmd)); err != nil {
				return fmt.Errorf("commodore bootstrap: %w", err)
			}
		case clusterFinalizeStepAssignments:
			if err := reconcileServiceClusterAssignments(ctx, cmd, manifest, runtimeData, raSession); err != nil {
				return fmt.Errorf("service-cluster assignments: %w", err)
			}
		case clusterFinalizeStepControlPlane:
			if err := validateControlPlane(ctx, cmd, manifest, runtimeData, raSession); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown finalize step %q", step)
		}
	}

	if rc.Source == inventory.SourceManifestFlag {
		rememberLastManifest(cmd, rc.ManifestPath)
	}
	ux.Success(out, "Cluster finalization complete")
	return nil
}

func clusterFinalizePlan(only string, skipValidation bool) ([]clusterFinalizeStep, error) {
	switch only {
	case clusterFinalizeOnlyAll:
		steps := []clusterFinalizeStep{
			clusterFinalizeStepPurserBootstrap,
			clusterFinalizeStepPurserValidate,
			clusterFinalizeStepCommodore,
			clusterFinalizeStepAssignments,
		}
		if !skipValidation {
			steps = append(steps, clusterFinalizeStepControlPlane)
		}
		return steps, nil
	case clusterFinalizeOnlyPurser:
		if skipValidation {
			return []clusterFinalizeStep{clusterFinalizeStepPurserBootstrap}, nil
		}
		return []clusterFinalizeStep{clusterFinalizeStepPurserBootstrap, clusterFinalizeStepPurserValidate}, nil
	case clusterFinalizeOnlyCommodore:
		return []clusterFinalizeStep{clusterFinalizeStepCommodore}, nil
	case clusterFinalizeOnlyAssignments:
		return []clusterFinalizeStep{clusterFinalizeStepAssignments}, nil
	case clusterFinalizeOnlyValidation:
		if skipValidation {
			return nil, fmt.Errorf("--skip-validation cannot be used with --only=validation")
		}
		return []clusterFinalizeStep{clusterFinalizeStepControlPlane}, nil
	default:
		return nil, fmt.Errorf("invalid finalization slice: %s (must be all, purser, commodore, assignments, or validation)", only)
	}
}

func finalizeStepsContainBootstrap(steps []clusterFinalizeStep) bool {
	return finalizeStepsContain(steps, clusterFinalizeStepPurserBootstrap) ||
		finalizeStepsContain(steps, clusterFinalizeStepCommodore)
}

func finalizeStepsContain(steps []clusterFinalizeStep, needle clusterFinalizeStep) bool {
	for _, step := range steps {
		if step == needle {
			return true
		}
	}
	return false
}
