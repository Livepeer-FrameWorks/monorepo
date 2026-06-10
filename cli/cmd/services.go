package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	fwcfg "frameworks/cli/internal/config"
	"frameworks/cli/internal/controlplane"
	"frameworks/cli/internal/services"
	"frameworks/cli/internal/ux"
	"frameworks/cli/internal/xexec"
	"frameworks/cli/pkg/provisioner"
	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/configgen"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"

	"github.com/spf13/cobra"
)

func newServicesCmd() *cobra.Command {
	svc := &cobra.Command{
		Use:   "services",
		Short: "Central-tier planning and operations",
	}
	svc.AddCommand(newServicesPlanCmd())
	svc.AddCommand(newServicesUpCmd())
	svc.AddCommand(newServicesDownCmd())
	svc.AddCommand(newServicesStatusCmd())
	svc.AddCommand(newServicesLogsCmd())
	svc.AddCommand(newServicesHealthCmd())
	svc.AddCommand(newServicesDiscoverCmd())
	return svc
}

func newQMGRPCClientFromContext(ctx context.Context) (*qmclient.GRPCClient, fwcfg.Context, func(), error) {
	ctxCfg, err := activeContextWithAuth(ctx)
	if err != nil {
		return nil, fwcfg.Context{}, nil, err
	}

	ep, err := controlplane.ResolveGRPC(ctx, ctxCfg, "quartermaster")
	if err != nil {
		return nil, fwcfg.Context{}, nil, err
	}

	qc, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{
		GRPCAddr:      ep.Address,
		Timeout:       15 * time.Second,
		Logger:        logging.NewLogger(),
		ServiceToken:  ctxCfg.Auth.ServiceToken,
		AllowInsecure: ep.AllowInsecure,
		ServerName:    ep.ServerName,
	})
	if err != nil {
		ep.Cleanup()
		return nil, fwcfg.Context{}, nil, fmt.Errorf("failed to connect to Quartermaster gRPC: %w", err)
	}
	return qc, ctxCfg, ep.Cleanup, nil
}

// servicesQMClient is the narrow Quartermaster surface the health/discover
// handlers call. *qmclient.GRPCClient satisfies it; tests inject a fake.
type servicesQMClient interface {
	GetServiceHealth(ctx context.Context, serviceID string) (*quartermasterpb.ListServicesHealthResponse, error)
	ListServicesHealth(ctx context.Context, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ListServicesHealthResponse, error)
	DiscoverServices(ctx context.Context, serviceType, clusterID string, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ServiceDiscoveryResponse, error)
}

func newServicesHealthCmd() *cobra.Command {
	var serviceID string
	var svcType string
	cmd := &cobra.Command{Use: "health", Short: "Show aggregated service health", RunE: func(cmd *cobra.Command, args []string) error {
		qc, _, cleanup, err := newQMGRPCClientFromContext(cmd.Context())
		if err != nil {
			return err
		}
		defer cleanup()
		defer qc.Close()
		ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
		defer cancel()
		return runServicesHealth(ctx, cmd.OutOrStdout(), qc, serviceID, svcType, output == "json")
	}}
	cmd.Flags().StringVar(&serviceID, "service-id", "", "filter by service ID")
	cmd.Flags().StringVar(&svcType, "type", "", "filter by service type (catalog name)")
	return cmd
}

// serviceHealthRow is one flattened health instance for text rendering.
type serviceHealthRow struct {
	ServiceID      string
	InstanceID     string
	Status         string
	Host           string
	Port           int32
	HealthEndpoint string
}

// runServicesHealth resolves health instances by the active filter
// (service-id > type > all), then renders them as JSON or a sorted,
// status-colored list. Seam-extracted so the filter/render contract is
// unit-tested with a fake Quartermaster client.
func runServicesHealth(ctx context.Context, w io.Writer, qc servicesQMClient, serviceID, svcType string, outputJSON bool) error {
	var resp any
	var instances []serviceHealthRow

	collect := func(insts []*quartermasterpb.ServiceInstanceHealth) {
		for _, h := range insts {
			host := ""
			if h.Host != nil {
				host = *h.Host
			}
			ep := ""
			if h.HealthEndpoint != nil {
				ep = *h.HealthEndpoint
			}
			instances = append(instances, serviceHealthRow{
				ServiceID:      h.ServiceId,
				InstanceID:     h.InstanceId,
				Status:         h.Status,
				Host:           host,
				Port:           h.Port,
				HealthEndpoint: ep,
			})
		}
	}

	switch {
	case strings.TrimSpace(serviceID) != "":
		healthResp, err := qc.GetServiceHealth(ctx, serviceID)
		if err != nil {
			return err
		}
		resp = healthResp
		collect(healthResp.Instances)
	case strings.TrimSpace(svcType) != "":
		// Discover services by type first, then get health for each service ID.
		discResp, err := qc.DiscoverServices(ctx, svcType, "", nil)
		if err != nil {
			return err
		}
		ids := map[string]struct{}{}
		for _, inst := range discResp.Instances {
			ids[inst.ServiceId] = struct{}{}
		}
		for id := range ids {
			healthResp, err := qc.GetServiceHealth(ctx, id)
			if err != nil {
				return err
			}
			collect(healthResp.Instances)
		}
		resp = map[string]any{"instances": instances, "total": len(instances)}
	default:
		healthResp, err := qc.ListServicesHealth(ctx, nil)
		if err != nil {
			return err
		}
		resp = healthResp
		collect(healthResp.Instances)
	}

	if outputJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(resp)
	}

	// Sort by service then instance.
	sort.SliceStable(instances, func(i, j int) bool {
		if instances[i].ServiceID == instances[j].ServiceID {
			return instances[i].InstanceID < instances[j].InstanceID
		}
		return instances[i].ServiceID < instances[j].ServiceID
	})
	ux.Heading(w, fmt.Sprintf("Service Health (%d instances)", len(instances)))
	for _, h := range instances {
		line := fmt.Sprintf("%-12s inst=%-10s %s:%d %s [%s]", h.ServiceID, h.InstanceID, h.Host, h.Port, h.HealthEndpoint, h.Status)
		switch strings.ToLower(h.Status) {
		case "ok", "healthy":
			ux.Success(w, line)
		case "degraded", "warning":
			ux.Warn(w, line)
		default:
			ux.Fail(w, line)
		}
	}
	return nil
}

func newServicesDiscoverCmd() *cobra.Command {
	var svcType string
	var clusterID string
	cmd := &cobra.Command{Use: "discover", Short: "Discover service instances", RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(svcType) == "" {
			return fmt.Errorf("--type is required")
		}
		qc, _, cleanup, err := newQMGRPCClientFromContext(cmd.Context())
		if err != nil {
			return err
		}
		defer cleanup()
		defer qc.Close()
		ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
		defer cancel()
		return runServicesDiscover(ctx, cmd.OutOrStdout(), qc, svcType, clusterID, output == "json")
	}}
	cmd.Flags().StringVar(&svcType, "type", "", "service type (catalog name)")
	cmd.Flags().StringVar(&clusterID, "cluster-id", "", "optional cluster filter")
	return cmd
}

// runServicesDiscover queries Quartermaster for instances of svcType and renders
// them (JSON or a sorted text list). Seam-extracted so the render contract is
// unit-tested with a fake client.
func runServicesDiscover(ctx context.Context, w io.Writer, qc servicesQMClient, svcType, clusterID string, outputJSON bool) error {
	resp, err := qc.DiscoverServices(ctx, svcType, clusterID, nil)
	if err != nil {
		return err
	}
	if outputJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(resp)
	}
	fmt.Fprintf(w, "Discovered %d instance(s) of %s\n", len(resp.Instances), svcType)
	sort.SliceStable(resp.Instances, func(i, j int) bool { return resp.Instances[i].InstanceId < resp.Instances[j].InstanceId })
	for _, inst := range resp.Instances {
		ver := ""
		if inst.Version != nil {
			ver = *inst.Version
		}
		port := int32(0)
		if inst.Port != nil {
			port = *inst.Port
		}
		fmt.Fprintf(w, " - inst=%-10s svc=%-12s cluster=%-8s version=%-8s port=%d status=%s\n",
			inst.InstanceId, inst.ServiceId, inst.ClusterId, ver, port, inst.HealthStatus)
	}
	return nil
}

func newServicesPlanCmd() *cobra.Command {
	var profile string
	var include string
	var exclude string
	var dir string
	var overwrite bool
	var interactive bool
	var configBase string
	var configSecrets string
	var envOutput string
	var envContext string
	var version string
	var unpinned bool
	cmd := &cobra.Command{Use: "plan", Short: "Generate central-tier compose (.yml + .env)", RunE: func(cmd *cobra.Command, args []string) error {
		if dir == "" {
			dir = "."
		}
		c, err := services.LoadCatalog()
		if err != nil {
			return err
		}
		var specs []services.ServiceSpec
		if interactive {
			specs, profile, err = services.InteractiveSelect(c)
			if err != nil {
				return err
			}
		} else {
			specs, err = services.ResolveSelection(c, profile, include, exclude)
			if err != nil {
				return err
			}
		}
		// Resolve each spec's image from the gitops manifest so the
		// generated compose pins by @digest, not the catalog's :latest.
		// --unpinned keeps the catalog values (escape hatch for offline
		// dev or for inspecting what the catalog itself ships).
		if !unpinned {
			specs, err = pinServiceSpecsToManifest(specs, version)
			if err != nil {
				return err
			}
		}
		// Write per-service fragments under dir
		if err := services.GenerateFragments(dir, specs, overwrite); err != nil {
			return err
		}
		// Write env and plan
		if configBase == "" {
			configBase = "config/env/base.env"
		}
		if configSecrets == "" {
			configSecrets = "config/env/secrets.env"
		}
		if envContext == "" {
			envContext = "central"
		}
		if envOutput == "" {
			envOutput = filepath.Join(dir, ".central.env")
		}
		configBase = filepath.Clean(configBase)
		configSecrets = filepath.Clean(configSecrets)
		envOutput = filepath.Clean(envOutput)
		if !overwrite {
			if _, err := os.Stat(envOutput); err == nil {
				return fmt.Errorf("file exists: %s (use --overwrite)", envOutput)
			}
		}
		if _, err := configgen.Generate(configgen.Options{
			BaseFile:    configBase,
			SecretsFile: configSecrets,
			OutputFile:  envOutput,
			Context:     envContext,
		}); err != nil {
			return err
		}
		if err := services.SavePlan(dir, specs, profile); err != nil {
			return err
		}
		ux.Success(cmd.OutOrStdout(), fmt.Sprintf("Wrote service fragments to %s and %s", dir, envOutput))
		fmt.Fprintln(cmd.OutOrStdout(), "Selection:")
		fmt.Fprint(cmd.OutOrStdout(), services.SummarizeSelection(specs))
		return nil
	}}
	cmd.Flags().StringVar(&profile, "profile", "central-all", "profile preset (central-all|platform|control|data|media|edge|interfaces|observability)")
	cmd.Flags().StringVar(&include, "include", "", "comma-separated services to include")
	cmd.Flags().StringVar(&exclude, "exclude", "", "comma-separated services to exclude")
	cmd.Flags().StringVar(&dir, "dir", ".", "target directory for generated files")
	cmd.Flags().BoolVar(&overwrite, "overwrite", false, "overwrite existing files")
	cmd.Flags().BoolVar(&interactive, "interactive", false, "interactive checkbox-like selection")
	cmd.Flags().StringVar(&configBase, "config-base", "", "path to base env file (default: config/env/base.env)")
	cmd.Flags().StringVar(&configSecrets, "config-secrets", "", "path to secrets env file (default: config/env/secrets.env)")
	cmd.Flags().StringVar(&envOutput, "env-output", "", "path for generated env file (default: <dir>/.central.env)")
	cmd.Flags().StringVar(&envContext, "env-context", "central", "value for ENV_CONTEXT in the generated env file")
	cmd.Flags().StringVar(&version, "version", "stable", "release channel (stable|rc) or pinned version (vX.Y.Z) for image digest resolution")
	cmd.Flags().BoolVar(&unpinned, "unpinned", false, "skip manifest resolution and emit the catalog's image strings verbatim (offline/dev escape hatch)")
	return cmd
}

// pinServiceSpecsToManifest resolves each ServiceSpec.Image from the gitops
// release manifest so generated compose fragments pin by image@digest. It
// fails closed: callers that intentionally want catalog tags use --unpinned.
func pinServiceSpecsToManifest(specs []services.ServiceSpec, version string) ([]services.ServiceSpec, error) {
	if strings.TrimSpace(version) == "" {
		version = "stable"
	}
	resolved := make([]services.ServiceSpec, len(specs))
	for i, s := range specs {
		resolved[i] = s
		deploy := s.Deploy
		if deploy == "" {
			deploy = s.Name
		}
		img, err := provisioner.ImageFromReleaseManifest(deploy, version, nil)
		if err != nil {
			return nil, fmt.Errorf("resolve %s image from release manifest (%s): %w (use --unpinned for local/dev catalog tags)", deploy, version, err)
		}
		resolved[i].Image = img
	}
	return resolved, nil
}

func newServicesUpCmd() *cobra.Command {
	var dir string
	var only string
	var sshTarget string
	var sshKey string
	cmd := &cobra.Command{Use: "up", Short: "Start selected central services", RunE: func(cmd *cobra.Command, args []string) error {
		if dir == "" {
			dir = "."
		}
		// build -f list from fragments according to selection
		selected := []string{}
		if strings.TrimSpace(only) != "" {
			selected = strings.Split(only, ",")
		}
		list, err := services.ResolveServiceList(dir, selected)
		if err != nil {
			return err
		}
		dockerArgs := []string{"compose"}
		for _, s := range list {
			dockerArgs = append(dockerArgs, "-f", fmt.Sprintf("svc-%s.yml", strings.TrimSpace(s)))
		}
		dockerArgs = append(dockerArgs, "--env-file", ".central.env", "up", "-d")
		var out, errOut string
		if strings.TrimSpace(sshTarget) != "" {
			_, out, errOut, err = xexec.RunSSHWithKey(cmd.Context(), sshTarget, sshKey, "docker", dockerArgs, dir)
		} else {
			_, out, errOut, err = xexec.Run(cmd.Context(), "docker", dockerArgs, dir)
		}
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "compose up error: %v\n%s\n%s\n", err, out, errOut)
			return err
		}
		fmt.Fprint(cmd.OutOrStdout(), out)
		return nil
	}}
	cmd.Flags().StringVar(&dir, "dir", ".", "directory containing generated compose")
	cmd.Flags().StringVar(&only, "only", "", "comma-separated services to start")
	cmd.Flags().StringVar(&sshTarget, "ssh", "", "run remotely on user@host via SSH")
	cmd.Flags().StringVar(&sshKey, "ssh-key", "", "SSH private key path")
	return cmd
}

func newServicesDownCmd() *cobra.Command {
	var dir string
	var only string
	var sshTarget string
	var sshKey string
	var yes bool
	cmd := &cobra.Command{Use: "down", Short: "Stop selected central services", RunE: func(cmd *cobra.Command, args []string) error {
		if dir == "" {
			dir = "."
		}

		// Determine what services will be stopped
		var servicesToStop []string
		var err error
		if strings.TrimSpace(only) == "" {
			servicesToStop, err = services.ResolveServiceList(dir, nil)
			if err != nil {
				return err
			}
		} else {
			for _, s := range strings.Split(only, ",") {
				s = strings.TrimSpace(s)
				if s != "" {
					servicesToStop = append(servicesToStop, s)
				}
			}
		}

		if len(servicesToStop) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "No services to stop")
			return nil
		}

		// Require confirmation for stopping services (destructive operation)
		if !yes {
			fmt.Fprintf(os.Stderr, "\nThis will stop the following services: %s\n", strings.Join(servicesToStop, ", "))
			fmt.Fprintf(os.Stderr, "Continue? [y/N]: ")
			reader := bufio.NewReader(os.Stdin)
			response, errRead := reader.ReadString('\n')
			if errRead != nil {
				return fmt.Errorf("failed to read confirmation: %w", errRead)
			}
			response = strings.TrimSpace(strings.ToLower(response))
			if response != "y" && response != "yes" {
				fmt.Fprintln(cmd.OutOrStdout(), "Cancelled")
				return nil
			}
		}

		if strings.TrimSpace(only) == "" {
			// down all fragments in plan or dir
			dockerArgs := []string{"compose"}
			for _, s := range servicesToStop {
				dockerArgs = append(dockerArgs, "-f", fmt.Sprintf("svc-%s.yml", strings.TrimSpace(s)))
			}
			dockerArgs = append(dockerArgs, "--env-file", ".central.env", "down")
			var out, errOut string
			if strings.TrimSpace(sshTarget) != "" {
				_, out, errOut, err = xexec.RunSSHWithKey(cmd.Context(), sshTarget, sshKey, "docker", dockerArgs, dir)
			} else {
				_, out, errOut, err = xexec.Run(cmd.Context(), "docker", dockerArgs, dir)
			}
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "compose down error: %v\n%s\n%s\n", err, out, errOut)
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), out)
			return nil
		}
		// Stop specific services
		for _, s := range servicesToStop {
			dockerArgs := []string{"compose", "-f", fmt.Sprintf("svc-%s.yml", s), "--env-file", ".central.env", "stop", s}
			var out, errOut string
			if strings.TrimSpace(sshTarget) != "" {
				_, out, errOut, err = xexec.RunSSHWithKey(cmd.Context(), sshTarget, sshKey, "docker", dockerArgs, dir)
			} else {
				_, out, errOut, err = xexec.Run(cmd.Context(), "docker", dockerArgs, dir)
			}
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "compose stop %s error: %v\n%s\n%s\n", s, err, out, errOut)
			}
			fmt.Fprint(cmd.OutOrStdout(), out)
		}
		return nil
	}}
	cmd.Flags().StringVar(&dir, "dir", ".", "directory containing generated compose")
	cmd.Flags().StringVar(&only, "only", "", "comma-separated services to stop")
	cmd.Flags().StringVar(&sshTarget, "ssh", "", "run remotely on user@host via SSH")
	cmd.Flags().StringVar(&sshKey, "ssh-key", "", "SSH private key path")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Skip confirmation prompt")
	return cmd
}

func newServicesStatusCmd() *cobra.Command {
	var dir string
	var only string
	var sshTarget string
	var sshKey string
	cmd := &cobra.Command{Use: "status", Short: "Show service container status", RunE: func(cmd *cobra.Command, args []string) error {
		if dir == "" {
			dir = "."
		}
		selected := []string{}
		if strings.TrimSpace(only) != "" {
			selected = strings.Split(only, ",")
		}
		list, err := services.ResolveServiceList(dir, selected)
		if err != nil {
			return err
		}
		dockerArgs := []string{"compose"}
		for _, s := range list {
			dockerArgs = append(dockerArgs, "-f", fmt.Sprintf("svc-%s.yml", strings.TrimSpace(s)))
		}
		dockerArgs = append(dockerArgs, "--env-file", ".central.env", "ps")
		var out, errOut string
		if strings.TrimSpace(sshTarget) != "" {
			_, out, errOut, err = xexec.RunSSHWithKey(cmd.Context(), sshTarget, sshKey, "docker", dockerArgs, dir)
		} else {
			_, out, errOut, err = xexec.Run(cmd.Context(), "docker", dockerArgs, dir)
		}
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "compose ps error: %v\n%s\n%s\n", err, out, errOut)
			return err
		}
		fmt.Fprint(cmd.OutOrStdout(), out)
		return nil
	}}
	cmd.Flags().StringVar(&dir, "dir", ".", "directory containing generated compose")
	cmd.Flags().StringVar(&only, "only", "", "comma-separated services to filter")
	cmd.Flags().StringVar(&sshTarget, "ssh", "", "run remotely on user@host via SSH")
	cmd.Flags().StringVar(&sshKey, "ssh-key", "", "SSH private key path")
	return cmd
}

func newServicesLogsCmd() *cobra.Command {
	var dir string
	var only string
	var follow bool
	var tail int
	var sshTarget string
	var sshKey string
	cmd := &cobra.Command{Use: "logs", Short: "Show service logs", RunE: func(cmd *cobra.Command, args []string) error {
		if dir == "" {
			dir = "."
		}
		selected := []string{}
		if strings.TrimSpace(only) != "" {
			selected = strings.Split(only, ",")
		}
		list, err := services.ResolveServiceList(dir, selected)
		if err != nil {
			return err
		}
		dockerArgs := []string{"compose"}
		for _, s := range list {
			dockerArgs = append(dockerArgs, "-f", fmt.Sprintf("svc-%s.yml", strings.TrimSpace(s)))
		}
		dockerArgs = append(dockerArgs, "--env-file", ".central.env", "logs")
		if follow {
			dockerArgs = append(dockerArgs, "-f")
		}
		if tail > 0 {
			dockerArgs = append(dockerArgs, "--tail", fmt.Sprintf("%d", tail))
		}
		if strings.TrimSpace(only) != "" {
			dockerArgs = append(dockerArgs, strings.Split(only, ",")...)
		}
		var out, errOut string
		if strings.TrimSpace(sshTarget) != "" {
			_, out, errOut, err = xexec.RunSSHWithKey(cmd.Context(), sshTarget, sshKey, "docker", dockerArgs, dir)
		} else {
			_, out, errOut, err = xexec.Run(cmd.Context(), "docker", dockerArgs, dir)
		}
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "compose logs error: %v\n%s\n%s\n", err, out, errOut)
			return err
		}
		fmt.Fprint(cmd.OutOrStdout(), out)
		return nil
	}}
	cmd.Flags().StringVar(&dir, "dir", ".", "directory containing generated compose")
	cmd.Flags().StringVar(&only, "only", "", "comma-separated services to filter")
	cmd.Flags().BoolVar(&follow, "follow", false, "follow logs (tail) ")
	cmd.Flags().IntVar(&tail, "tail", 200, "number of lines to show (per service)")
	cmd.Flags().StringVar(&sshTarget, "ssh", "", "run remotely on user@host via SSH")
	cmd.Flags().StringVar(&sshKey, "ssh-key", "", "SSH private key path")
	return cmd
}
