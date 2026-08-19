package cmd

import (
	"fmt"
	"path/filepath"

	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/credentials"

	"github.com/spf13/cobra"
)

const (
	clusterControlPlaneDomainAll           = "all"
	clusterControlPlaneDomainQuartermaster = "quartermaster"
	clusterControlPlaneDomainBilling       = "billing"
	clusterControlPlaneDomainAccounts      = "accounts"
	clusterControlPlaneDomainAssignments   = "assignments"
	clusterControlPlaneDomainValidation    = "validation"
)

func newClusterControlPlaneCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "control-plane",
		Short: "Plan and reconcile platform control-plane state",
		Long: `Plan and reconcile the desired platform state owned by FrameWorks services.

This covers the Quartermaster catalog and tenant state, Purser billing state,
Commodore managed accounts, service-cluster assignments, and cross-service
validation. It does not provision hosts, deploy services, or run schema
migrations.`,
	}
	cmd.AddCommand(newClusterControlPlanePlanCmd())
	cmd.AddCommand(newClusterControlPlaneReconcileCmd())
	return cmd
}

func newClusterControlPlanePlanCmd() *cobra.Command {
	var domain string
	var skipValidation bool

	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Validate and show the control-plane reconciliation plan",
		Args:  cobra.NoArgs,
		Long: `Resolve the cluster release, render and validate its bootstrap desired
state, and print the ordered reconciliation steps. This command does not open
remote sessions or change service state.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return err
			}
			defer rc.Cleanup()
			if err := requirePlatformIfImplicitManifest(rc, cmd.OutOrStdout()); err != nil {
				return err
			}
			return runClusterControlPlanePlan(cmd, rc, domain, skipValidation)
		},
	}

	cmd.Flags().StringVar(&domain, "domain", clusterControlPlaneDomainAll, "Control-plane domain to plan (all|quartermaster|billing|accounts|assignments|validation)")
	cmd.Flags().BoolVar(&skipValidation, "skip-validation", false, "Omit validation steps when --domain=all or --domain=billing")
	addControlPlaneBootstrapFlags(cmd)
	return cmd
}

func newClusterControlPlaneReconcileCmd() *cobra.Command {
	var domain string
	var skipValidation bool
	var ignoreValidation bool

	cmd := &cobra.Command{
		Use:   "reconcile",
		Short: "Reconcile platform control-plane state",
		Args:  cobra.NoArgs,
		Long: `Run the selected idempotent control-plane reconciliation steps without
provisioning hosts, deploying services, or running schema migrations.`,
		Example: `  # Reconcile all control-plane domains
  frameworks cluster control-plane reconcile --gitops-dir ../gitops --cluster production

  # Reconcile billing catalog and pricing state only
  frameworks cluster control-plane reconcile --domain billing

  # Reconcile service-cluster assignments only
  frameworks cluster control-plane reconcile --domain assignments`,
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return err
			}
			defer rc.Cleanup()
			if err := requirePlatformIfImplicitManifest(rc, cmd.OutOrStdout()); err != nil {
				return err
			}
			return runClusterControlPlaneReconcile(cmd, rc, domain, skipValidation, ignoreValidation)
		},
	}

	cmd.Flags().StringVar(&domain, "domain", clusterControlPlaneDomainAll, "Control-plane domain to reconcile (all|quartermaster|billing|accounts|assignments|validation)")
	cmd.Flags().BoolVar(&skipValidation, "skip-validation", false, "Skip validation when --domain=all or --domain=billing")
	cmd.Flags().BoolVar(&ignoreValidation, "ignore-validation", false, "Continue even if control-plane validation has warnings")
	addControlPlaneBootstrapFlags(cmd)
	return cmd
}

func runClusterControlPlaneReconcile(cmd *cobra.Command, rc *resolvedCluster, domain string, skipValidation, ignoreValidation bool) error {
	only, err := controlPlaneDomainFinalizeOnly(domain)
	if err != nil {
		return err
	}
	return runClusterFinalizeWithLabels(
		cmd,
		rc,
		only,
		skipValidation,
		ignoreValidation,
		"Reconciling control-plane state from manifest",
		"Control-plane reconciliation complete",
		"Domain",
		domain,
	)
}

func runClusterControlPlanePlan(cmd *cobra.Command, rc *resolvedCluster, domain string, skipValidation bool) error {
	only, err := controlPlaneDomainFinalizeOnly(domain)
	if err != nil {
		return err
	}
	steps, err := clusterFinalizePlan(only, skipValidation)
	if err != nil {
		return err
	}

	manifest := rc.Manifest
	manifestDir := filepath.Dir(rc.ManifestPath)
	out := cmd.OutOrStdout()

	ux.Heading(out, fmt.Sprintf("Control-plane plan from manifest: %s", rc.ManifestPath))
	frozenManifest, releaseSelector, releaseVersion, err := freezeProvisionReleaseManifest(manifest, rc.ReleaseRepos)
	if err != nil {
		return err
	}
	manifest = frozenManifest
	if releaseSelector != releaseVersion {
		fmt.Fprintf(out, "Platform release: %s -> %s\n", releaseSelector, releaseVersion)
	} else {
		fmt.Fprintf(out, "Platform release: %s\n", releaseVersion)
	}
	fmt.Fprintf(out, "Domain: %s\n", domain)

	if finalizeStepsContainBootstrap(steps) {
		sharedEnv, envErr := rc.SharedEnv()
		if envErr != nil {
			return fmt.Errorf("load manifest env_files: %w", envErr)
		}
		if isDevProfile(manifest) {
			if _, generateErr := credentials.GenerateIfMissing(sharedEnv); generateErr != nil {
				return fmt.Errorf("prepare dev bootstrap secrets: %w", generateErr)
			}
		} else {
			if validateErr := credentials.ValidateShared(sharedEnv); validateErr != nil {
				return validateErr
			}
		}
		if _, renderErr := renderBootstrapYAML(cmd, manifest, manifestDir, sharedEnv); renderErr != nil {
			return renderErr
		}
		fmt.Fprintln(out, "Desired bootstrap state: valid")
	} else {
		fmt.Fprintln(out, "Desired bootstrap state: not required for selected domain")
	}

	fmt.Fprintln(out, "Steps:")
	for i, step := range steps {
		fmt.Fprintf(out, "  %d. %s\n", i+1, controlPlaneStepDescription(step))
	}
	fmt.Fprintln(out, "\nNo changes applied.")
	return nil
}

func controlPlaneDomainFinalizeOnly(domain string) (string, error) {
	switch domain {
	case clusterControlPlaneDomainAll:
		return clusterFinalizeOnlyAll, nil
	case clusterControlPlaneDomainQuartermaster:
		return clusterFinalizeOnlyQuartermaster, nil
	case clusterControlPlaneDomainBilling:
		return clusterFinalizeOnlyPurser, nil
	case clusterControlPlaneDomainAccounts:
		return clusterFinalizeOnlyCommodore, nil
	case clusterControlPlaneDomainAssignments:
		return clusterFinalizeOnlyAssignments, nil
	case clusterControlPlaneDomainValidation:
		return clusterFinalizeOnlyValidation, nil
	default:
		return "", fmt.Errorf("invalid control-plane domain: %s (must be all, quartermaster, billing, accounts, assignments, or validation)", domain)
	}
}

func controlPlaneStepDescription(step clusterFinalizeStep) string {
	switch step {
	case clusterFinalizeStepQuartermaster:
		return "quartermaster: reconcile tenants, clusters, ingress, and service catalog"
	case clusterFinalizeStepPurserBootstrap:
		return "billing: reconcile tiers, pricing, and billing invariants"
	case clusterFinalizeStepPurserValidate:
		return "billing: validate cluster pricing"
	case clusterFinalizeStepCommodore:
		return "accounts: reconcile managed operator accounts and bootstrap state"
	case clusterFinalizeStepAssignments:
		return "assignments: reconcile service instances to clusters"
	case clusterFinalizeStepControlPlane:
		return "validation: check cross-service control-plane invariants"
	default:
		return string(step)
	}
}
