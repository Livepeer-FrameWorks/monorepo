package cmd

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	fwcfg "frameworks/cli/internal/config"
	"frameworks/cli/internal/ux"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
)

func newSetupCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: "Interactive onboarding: choose a persona and capture defaults",
		Long: `Interactive wizard that captures your persona, endpoints, and manifest
defaults into a context, then makes it current.

Non-interactive alternative: use 'frameworks context create <name>' and
the 'frameworks context set-*' commands.`,
		RunE: runSetup,
	}
}

func runSetup(cmd *cobra.Command, _ []string) error {
	if !isatty.IsTerminal(os.Stdin.Fd()) {
		return fmt.Errorf(`not a terminal; bootstrap non-interactively with:
  frameworks context create <name>
  frameworks context set-persona <platform|selfhosted|edge> --context <name>
  frameworks context set-gitops-source <local|github|manifest> --context <name>
  frameworks context set-gitops-path <path> --context <name>    (local)
  frameworks context set-gitops-repo <owner/repo> --context <name>   (github)
  frameworks context set-gitops-cluster <name> --context <name>
  frameworks context set-age-key <path> --context <name>
  frameworks context use <name>`)
	}

	cfg, err := fwcfg.Load()
	if err != nil {
		return err
	}

	reader := bufio.NewReader(os.Stdin)
	out := cmd.OutOrStdout()

	persona, err := promptPersona(reader, out)
	if err != nil {
		return err
	}

	name, err := promptContextName(reader, out, persona, cfg)
	if err != nil {
		return err
	}

	ctx := fwcfg.Context{
		Name:     name,
		Executor: fwcfg.Executor{Type: "local"},
		Persona:  persona,
	}
	if persona == fwcfg.PersonaEdge {
		// Edge contexts only need to know about Bridge; cluster-internal
		// endpoints are resolved per-call (e.g. via bootstrapEdge).
		ctx.Endpoints = fwcfg.Endpoints{BridgeURL: fwcfg.DefaultEndpoints().BridgeURL}
	} else {
		ctx.Endpoints = fwcfg.DefaultEndpoints()
	}

	if persona == fwcfg.PersonaEdge {
		if err := promptBridgeURL(reader, out, &ctx); err != nil {
			return err
		}
	} else {
		if err := promptBridgeURL(reader, out, &ctx); err != nil {
			return err
		}
		if err := promptControlPlaneHost(reader, out, &ctx); err != nil {
			return err
		}
		if err := promptGitops(reader, out, &ctx); err != nil {
			return err
		}
	}

	if cfg.Contexts == nil {
		cfg.Contexts = map[string]fwcfg.Context{}
	}
	cfg.Contexts[name] = ctx
	cfg.Current = name

	if err := fwcfg.Save(cfg); err != nil {
		return err
	}

	path, pathErr := fwcfg.ConfigPath()
	if pathErr != nil {
		// Save already succeeded, so the path is valid on disk; this
		// is only needed to surface the path to the user.
		path = "(unknown)"
	}
	fmt.Fprintln(out, "")
	ux.Success(out, fmt.Sprintf("Saved context %q as current in %s.", name, path))

	ux.Result(out, setupResultFields(ctx))
	ux.PrintNextSteps(out, setupNextSteps(persona))
	return nil
}

func setupResultFields(ctx fwcfg.Context) []ux.ResultField {
	fields := []ux.ResultField{
		{Key: "context", OK: true, Detail: ctx.Name},
		{Key: "persona", OK: true, Detail: string(ctx.Persona)},
		{Key: "bridge url", OK: ctx.Endpoints.BridgeURL != "", Detail: ctx.Endpoints.BridgeURL},
	}
	if ctx.Persona != fwcfg.PersonaEdge {
		fields = append(fields, ux.ResultField{
			Key:    "control plane",
			OK:     ctx.Endpoints.QuartermasterGRPCAddr != "",
			Detail: ctx.Endpoints.QuartermasterGRPCAddr,
		})
		if ctx.Gitops != nil {
			fields = append(fields, ux.ResultField{
				Key:    "gitops",
				OK:     true,
				Detail: string(ctx.Gitops.Source),
			})
		}
	}
	return fields
}

func setupNextSteps(persona fwcfg.Persona) []ux.NextStep {
	switch persona {
	case fwcfg.PersonaEdge:
		return []ux.NextStep{
			{Cmd: "frameworks login", Why: "Authenticate so `edge deploy` can auto-create a private cluster."},
			{Cmd: "frameworks edge deploy --ssh <user>@<host>", Why: "Or deploy directly with a pre-issued enrollment token."},
		}
	case fwcfg.PersonaPlatform, fwcfg.PersonaSelfHosted:
		return []ux.NextStep{
			{Cmd: "frameworks cluster preflight", Why: "Check the host is ready to run cluster services."},
			{Cmd: "frameworks cluster provision --ready", Why: "Provision infra + init + static seeds in one shot."},
		}
	default:
		return nil
	}
}

func promptPersona(reader *bufio.Reader, out interface{ Write([]byte) (int, error) }) (fwcfg.Persona, error) {
	fmt.Fprintln(out, "What are you primarily using the Frameworks CLI for?")
	fmt.Fprintln(out, "  [1] Platform operations (deploy/manage the whole FrameWorks platform)")
	fmt.Fprintln(out, "  [2] Self-hosted cluster (run one cluster for your own use)")
	fmt.Fprintln(out, "  [3] Edge / account (manage edges or use the hosted API)")
	fmt.Fprint(out, "Select [1-3]: ")

	choice, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	switch strings.TrimSpace(choice) {
	case "1":
		return fwcfg.PersonaPlatform, nil
	case "2":
		return fwcfg.PersonaSelfHosted, nil
	case "3":
		return fwcfg.PersonaEdge, nil
	default:
		return "", fmt.Errorf("invalid selection: %q", strings.TrimSpace(choice))
	}
}

func promptContextName(reader *bufio.Reader, out interface{ Write([]byte) (int, error) }, persona fwcfg.Persona, cfg fwcfg.Config) (string, error) {
	suggested := suggestedContextName(persona)
	fmt.Fprintf(out, "Context name [%s]: ", suggested)
	raw, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(raw)
	if name == "" {
		name = suggested
	}
	if _, exists := cfg.Contexts[name]; exists {
		fmt.Fprintf(out, "Context %q already exists; overwrite? [y/N]: ", name)
		raw, err = reader.ReadString('\n')
		if err != nil {
			return "", err
		}
		if strings.ToLower(strings.TrimSpace(raw)) != "y" {
			return "", fmt.Errorf("aborted: context %q already exists", name)
		}
	}
	return name, nil
}

func suggestedContextName(p fwcfg.Persona) string {
	switch p {
	case fwcfg.PersonaPlatform:
		return "platform-prod"
	case fwcfg.PersonaSelfHosted:
		return "my-cluster"
	case fwcfg.PersonaEdge:
		return "my-edge"
	default:
		return "default"
	}
}

func promptBridgeURL(reader *bufio.Reader, out interface{ Write([]byte) (int, error) }, ctx *fwcfg.Context) error {
	fmt.Fprintf(out, "Bridge URL [%s]: ", ctx.Endpoints.BridgeURL)
	raw, err := reader.ReadString('\n')
	if err != nil {
		return err
	}
	if v := strings.TrimSpace(raw); v != "" {
		ctx.Endpoints.BridgeURL = v
	}
	return nil
}

// promptControlPlaneHost rewrites localhost in all gRPC and WS endpoint
// addresses to the operator's control-plane host, preserving per-service
// ports. Without this, a platform/self-hosted context saved by setup
// would leave services/mesh/dns/admin clients pointed at localhost.
func promptControlPlaneHost(reader *bufio.Reader, out interface{ Write([]byte) (int, error) }, ctx *fwcfg.Context) error {
	fmt.Fprint(out, "Control-plane host (for gRPC/WS endpoints) [localhost]: ")
	raw, err := reader.ReadString('\n')
	if err != nil {
		return err
	}
	host := strings.TrimSpace(raw)
	if host == "" || host == "localhost" {
		return nil
	}
	ep := &ctx.Endpoints
	ep.CommodoreGRPCAddr = replaceHost(ep.CommodoreGRPCAddr, host)
	ep.QuartermasterGRPCAddr = replaceHost(ep.QuartermasterGRPCAddr, host)
	ep.PurserGRPCAddr = replaceHost(ep.PurserGRPCAddr, host)
	ep.PeriscopeGRPCAddr = replaceHost(ep.PeriscopeGRPCAddr, host)
	ep.SignalmanGRPCAddr = replaceHost(ep.SignalmanGRPCAddr, host)
	ep.DecklogGRPCAddr = replaceHost(ep.DecklogGRPCAddr, host)
	ep.FoghornGRPCAddr = replaceHost(ep.FoghornGRPCAddr, host)
	ep.NavigatorGRPCAddr = replaceHost(ep.NavigatorGRPCAddr, host)
	ep.SignalmanWSURL = replaceHost(ep.SignalmanWSURL, host)
	return nil
}

// replaceHost swaps the host portion of addr (host:port or
// scheme://host:port) with newHost while preserving the port and any
// scheme/path. Returns addr unchanged if it has no recognizable host.
func replaceHost(addr, newHost string) string {
	if addr == "" {
		return addr
	}
	scheme := ""
	rest := addr
	if i := strings.Index(addr, "://"); i >= 0 {
		scheme = addr[:i+3]
		rest = addr[i+3:]
	}
	pathIdx := strings.IndexAny(rest, "/?")
	tail := ""
	hostPort := rest
	if pathIdx >= 0 {
		tail = rest[pathIdx:]
		hostPort = rest[:pathIdx]
	}
	if colon := strings.LastIndex(hostPort, ":"); colon >= 0 {
		hostPort = newHost + hostPort[colon:]
	} else {
		hostPort = newHost
	}
	return scheme + hostPort + tail
}

func promptGitops(reader *bufio.Reader, out interface{ Write([]byte) (int, error) }, ctx *fwcfg.Context) error {
	fmt.Fprintln(out, "Where do cluster manifests come from?")
	fmt.Fprintln(out, "  [a] Local gitops repo (a directory I've cloned)")
	fmt.Fprintln(out, "  [b] GitHub repo (via GitHub App)")
	fmt.Fprintln(out, "  [c] Single manifest file")
	fmt.Fprint(out, "Select [a-c]: ")
	raw, err := reader.ReadString('\n')
	if err != nil {
		return err
	}

	gs := &fwcfg.Gitops{}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "a":
		gs.Source = fwcfg.GitopsLocal
		if err := promptGitopsLocal(reader, out, gs); err != nil {
			return err
		}
	case "b":
		gs.Source = fwcfg.GitopsGitHub
		if err := promptGitopsGithub(reader, out, gs); err != nil {
			return err
		}
	case "c":
		gs.Source = fwcfg.GitopsManifest
		fmt.Fprint(out, "Path to cluster.yaml: ")
		manifestRaw, readErr := reader.ReadString('\n')
		if readErr != nil {
			return readErr
		}
		gs.ManifestPath = strings.TrimSpace(manifestRaw)
		if gs.ManifestPath == "" {
			return fmt.Errorf("manifest path required")
		}
	default:
		return fmt.Errorf("invalid selection: %q", strings.TrimSpace(raw))
	}

	if err := promptAgeKey(reader, out, gs); err != nil {
		return err
	}

	ctx.Gitops = gs
	return nil
}

func promptGitopsLocal(reader *bufio.Reader, out interface{ Write([]byte) (int, error) }, gs *fwcfg.Gitops) error {
	cwd, cwdErr := os.Getwd()
	if cwdErr != nil {
		cwd = ""
	}
	suggestion := ""
	if looksLikeGitopsRoot(cwd) {
		suggestion = cwd
	}
	prompt := "Path to your gitops repo"
	if suggestion != "" {
		prompt = fmt.Sprintf("%s [%s]", prompt, suggestion)
	}
	fmt.Fprintf(out, "%s: ", prompt)
	raw, err := reader.ReadString('\n')
	if err != nil {
		return err
	}
	v := strings.TrimSpace(raw)
	if v == "" {
		v = suggestion
	}
	if v == "" {
		return fmt.Errorf("gitops repo path required")
	}
	abs, err := filepath.Abs(v)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", v, err)
	}
	if _, statErr := os.Stat(abs); statErr != nil {
		return fmt.Errorf("stat %s: %w", abs, statErr)
	}
	gs.LocalPath = abs

	clusters, listErr := listClusterDirs(abs)
	if listErr != nil {
		return listErr
	}
	switch len(clusters) {
	case 0:
		return fmt.Errorf("no clusters/ subdirectory under %s; is this a gitops repo?", abs)
	case 1:
		gs.Cluster = clusters[0]
		fmt.Fprintf(out, "Using cluster: %s (auto-picked)\n", gs.Cluster)
	default:
		fmt.Fprintf(out, "Available clusters: %s\n", strings.Join(clusters, ", "))
		fmt.Fprint(out, "Which cluster? ")
		raw, err := reader.ReadString('\n')
		if err != nil {
			return err
		}
		gs.Cluster = strings.TrimSpace(raw)
		if !contains(clusters, gs.Cluster) {
			return fmt.Errorf("cluster %q not in %v", gs.Cluster, clusters)
		}
	}
	return nil
}

func promptGitopsGithub(reader *bufio.Reader, out interface{ Write([]byte) (int, error) }, gs *fwcfg.Gitops) error {
	fmt.Fprint(out, "GitHub repo (owner/repo): ")
	raw, err := reader.ReadString('\n')
	if err != nil {
		return err
	}
	gs.Repo = strings.TrimSpace(raw)
	if gs.Repo == "" {
		return fmt.Errorf("repo required")
	}

	fmt.Fprint(out, "Branch/ref [main]: ")
	raw, err = reader.ReadString('\n')
	if err != nil {
		return err
	}
	ref := strings.TrimSpace(raw)
	if ref == "" {
		ref = "main"
	}
	gs.Ref = ref

	fmt.Fprint(out, "Cluster name (within the repo): ")
	raw, err = reader.ReadString('\n')
	if err != nil {
		return err
	}
	gs.Cluster = strings.TrimSpace(raw)
	if gs.Cluster == "" {
		return fmt.Errorf("cluster name required")
	}
	fmt.Fprintln(out, "GitHub App credentials (app id, installation id, private key) are stored separately under 'frameworks config set github.*'.")
	return nil
}

func promptAgeKey(reader *bufio.Reader, out interface{ Write([]byte) (int, error) }, gs *fwcfg.Gitops) error {
	suggestion := strings.TrimSpace(os.Getenv("SOPS_AGE_KEY_FILE"))
	if suggestion == "" {
		if home, err := os.UserHomeDir(); err == nil {
			suggestion = filepath.Join(home, ".config", "sops", "age", "keys.txt")
		}
	}
	fmt.Fprintf(out, "SOPS age key path [%s]: ", suggestion)
	raw, err := reader.ReadString('\n')
	if err != nil {
		return err
	}
	v := strings.TrimSpace(raw)
	if v == "" {
		v = suggestion
	}
	gs.AgeKeyPath = v
	return nil
}

func looksLikeGitopsRoot(dir string) bool {
	if dir == "" {
		return false
	}
	if st, err := os.Stat(filepath.Join(dir, "clusters")); err != nil || !st.IsDir() {
		return false
	}
	if _, err := os.Stat(filepath.Join(dir, ".sops.yaml")); err != nil {
		return false
	}
	return true
}

func listClusterDirs(root string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(root, "clusters"))
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

func contains(ss []string, v string) bool {
	for _, s := range ss {
		if s == v {
			return true
		}
	}
	return false
}
