package cmd

import (
	"fmt"
	fwcfg "frameworks/cli/internal/config"
	"github.com/spf13/cobra"
)

func newContextCmd() *cobra.Command {
	ctx := &cobra.Command{Use: "context", Short: "Manage CLI contexts (endpoints, auth, executor)"}
	ctx.AddCommand(newContextInitCmd())
	ctx.AddCommand(newContextListCmd())
	ctx.AddCommand(newContextUseCmd())
	ctx.AddCommand(newContextShowCmd())
	ctx.AddCommand(newContextSetURLCmd())
	ctx.AddCommand(newContextCheckCmd())
	ctx.AddCommand(newContextSetClusterCmd())
	return ctx
}

func newContextInitCmd() *cobra.Command {
	return &cobra.Command{Use: "init", Short: "Create default config with local endpoints", RunE: func(cmd *cobra.Command, args []string) error {
		path, _ := fwcfg.ConfigPath()
		cfg := fwcfg.Config{
			Current: "local",
			Contexts: map[string]fwcfg.Context{
				"local": {
					Name:      "local",
					Endpoints: fwcfg.DefaultEndpoints(),
					Executor:  fwcfg.Executor{Type: "local"},
				},
			},
		}
		if err := fwcfg.Save(cfg, path); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Initialized config at %s\n", path)
		return nil
	}}
}

func newContextListCmd() *cobra.Command {
	return &cobra.Command{Use: "list", Short: "List contexts", RunE: func(cmd *cobra.Command, args []string) error {
		cfg, _, err := fwcfg.Load()
		if err != nil {
			return err
		}
		for name := range cfg.Contexts {
			cur := " "
			if name == cfg.Current {
				cur = "*"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", cur, name)
		}
		return nil
	}}
}

func newContextUseCmd() *cobra.Command {
	return &cobra.Command{Use: "use <name>", Short: "Switch current context", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cfg, path, err := fwcfg.Load()
		if err != nil {
			return err
		}
		if _, ok := cfg.Contexts[args[0]]; !ok {
			return fmt.Errorf("unknown context: %s", args[0])
		}
		cfg.Current = args[0]
		if err := fwcfg.Save(cfg, path); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Now using context %q\n", args[0])
		return nil
	}}
}

func newContextShowCmd() *cobra.Command {
	return &cobra.Command{Use: "show [name]", Short: "Show context details", Args: cobra.RangeArgs(0, 1), RunE: func(cmd *cobra.Command, args []string) error {
		cfg, _, err := fwcfg.Load()
		if err != nil {
			return err
		}
		name := cfg.Current
		if len(args) == 1 {
			name = args[0]
		}
		c, ok := cfg.Contexts[name]
		if !ok {
			return fmt.Errorf("unknown context: %s", name)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Context: %s\n", name)
		if c.ClusterID != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "  cluster_id:          %s\n", c.ClusterID)
		}
		ep := c.Endpoints
		fmt.Fprintf(cmd.OutOrStdout(), "  bridge (http):       %s\n", ep.GatewayURL)
		fmt.Fprintf(cmd.OutOrStdout(), "  quartermaster http:  %s\n", ep.QuartermasterURL)
		fmt.Fprintf(cmd.OutOrStdout(), "  commodore http:      %s\n", ep.ControlURL)
		fmt.Fprintf(cmd.OutOrStdout(), "  foghorn http:        %s\n", ep.FoghornHTTPURL)
		fmt.Fprintf(cmd.OutOrStdout(), "  periscope query:     %s\n", ep.PeriscopeQueryURL)
		fmt.Fprintf(cmd.OutOrStdout(), "  periscope ingest:    %s\n", ep.PeriscopeIngestURL)
		fmt.Fprintf(cmd.OutOrStdout(), "  purser http:         %s\n", ep.PurserURL)
		fmt.Fprintf(cmd.OutOrStdout(), "  signalman ws:        %s\n", ep.SignalmanWSURL)
		fmt.Fprintf(cmd.OutOrStdout(), "  commodore grpc:      %s\n", ep.CommodoreGRPCAddr)
		fmt.Fprintf(cmd.OutOrStdout(), "  quartermaster grpc:  %s\n", ep.QuartermasterGRPCAddr)
		fmt.Fprintf(cmd.OutOrStdout(), "  purser grpc:         %s\n", ep.PurserGRPCAddr)
		fmt.Fprintf(cmd.OutOrStdout(), "  periscope grpc:      %s\n", ep.PeriscopeGRPCAddr)
		fmt.Fprintf(cmd.OutOrStdout(), "  signalman grpc:      %s\n", ep.SignalmanGRPCAddr)
		fmt.Fprintf(cmd.OutOrStdout(), "  foghorn grpc:        %s\n", ep.FoghornGRPCAddr)
		fmt.Fprintf(cmd.OutOrStdout(), "  decklog grpc:        %s\n", ep.DecklogGRPCAddr)
		fmt.Fprintf(cmd.OutOrStdout(), "  navigator grpc:      %s\n", ep.NavigatorGRPCAddr)
		fmt.Fprintf(cmd.OutOrStdout(), "Executor: %s\n", c.Executor.Type)
		return nil
	}}
}

func newContextSetURLCmd() *cobra.Command {
	return &cobra.Command{Use: "set-url <service> <url>", Short: "Update a service URL in the current context", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		svc, url := args[0], args[1]
		cfg, path, err := fwcfg.Load()
		if err != nil {
			return err
		}
		cur := fwcfg.GetCurrent(cfg)
		ep := cur.Endpoints
		switch svc {
		case "bridge":
			ep.GatewayURL = url
		case "quartermaster":
			ep.QuartermasterURL = url
		case "commodore":
			ep.ControlURL = url
		case "foghorn":
			ep.FoghornHTTPURL = url
		case "periscope-query":
			ep.PeriscopeQueryURL = url
		case "periscope-ingest":
			ep.PeriscopeIngestURL = url
		case "purser":
			ep.PurserURL = url
		case "signalman-ws":
			ep.SignalmanWSURL = url
		case "commodore-grpc":
			ep.CommodoreGRPCAddr = url
		case "quartermaster-grpc":
			ep.QuartermasterGRPCAddr = url
		case "purser-grpc":
			ep.PurserGRPCAddr = url
		case "periscope-grpc":
			ep.PeriscopeGRPCAddr = url
		case "signalman-grpc":
			ep.SignalmanGRPCAddr = url
		case "foghorn-grpc":
			ep.FoghornGRPCAddr = url
		case "decklog-grpc":
			ep.DecklogGRPCAddr = url
		case "navigator-grpc":
			ep.NavigatorGRPCAddr = url
		default:
			return fmt.Errorf("unknown service: %s", svc)
		}
		cur.Endpoints = ep
		cfg.Contexts[cur.Name] = cur
		if err := fwcfg.Save(cfg, path); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Updated %s to %s in context %q\n", svc, url, cur.Name)
		return nil
	}}
}

func newContextSetClusterCmd() *cobra.Command {
	return &cobra.Command{Use: "set-cluster <cluster-id>", Short: "Set the cluster ID for the current context", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cfg, path, err := fwcfg.Load()
		if err != nil {
			return err
		}
		cur := fwcfg.GetCurrent(cfg)
		cur.ClusterID = args[0]
		cfg.Contexts[cur.Name] = cur
		if err := fwcfg.Save(cfg, path); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Set cluster_id to %q in context %q\n", args[0], cur.Name)
		return nil
	}}
}
