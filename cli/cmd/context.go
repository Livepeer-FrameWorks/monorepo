package cmd

import (
	"encoding/json"
	"fmt"

	fwcfg "frameworks/cli/internal/config"
	"frameworks/cli/internal/ux"

	"github.com/spf13/cobra"
)

func newContextCmd() *cobra.Command {
	ctx := &cobra.Command{
		Use:   "context",
		Short: "Manage CLI contexts (endpoints, persona, gitops defaults)",
		Long: `Manage CLI contexts.

A context bundles endpoints, persona, and manifest-sourcing defaults for
one environment (e.g. platform-prod, staging, my-cluster). Use 'frameworks
setup' for interactive onboarding, or 'frameworks context create' +
'frameworks context set-*' for non-interactive bootstrap.`,
	}
	ctx.AddCommand(newContextCreateCmd())
	ctx.AddCommand(newContextListCmd())
	ctx.AddCommand(newContextUseCmd())
	ctx.AddCommand(newContextShowCmd())
	ctx.AddCommand(newContextSetURLCmd())
	ctx.AddCommand(newContextCheckCmd())
	ctx.AddCommand(newContextSetClusterCmd())
	ctx.AddCommand(newContextSetPersonaCmd())
	ctx.AddCommand(newContextSetGitopsSourceCmd())
	ctx.AddCommand(newContextSetGitopsPathCmd())
	ctx.AddCommand(newContextSetGitopsRepoCmd())
	ctx.AddCommand(newContextSetGitopsRefCmd())
	ctx.AddCommand(newContextSetGitopsClusterCmd())
	ctx.AddCommand(newContextSetGitopsManifestCmd())
	ctx.AddCommand(newContextSetAgeKeyCmd())
	return ctx
}

// mutateContext loads the active (or explicit) context, applies mutate,
// writes it back, and prints "Updated <subject> in context <name>" on success.
func mutateContext(cmd *cobra.Command, explicitCtx, subject string, mutate func(*fwcfg.Context) error) error {
	cfg, err := fwcfg.Load()
	if err != nil {
		return err
	}

	var target string
	if explicitCtx != "" {
		target = explicitCtx
	} else {
		rt := fwcfg.GetRuntimeOverrides()
		active, err := fwcfg.ResolveActiveContext(rt, fwcfg.OSEnv{}, cfg)
		if err != nil {
			return err
		}
		target = active.Name
	}

	ctx, ok := cfg.Contexts[target]
	if !ok {
		return fmt.Errorf("context %q does not exist (use 'frameworks context create %s' first)", target, target)
	}
	if ctx.Name == "" {
		ctx.Name = target
	}
	if err := mutate(&ctx); err != nil {
		return err
	}
	cfg.Contexts[target] = ctx
	if err := fwcfg.Save(cfg); err != nil {
		return err
	}
	ux.Success(cmd.OutOrStdout(), fmt.Sprintf("Updated %s in context %q", subject, target))
	return nil
}

func newContextCreateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new empty context",
		Long: `Create a new context with default endpoints. The context is NOT made
current — pair with 'frameworks context use <name>' to switch to it.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, err := fwcfg.Load()
			if err != nil {
				return err
			}
			if cfg.Contexts == nil {
				cfg.Contexts = map[string]fwcfg.Context{}
			}
			if _, exists := cfg.Contexts[name]; exists {
				return fmt.Errorf("context %q already exists", name)
			}
			cfg.Contexts[name] = fwcfg.Context{
				Name:      name,
				Endpoints: fwcfg.DefaultEndpoints(),
				Executor:  fwcfg.Executor{Type: "local"},
			}
			if err := fwcfg.Save(cfg); err != nil {
				return err
			}
			ux.Success(cmd.OutOrStdout(), fmt.Sprintf("Created context %q", name))
			ux.PrintNextSteps(cmd.OutOrStdout(), []ux.NextStep{
				{Cmd: fmt.Sprintf("frameworks context use %s", name), Why: "Switch to the newly created context."},
				{Cmd: fmt.Sprintf("frameworks context set-persona <platform|selfhosted|edge> --context %s", name), Why: "Label the context by intent."},
			})
			return nil
		},
	}
}

func newContextListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List contexts",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := fwcfg.Load()
			if err != nil {
				return err
			}
			rt := fwcfg.GetRuntimeOverrides()
			active, err := fwcfg.MaybeActiveContext(rt, fwcfg.OSEnv{}, cfg)
			if err != nil {
				return err
			}
			activeName := cfg.Current
			if active.Name != "" {
				activeName = active.Name
			}
			if rt.OutputJSON || output == "json" {
				type contextEntry struct {
					Name    string `json:"name"`
					Current bool   `json:"current"`
				}
				entries := make([]contextEntry, 0, len(cfg.Contexts))
				for name := range cfg.Contexts {
					entries = append(entries, contextEntry{Name: name, Current: name == activeName})
				}
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(entries)
			}
			for name := range cfg.Contexts {
				cur := " "
				if name == activeName {
					cur = "*"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", cur, name)
			}
			return nil
		},
	}
}

func newContextUseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "use <name>",
		Short: "Switch current context",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := fwcfg.Load()
			if err != nil {
				return err
			}
			if _, ok := cfg.Contexts[args[0]]; !ok {
				return fmt.Errorf("unknown context: %s", args[0])
			}
			cfg.Current = args[0]
			if err := fwcfg.Save(cfg); err != nil {
				return err
			}
			ux.Success(cmd.OutOrStdout(), fmt.Sprintf("Now using context %q", args[0]))
			return nil
		},
	}
}

func newContextShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show [name]",
		Short: "Show context details",
		Args:  cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := fwcfg.Load()
			if err != nil {
				return err
			}
			var name string
			if len(args) == 1 {
				name = args[0]
			} else {
				rt := fwcfg.GetRuntimeOverrides()
				active, activeErr := fwcfg.MaybeActiveContext(rt, fwcfg.OSEnv{}, cfg)
				if activeErr != nil {
					return activeErr
				}
				name = active.Name
				if name == "" {
					name = cfg.Current
				}
			}
			if name == "" {
				return fmt.Errorf("no current context configured: run 'frameworks setup'")
			}
			c, ok := cfg.Contexts[name]
			if !ok {
				return fmt.Errorf("unknown context: %s", name)
			}
			if c.Name == "" {
				c.Name = name
			}
			rt := fwcfg.GetRuntimeOverrides()
			if rt.OutputJSON || output == "json" {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(c)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Context: %s\n", name)
			if c.Persona != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "  persona:             %s\n", c.Persona)
			}
			if c.ClusterID != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "  cluster_id:          %s\n", c.ClusterID)
			}
			if c.Gitops != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "  gitops.source:       %s\n", c.Gitops.Source)
				if c.Gitops.LocalPath != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "  gitops.local_path:   %s\n", c.Gitops.LocalPath)
				}
				if c.Gitops.Repo != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "  gitops.repo:         %s\n", c.Gitops.Repo)
				}
				if c.Gitops.Ref != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "  gitops.ref:          %s\n", c.Gitops.Ref)
				}
				if c.Gitops.ManifestPath != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "  gitops.manifest:     %s\n", c.Gitops.ManifestPath)
				}
				if c.Gitops.Cluster != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "  gitops.cluster:      %s\n", c.Gitops.Cluster)
				}
				if c.Gitops.AgeKeyPath != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "  gitops.age_key:      %s\n", c.Gitops.AgeKeyPath)
				}
			}
			ep := c.Endpoints
			fmt.Fprintf(cmd.OutOrStdout(), "  bridge (http):       %s\n", ep.BridgeURL)
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
		},
	}
}

func ctxFlag(cmd *cobra.Command, target *string) {
	cmd.Flags().StringVar(target, "context", "", "target context name (defaults to active context)")
}

func newContextSetURLCmd() *cobra.Command {
	var explicitCtx string
	cmd := &cobra.Command{
		Use:   "set-url <service> <url>",
		Short: "Update a service URL in a context",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, url := args[0], args[1]
			return mutateContext(cmd, explicitCtx, fmt.Sprintf("%s URL", svc), func(c *fwcfg.Context) error {
				return setEndpointURL(&c.Endpoints, svc, url)
			})
		},
	}
	ctxFlag(cmd, &explicitCtx)
	return cmd
}

func setEndpointURL(ep *fwcfg.Endpoints, svc, url string) error {
	switch svc {
	case "bridge":
		ep.BridgeURL = url
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
	return nil
}

func newContextSetClusterCmd() *cobra.Command {
	var explicitCtx string
	cmd := &cobra.Command{
		Use:   "set-cluster <cluster-id>",
		Short: "Set the cluster ID in a context",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return mutateContext(cmd, explicitCtx, "cluster ID", func(c *fwcfg.Context) error {
				c.ClusterID = args[0]
				return nil
			})
		},
	}
	ctxFlag(cmd, &explicitCtx)
	return cmd
}

func newContextSetPersonaCmd() *cobra.Command {
	var explicitCtx string
	cmd := &cobra.Command{
		Use:   "set-persona <platform|selfhosted|edge>",
		Short: "Set the persona for a context",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := fwcfg.Persona(args[0])
			switch p {
			case fwcfg.PersonaPlatform, fwcfg.PersonaSelfHosted, fwcfg.PersonaEdge:
			default:
				return fmt.Errorf("persona must be one of platform|selfhosted|edge (got %q)", args[0])
			}
			return mutateContext(cmd, explicitCtx, "persona", func(c *fwcfg.Context) error {
				c.Persona = p
				return nil
			})
		},
	}
	ctxFlag(cmd, &explicitCtx)
	return cmd
}

func ensureGitops(c *fwcfg.Context) *fwcfg.Gitops {
	if c.Gitops == nil {
		c.Gitops = &fwcfg.Gitops{}
	}
	return c.Gitops
}

func newContextSetGitopsSourceCmd() *cobra.Command {
	var explicitCtx string
	cmd := &cobra.Command{
		Use:   "set-gitops-source <local|github|manifest>",
		Short: "Set the manifest source type for a context",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := fwcfg.GitopsSource(args[0])
			switch s {
			case fwcfg.GitopsLocal, fwcfg.GitopsGitHub, fwcfg.GitopsManifest:
			default:
				return fmt.Errorf("source must be local|github|manifest (got %q)", args[0])
			}
			return mutateContext(cmd, explicitCtx, "gitops source", func(c *fwcfg.Context) error {
				ensureGitops(c).Source = s
				return nil
			})
		},
	}
	ctxFlag(cmd, &explicitCtx)
	return cmd
}

func newContextSetGitopsPathCmd() *cobra.Command {
	var explicitCtx string
	cmd := &cobra.Command{
		Use:   "set-gitops-path <path>",
		Short: "Set the local gitops repo path (source=local)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return mutateContext(cmd, explicitCtx, "gitops local-path", func(c *fwcfg.Context) error {
				ensureGitops(c).LocalPath = args[0]
				return nil
			})
		},
	}
	ctxFlag(cmd, &explicitCtx)
	return cmd
}

func newContextSetGitopsRepoCmd() *cobra.Command {
	var explicitCtx string
	cmd := &cobra.Command{
		Use:   "set-gitops-repo <owner/repo>",
		Short: "Set the GitHub repo (source=github)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return mutateContext(cmd, explicitCtx, "gitops repo", func(c *fwcfg.Context) error {
				ensureGitops(c).Repo = args[0]
				return nil
			})
		},
	}
	ctxFlag(cmd, &explicitCtx)
	return cmd
}

func newContextSetGitopsRefCmd() *cobra.Command {
	var explicitCtx string
	cmd := &cobra.Command{
		Use:   "set-gitops-ref <ref>",
		Short: "Set the GitHub branch/tag (source=github)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return mutateContext(cmd, explicitCtx, "gitops ref", func(c *fwcfg.Context) error {
				ensureGitops(c).Ref = args[0]
				return nil
			})
		},
	}
	ctxFlag(cmd, &explicitCtx)
	return cmd
}

func newContextSetGitopsClusterCmd() *cobra.Command {
	var explicitCtx string
	cmd := &cobra.Command{
		Use:   "set-gitops-cluster <name>",
		Short: "Set the cluster name within the gitops repo",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return mutateContext(cmd, explicitCtx, "gitops cluster", func(c *fwcfg.Context) error {
				ensureGitops(c).Cluster = args[0]
				return nil
			})
		},
	}
	ctxFlag(cmd, &explicitCtx)
	return cmd
}

func newContextSetGitopsManifestCmd() *cobra.Command {
	var explicitCtx string
	cmd := &cobra.Command{
		Use:   "set-gitops-manifest <path>",
		Short: "Set the single-manifest path (source=manifest, or explicit override)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return mutateContext(cmd, explicitCtx, "gitops manifest-path", func(c *fwcfg.Context) error {
				ensureGitops(c).ManifestPath = args[0]
				return nil
			})
		},
	}
	ctxFlag(cmd, &explicitCtx)
	return cmd
}

func newContextSetAgeKeyCmd() *cobra.Command {
	var explicitCtx string
	cmd := &cobra.Command{
		Use:   "set-age-key <path>",
		Short: "Set the SOPS age key path for this context",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return mutateContext(cmd, explicitCtx, "age key", func(c *fwcfg.Context) error {
				ensureGitops(c).AgeKeyPath = args[0]
				return nil
			})
		},
	}
	ctxFlag(cmd, &explicitCtx)
	return cmd
}
