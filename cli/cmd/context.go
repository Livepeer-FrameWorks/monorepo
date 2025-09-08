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
	return ctx
}

func newContextInitCmd() *cobra.Command {
	return &cobra.Command{Use: "init", Short: "Create default config with local endpoints", RunE: func(cmd *cobra.Command, args []string) error {
		cfg := fwcfg.Config{}
		path, _ := fwcfg.ConfigPath()
		cfg = fwcfg.Config{Current: "local", Contexts: map[string]fwcfg.Context{"local": fwcfg.Context{Name: "local", Endpoints: fwcfg.Endpoints{
			GatewayURL:         "http://localhost:18000",
			QuartermasterURL:   "http://localhost:18002",
			ControlURL:         "http://localhost:18001",
			FoghornHTTPURL:     "http://localhost:18008",
			FoghornGRPCAddr:    "localhost:18019",
			DecklogGRPCAddr:    "localhost:18006",
			PeriscopeQueryURL:  "http://localhost:18004",
			PeriscopeIngestURL: "http://localhost:18005",
			PurserURL:          "http://localhost:18003",
			SignalmanWSURL:     "ws://localhost:18009",
		}, Executor: fwcfg.Executor{Type: "local"}}}}
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
		ep := c.Endpoints
		fmt.Fprintf(cmd.OutOrStdout(), "  gateway:         %s\n", ep.GatewayURL)
		fmt.Fprintf(cmd.OutOrStdout(), "  quartermaster:   %s\n", ep.QuartermasterURL)
		fmt.Fprintf(cmd.OutOrStdout(), "  control:         %s\n", ep.ControlURL)
		fmt.Fprintf(cmd.OutOrStdout(), "  foghorn http:    %s\n", ep.FoghornHTTPURL)
		fmt.Fprintf(cmd.OutOrStdout(), "  foghorn grpc:    %s\n", ep.FoghornGRPCAddr)
		fmt.Fprintf(cmd.OutOrStdout(), "  decklog grpc:    %s\n", ep.DecklogGRPCAddr)
		fmt.Fprintf(cmd.OutOrStdout(), "  periscope query: %s\n", ep.PeriscopeQueryURL)
		fmt.Fprintf(cmd.OutOrStdout(), "  periscope ingest:%s\n", ep.PeriscopeIngestURL)
		fmt.Fprintf(cmd.OutOrStdout(), "  purser:          %s\n", ep.PurserURL)
		fmt.Fprintf(cmd.OutOrStdout(), "  signalman ws:    %s\n", ep.SignalmanWSURL)
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
		case "gateway":
			ep.GatewayURL = url
		case "quartermaster":
			ep.QuartermasterURL = url
		case "control":
			ep.ControlURL = url
		case "foghorn-http":
			ep.FoghornHTTPURL = url
		case "foghorn-grpc":
			ep.FoghornGRPCAddr = url
		case "decklog-grpc":
			ep.DecklogGRPCAddr = url
		case "periscope-query":
			ep.PeriscopeQueryURL = url
		case "periscope-ingest":
			ep.PeriscopeIngestURL = url
		case "purser":
			ep.PurserURL = url
		case "signalman-ws":
			ep.SignalmanWSURL = url
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
