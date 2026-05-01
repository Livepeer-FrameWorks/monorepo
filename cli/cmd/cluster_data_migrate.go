package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"frameworks/cli/internal/releases"
	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/exec"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/preflight"
	"frameworks/cli/pkg/ssh"
	"frameworks/pkg/datamigrate"

	"github.com/spf13/cobra"
)

func newClusterDataMigrateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "data-migrate",
		Short: "List and run service-owned background data migrations",
		Long: `Operate on service-owned background data migrations across the cluster.

Each adopting service exposes a "data-migrations" subcommand on its own
binary; this command fans out over SSH and aggregates the results.

When the release catalog is empty, "list" prints "no required data
migrations declared" — that is the honest empty-state signal, not a silent
"all clear." When a service has not adopted the data-migrations library,
its declared migrations are reported as blockers, never as completed.`,
	}
	cmd.AddCommand(
		newDMListCmd(),
		newDMStatusCmd(),
		newDMRunCmd(),
		newDMVerifyCmd(),
		newDMPauseCmd(),
		newDMResumeCmd(),
	)
	return cmd
}

func newDMListCmd() *cobra.Command {
	var toVersion, format string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List required data migrations declared in the release catalog",
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return err
			}
			defer rc.Cleanup()
			target, err := resolveMigrationTarget(rc, toVersion)
			if err != nil {
				return err
			}
			return runDataMigrateList(cmd, rc, target, format)
		},
	}
	cmd.Flags().StringVar(&toVersion, "to-version", "", "Concrete vX.Y.Z; defaults to cluster's resolved platform version")
	cmd.Flags().StringVar(&format, "format", "text", "Output format: text|json")
	return cmd
}

func runDataMigrateList(cmd *cobra.Command, rc *resolvedCluster, targetVersion, format string) error {
	catalog := releases.Catalog()
	if len(catalog) == 0 {
		if format == "json" {
			return jsonEncode(cmd, map[string]any{
				"target_version": targetVersion,
				"requirements":   []any{},
				"message":        "release catalog is empty; no required data migrations declared",
			})
		}
		fmt.Fprintf(cmd.OutOrStdout(), "release catalog is empty; no required data migrations declared up to %s\n", targetVersion)
		return nil
	}

	reqs := preflight.CatalogRequirements(catalog, targetVersion)
	if len(reqs) == 0 {
		if format == "json" {
			return jsonEncode(cmd, map[string]any{
				"target_version": targetVersion,
				"requirements":   []any{},
				"message":        "no required data migrations declared",
			})
		}
		fmt.Fprintf(cmd.OutOrStdout(), "no required data migrations declared up to %s\n", targetVersion)
		return nil
	}

	sshKey := stringFlag(cmd, "ssh-key").Value
	pool := ssh.NewPool(30*time.Second, sshKey)
	defer pool.Close()

	src := preflight.SSHStateSource(pool, manifestHostFor(rc.Manifest), manifestRuntimeFor(rc.Manifest))
	type entry struct {
		ID                  string             `json:"id"`
		Service             string             `json:"service"`
		IntroducedIn        string             `json:"introduced_in"`
		RequiredBeforePhase string             `json:"required_before_phase"`
		Status              datamigrate.Status `json:"status,omitempty"`
		NotRegistered       bool               `json:"not_registered,omitempty"`
		NotAdopted          bool               `json:"not_adopted,omitempty"`
		Error               string             `json:"error,omitempty"`
	}
	out := make([]entry, 0, len(reqs))
	for _, r := range reqs {
		live := src(cmd.Context(), r.Service, r.ID)
		e := entry{
			ID: r.ID, Service: r.Service, IntroducedIn: r.IntroducedIn, RequiredBeforePhase: r.RequiredBeforePhase,
		}
		switch {
		case live.FetchError != nil:
			e.Error = live.FetchError.Error()
		case live.NotAdopted:
			e.NotAdopted = true
		case live.NotRegistered:
			e.NotRegistered = true
		default:
			e.Status = live.Status
		}
		out = append(out, e)
	}

	if format == "json" {
		return jsonEncode(cmd, map[string]any{
			"target_version": targetVersion,
			"requirements":   out,
		})
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Required data migrations up to %s:\n", targetVersion)
	for _, e := range out {
		switch {
		case e.Error != "":
			fmt.Fprintf(cmd.OutOrStdout(), "  %s/%s (introduced %s, required before %s) — fetch failed: %s\n",
				e.Service, e.ID, e.IntroducedIn, e.RequiredBeforePhase, e.Error)
		case e.NotAdopted:
			fmt.Fprintf(cmd.OutOrStdout(), "  %s/%s (introduced %s) — service binary has NOT ADOPTED data-migrations\n",
				e.Service, e.ID, e.IntroducedIn)
		case e.NotRegistered:
			fmt.Fprintf(cmd.OutOrStdout(), "  %s/%s (introduced %s) — DECLARED in catalog but NOT REGISTERED in service binary\n",
				e.Service, e.ID, e.IntroducedIn)
		default:
			fmt.Fprintf(cmd.OutOrStdout(), "  %s/%s (introduced %s, required_before_phase=%s) — status: %s\n",
				e.Service, e.ID, e.IntroducedIn, e.RequiredBeforePhase, e.Status)
		}
	}
	return nil
}

func newDMStatusCmd() *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "status <service>.<id>",
		Short: "Show persisted state for one data migration",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return err
			}
			defer rc.Cleanup()
			service, id, err := parseServiceID(args[0])
			if err != nil {
				return err
			}
			return runDataMigrateRemote(cmd, rc, service, []string{"data-migrations", "status", id, "--format", format})
		},
	}
	cmd.Flags().StringVar(&format, "format", "text", "Output format: text|json")
	return cmd
}

func newDMRunCmd() *cobra.Command {
	var batchSize int
	var dryRun bool
	var scopeKind, scopeValue string
	cmd := &cobra.Command{
		Use:   "run <service>.<id>",
		Short: "Run one data migration to completion",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return err
			}
			defer rc.Cleanup()
			service, id, err := parseServiceID(args[0])
			if err != nil {
				return err
			}
			remoteArgs := []string{"data-migrations", "run", id,
				"--batch-size", fmt.Sprintf("%d", batchSize)}
			if dryRun {
				remoteArgs = append(remoteArgs, "--dry-run")
			}
			if scopeKind != "" {
				remoteArgs = append(remoteArgs, "--scope-kind", scopeKind, "--scope-value", scopeValue)
			}
			return runDataMigrateRemote(cmd, rc, service, remoteArgs)
		},
	}
	cmd.Flags().IntVar(&batchSize, "batch-size", 1000, "Batch size hint")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Run inside a read-only database transaction")
	cmd.Flags().StringVar(&scopeKind, "scope-kind", "", "Scope partition kind")
	cmd.Flags().StringVar(&scopeValue, "scope-value", "", "Scope partition value")
	return cmd
}

func newDMVerifyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "verify <service>.<id>",
		Short: "Run the migration's read-only verification on the service host",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return err
			}
			defer rc.Cleanup()
			service, id, err := parseServiceID(args[0])
			if err != nil {
				return err
			}
			return runDataMigrateRemote(cmd, rc, service, []string{"data-migrations", "verify", id})
		},
	}
}

func newDMPauseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pause <service>.<id>",
		Short: "Mark a data migration paused",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return err
			}
			defer rc.Cleanup()
			service, id, err := parseServiceID(args[0])
			if err != nil {
				return err
			}
			return runDataMigrateRemote(cmd, rc, service, []string{"data-migrations", "pause", id})
		},
	}
}

func newDMResumeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "resume <service>.<id>",
		Short: "Mark a paused data migration runnable again",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return err
			}
			defer rc.Cleanup()
			service, id, err := parseServiceID(args[0])
			if err != nil {
				return err
			}
			return runDataMigrateRemote(cmd, rc, service, []string{"data-migrations", "resume", id})
		},
	}
}

// runDataMigrateRemote SSHes into service's host, detects mode, and invokes
// the service binary with the given args. Streams stdout/stderr through.
func runDataMigrateRemote(cmd *cobra.Command, rc *resolvedCluster, service string, args []string) error {
	host, ok := manifestHostFor(rc.Manifest)(service)
	if !ok {
		return fmt.Errorf("no host for service %q in manifest", service)
	}
	runtime := manifestRuntimeFor(rc.Manifest)(service)

	sshKey := stringFlag(cmd, "ssh-key").Value
	pool := ssh.NewPool(30*time.Second, sshKey)
	defer pool.Close()

	ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Minute)
	defer cancel()

	adopted, err := remoteDataMigrationsAdopted(ctx, pool, host, runtime)
	if err != nil {
		return fmt.Errorf("check data-migrations adoption for %s: %w", runtime, err)
	}
	if !adopted {
		return fmt.Errorf("%s has not adopted data-migrations (missing %s)", service, datamigrate.AdoptionMarkerPath(runtime))
	}

	detector := detect.NewDetector(pool, host)
	state, err := detector.Detect(ctx, runtime)
	if err != nil {
		return fmt.Errorf("detect %s: %w", runtime, err)
	}
	mode := exec.Mode(state.Mode)
	if mode != exec.ModeDocker {
		mode = exec.ModeNative
	}
	shellCmd, err := exec.Command(exec.Spec{Mode: mode, ContainerName: state.Metadata["container_name"], BinaryName: runtime}, args)
	if err != nil {
		return err
	}

	cfg := &ssh.ConnectionConfig{
		Address:  host.ExternalIP,
		Port:     22,
		User:     host.User,
		HostName: host.Name,
		Timeout:  30 * time.Second,
	}
	result, err := pool.Run(ctx, cfg, shellCmd)
	if err != nil {
		return fmt.Errorf("ssh run: %w", err)
	}
	if strings.TrimSpace(result.Stdout) != "" {
		fmt.Fprint(cmd.OutOrStdout(), result.Stdout)
		if !strings.HasSuffix(result.Stdout, "\n") {
			fmt.Fprintln(cmd.OutOrStdout())
		}
	}
	if strings.TrimSpace(result.Stderr) != "" {
		fmt.Fprint(cmd.OutOrStderr(), result.Stderr)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("%s data-migrations exit %d", service, result.ExitCode)
	}
	return nil
}

func remoteDataMigrationsAdopted(ctx context.Context, pool *ssh.Pool, host inventory.Host, runtime string) (bool, error) {
	runCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cfg := &ssh.ConnectionConfig{
		Address:  host.ExternalIP,
		Port:     22,
		User:     host.User,
		HostName: host.Name,
		Timeout:  30 * time.Second,
	}
	result, err := pool.Run(runCtx, cfg, "test -f "+exec.ShellQuote(datamigrate.AdoptionMarkerPath(runtime)))
	if err != nil {
		return false, fmt.Errorf("ssh run: %w", err)
	}
	return result.ExitCode == 0, nil
}

// parseServiceID splits "service.id" into its parts. Both halves required.
func parseServiceID(arg string) (string, string, error) {
	idx := strings.Index(arg, ".")
	if idx <= 0 || idx == len(arg)-1 {
		return "", "", fmt.Errorf("expected <service>.<id>, got %q", arg)
	}
	return arg[:idx], arg[idx+1:], nil
}

func jsonEncode(cmd *cobra.Command, v any) error {
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
