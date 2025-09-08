package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	fwcfg "frameworks/cli/internal/config"
	"frameworks/cli/internal/services"
	"frameworks/cli/internal/xexec"
	qmapi "frameworks/pkg/api/quartermaster"
	qmclient "frameworks/pkg/clients/quartermaster"
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

func newQMClientFromContext() (*qmclient.Client, fwcfg.Context, error) {
	cfg, _, err := fwcfg.Load()
	if err != nil {
		return nil, fwcfg.Context{}, err
	}
	ctx := fwcfg.GetCurrent(cfg)
	qc := qmclient.NewClient(qmclient.Config{BaseURL: ctx.Endpoints.QuartermasterURL, ServiceToken: ctx.Auth.ServiceToken, Timeout: 15 * time.Second})
	return qc, ctx, nil
}

func newServicesHealthCmd() *cobra.Command {
	var serviceID string
	var svcType string
	cmd := &cobra.Command{Use: "health", Short: "Show aggregated service health", RunE: func(cmd *cobra.Command, args []string) error {
		qc, _, err := newQMClientFromContext()
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var resp *qmapi.ServicesHealthResponse
		if strings.TrimSpace(serviceID) != "" {
			resp, err = qc.GetServiceHealth(ctx, serviceID)
			if err != nil {
				return err
			}
		} else if strings.TrimSpace(svcType) != "" {
			// Discover to find service IDs for this type
			disc, err := qc.ServiceDiscovery(ctx, svcType, nil)
			if err != nil {
				return err
			}
			ids := map[string]struct{}{}
			for _, inst := range disc.Instances {
				ids[inst.ServiceID] = struct{}{}
			}
			agg := qmapi.ServicesHealthResponse{}
			for id := range ids {
				r, err := qc.GetServiceHealth(ctx, id)
				if err != nil {
					return err
				}
				agg.Instances = append(agg.Instances, r.Instances...)
			}
			agg.Count = len(agg.Instances)
			resp = &agg
		} else {
			resp, err = qc.GetServicesHealth(ctx)
			if err != nil {
				return err
			}
		}
		// Output
		if output == "json" {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(resp)
		}
		// text
		// Sort by service then status
		list := resp.Instances
		sort.SliceStable(list, func(i, j int) bool {
			if list[i].ServiceID == list[j].ServiceID {
				return list[i].InstanceID < list[j].InstanceID
			}
			return list[i].ServiceID < list[j].ServiceID
		})
		fmt.Fprintf(cmd.OutOrStdout(), "Service Health (%d instances)\n", resp.Count)
		for _, h := range list {
			mark := "✗"
			if strings.ToLower(h.Status) == "ok" || strings.ToLower(h.Status) == "healthy" {
				mark = "✓"
			}
			host := ""
			if h.Host != nil {
				host = *h.Host
			}
			ep := ""
			if h.HealthEndpoint != nil {
				ep = *h.HealthEndpoint
			}
			fmt.Fprintf(cmd.OutOrStdout(), " %s %-12s inst=%-10s %s:%d %s [%s]\n", mark, h.ServiceID, h.InstanceID, host, h.Port, ep, h.Status)
		}
		return nil
	}}
	cmd.Flags().StringVar(&serviceID, "service-id", "", "filter by service ID")
	cmd.Flags().StringVar(&svcType, "type", "", "filter by service type (catalog name)")
	return cmd
}

func newServicesDiscoverCmd() *cobra.Command {
	var svcType string
	var clusterID string
	cmd := &cobra.Command{Use: "discover", Short: "Discover service instances", RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(svcType) == "" {
			return fmt.Errorf("--type is required")
		}
		qc, _, err := newQMClientFromContext()
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var cid *string
		if strings.TrimSpace(clusterID) != "" {
			cid = &clusterID
		}
		resp, err := qc.ServiceDiscovery(ctx, svcType, cid)
		if err != nil {
			return err
		}
		if output == "json" {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(resp)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Discovered %d instance(s) of %s\n", resp.Count, svcType)
		sort.SliceStable(resp.Instances, func(i, j int) bool { return resp.Instances[i].InstanceID < resp.Instances[j].InstanceID })
		for _, inst := range resp.Instances {
			ver := ""
			if inst.Version != nil {
				ver = *inst.Version
			}
			port := 0
			if inst.Port != nil {
				port = *inst.Port
			}
			fmt.Fprintf(cmd.OutOrStdout(), " - inst=%-10s svc=%-12s cluster=%-8s version=%-8s port=%d status=%s\n",
				inst.InstanceID, inst.ServiceID, inst.ClusterID, ver, port, inst.HealthStatus)
		}
		return nil
	}}
	cmd.Flags().StringVar(&svcType, "type", "", "service type (catalog name)")
	cmd.Flags().StringVar(&clusterID, "cluster-id", "", "optional cluster filter")
	return cmd
}

func newServicesPlanCmd() *cobra.Command {
	var profile string
	var include string
	var exclude string
	var dir string
	var overwrite bool
	var interactive bool
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
		// Write per-service fragments under dir
		if err := services.GenerateFragments(dir, specs, overwrite); err != nil {
			return err
		}
		// Write env and plan
		epath := dir + string(os.PathSeparator) + ".central.env"
		if !overwrite {
			if _, err := os.Stat(epath); err == nil {
				return fmt.Errorf("file exists: %s (use --overwrite)", epath)
			}
		}
		if err := os.WriteFile(epath, []byte(services.GenerateEnv(specs)), 0o644); err != nil {
			return err
		}
		if err := services.SavePlan(dir, specs, profile); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Wrote service fragments to %s and %s\n", dir, epath)
		fmt.Fprintln(cmd.OutOrStdout(), "Selection:")
		fmt.Fprint(cmd.OutOrStdout(), services.SummarizeSelection(specs))
		return nil
	}}
	cmd.Flags().StringVar(&profile, "profile", "central-all", "profile preset (central-all|control-core|routing-only|analytics-suite|billing-only)")
	cmd.Flags().StringVar(&include, "include", "", "comma-separated services to include")
	cmd.Flags().StringVar(&exclude, "exclude", "", "comma-separated services to exclude")
	cmd.Flags().StringVar(&dir, "dir", ".", "target directory for generated files")
	cmd.Flags().BoolVar(&overwrite, "overwrite", false, "overwrite existing files")
	cmd.Flags().BoolVar(&interactive, "interactive", false, "interactive checkbox-like selection")
	return cmd
}

func newServicesUpCmd() *cobra.Command {
	var dir string
	var only string
	var sshTarget string
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
			_, out, errOut, err = xexec.RunSSH(sshTarget, "docker", dockerArgs, dir)
		} else {
			_, out, errOut, err = xexec.Run("docker", dockerArgs, dir)
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
	return cmd
}

func newServicesDownCmd() *cobra.Command {
	var dir string
	var only string
	var sshTarget string
	cmd := &cobra.Command{Use: "down", Short: "Stop selected central services", RunE: func(cmd *cobra.Command, args []string) error {
		if dir == "" {
			dir = "."
		}
		var err error
		if strings.TrimSpace(only) == "" {
			// down all fragments in plan or dir
			list, err := services.ResolveServiceList(dir, nil)
			if err != nil {
				return err
			}
			dockerArgs := []string{"compose"}
			for _, s := range list {
				dockerArgs = append(dockerArgs, "-f", fmt.Sprintf("svc-%s.yml", strings.TrimSpace(s)))
			}
			dockerArgs = append(dockerArgs, "--env-file", ".central.env", "down")
			var out, errOut string
			if strings.TrimSpace(sshTarget) != "" {
				_, out, errOut, err = xexec.RunSSH(sshTarget, "docker", dockerArgs, dir)
			} else {
				_, out, errOut, err = xexec.Run("docker", dockerArgs, dir)
			}
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "compose down error: %v\n%s\n%s\n", err, out, errOut)
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), out)
			return nil
		}
		// Stop specific services
		svcs := strings.Split(only, ",")
		for _, s := range svcs {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			dockerArgs := []string{"compose", "-f", fmt.Sprintf("svc-%s.yml", s), "--env-file", ".central.env", "stop", s}
			var out, errOut string
			if strings.TrimSpace(sshTarget) != "" {
				_, out, errOut, err = xexec.RunSSH(sshTarget, "docker", dockerArgs, dir)
			} else {
				_, out, errOut, err = xexec.Run("docker", dockerArgs, dir)
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
	return cmd
}

func newServicesStatusCmd() *cobra.Command {
	var dir string
	var only string
	var sshTarget string
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
			_, out, errOut, err = xexec.RunSSH(sshTarget, "docker", dockerArgs, dir)
		} else {
			_, out, errOut, err = xexec.Run("docker", dockerArgs, dir)
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
	return cmd
}

func newServicesLogsCmd() *cobra.Command {
	var dir string
	var only string
	var follow bool
	var tail int
	var sshTarget string
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
			_, out, errOut, err = xexec.RunSSH(sshTarget, "docker", dockerArgs, dir)
		} else {
			_, out, errOut, err = xexec.Run("docker", dockerArgs, dir)
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
	return cmd
}
