package cmd

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"

	fwcfg "frameworks/cli/internal/config"
	"frameworks/cli/internal/mesh"
	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"

	"github.com/spf13/cobra"
)

// newMeshWgCmd returns the `mesh wg` command group for WireGuard identity
// management committed to GitOps.
func newMeshWgCmd() *cobra.Command {
	wg := &cobra.Command{
		Use:   "wg",
		Short: "Manage GitOps-owned WireGuard identity",
		Long: `Generate, validate, and rotate the per-host WireGuard identity consumed by
Privateer's startup substrate.

Public keys and mesh IPs are written to the plaintext cluster.yaml; private
keys are written to the SOPS-encrypted hosts inventory. Provisioning validates
this state but never writes it.`,
	}
	wg.AddCommand(newMeshWgGenerateCmd())
	wg.AddCommand(newMeshWgCheckCmd())
	wg.AddCommand(newMeshWgRotateCmd())
	wg.AddCommand(newMeshWgAuditCmd())
	wg.AddCommand(newMeshWgPromoteCmd())
	return wg
}

func newMeshWgGenerateCmd() *cobra.Command {
	var (
		hostsPath  string
		meshCIDR   string
		listenPort int
		dryRun     bool
	)
	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Populate per-host WireGuard identity in cluster.yaml + hosts.enc.yaml",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			manifestPath := stringFlag(cmd, "manifest").Value
			target, err := resolveMeshMutationTarget(cmd, manifestPath, hostsPath)
			if err != nil {
				return err
			}
			return runMeshWgGenerate(cmd, target.manifestPath, target.hostsPath, target.ageKey, meshCIDR, listenPort, "", false, dryRun)
		},
	}
	cmd.Flags().StringVar(&hostsPath, "hosts-file", "", "path to SOPS-encrypted hosts inventory (default: manifest hosts_file or sibling hosts.enc.yaml)")
	cmd.Flags().StringVar(&meshCIDR, "mesh-cidr", "10.88.0.0/16", "IPv4 CIDR for the WireGuard mesh")
	cmd.Flags().IntVar(&listenPort, "listen-port", 51820, "UDP listen port for the WireGuard mesh")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the planned changes without writing any files")
	return cmd
}

func newMeshWgRotateCmd() *cobra.Command {
	var (
		hostsPath  string
		meshCIDR   string
		listenPort int
		readdress  bool
		dryRun     bool
	)
	cmd := &cobra.Command{
		Use:   "rotate <host>",
		Short: "Rotate one host's WireGuard key in cluster.yaml + hosts.enc.yaml",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			manifestPath := stringFlag(cmd, "manifest").Value
			target, err := resolveMeshMutationTarget(cmd, manifestPath, hostsPath)
			if err != nil {
				return err
			}
			return runMeshWgGenerate(cmd, target.manifestPath, target.hostsPath, target.ageKey, meshCIDR, listenPort, args[0], readdress, dryRun)
		},
	}
	cmd.Flags().StringVar(&hostsPath, "hosts-file", "", "path to SOPS-encrypted hosts inventory (default: manifest hosts_file or sibling hosts.enc.yaml)")
	cmd.Flags().StringVar(&meshCIDR, "mesh-cidr", "10.88.0.0/16", "IPv4 CIDR for the WireGuard mesh")
	cmd.Flags().IntVar(&listenPort, "listen-port", 51820, "UDP listen port for the WireGuard mesh")
	cmd.Flags().BoolVar(&readdress, "readdress", false, "also allocate a new wireguard_ip for the host")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the planned changes without writing any files")
	return cmd
}

func newMeshWgCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check",
		Short: "Validate GitOps WireGuard identity without mutating files",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return err
			}
			defer rc.Cleanup()

			hostNames := meshCheckHostNames(rc.Manifest)
			if err := mesh.ValidateIdentity(rc.Manifest, hostNames); err != nil {
				return fmt.Errorf("%w\n\nRun: frameworks mesh wg generate --manifest %s", err, rc.ManifestPath)
			}
			ux.Success(cmd.OutOrStdout(), fmt.Sprintf("mesh wg: identity valid for %d host(s)", len(hostNames)))
			return nil
		},
	}
}

func runMeshWgGenerate(cmd *cobra.Command, manifestPath, hostsPath, ageKey, cidrStr string, listenPort int, rotateHost string, readdress, dryRun bool) error {
	_, cidr, err := net.ParseCIDR(cidrStr)
	if err != nil {
		return fmt.Errorf("--mesh-cidr: %w", err)
	}

	manifest, err := inventory.LoadWithHostsFileNoValidate(manifestPath, hostsPath, ageKey)
	if err != nil {
		return fmt.Errorf("load manifest: %w", err)
	}

	taken := map[string]struct{}{}
	hostNames := make([]string, 0, len(manifest.Hosts))
	for name, h := range manifest.Hosts {
		hostNames = append(hostNames, name)
		if h.WireguardIP != "" && name != rotateHost {
			taken[h.WireguardIP] = struct{}{}
		}
	}
	sort.Strings(hostNames)
	if rotateHost != "" {
		if _, ok := manifest.Hosts[rotateHost]; !ok {
			return fmt.Errorf("host %q not found in manifest", rotateHost)
		}
	}

	clusterName := manifest.Profile
	if clusterName == "" {
		clusterName = filepath.Base(filepath.Dir(manifestPath))
	}

	hostUpdates := map[string]mesh.HostWG{}
	privateKeys := map[string]string{}
	changed := 0
	wireGuardChanged := manifest.WireGuard == nil ||
		!manifest.WireGuard.Enabled ||
		manifest.WireGuard.MeshCIDR != cidrStr ||
		manifest.WireGuard.ListenPort != listenPort

	// adoptedLocalHosts are rotate targets currently holding their private
	// key on-disk (enrollment_origin=adopted_local). After rotate writes a
	// fresh SOPS-managed key, we also strip the preserve-key markers from
	// hosts.enc.yaml so the Ansible role stops treating them as opt-out.
	adoptedLocalHosts := []string{}
	for _, name := range hostNames {
		h := manifest.Hosts[name]
		needKey := h.WireguardPublicKey == "" || h.WireguardPrivateKey == ""
		needIP := h.WireguardIP == ""
		needPort := h.WireguardPort == 0
		if rotateHost != "" && name != rotateHost {
			continue
		}
		if rotateHost == name {
			needKey = true
			needIP = readdress || h.WireguardIP == ""
			needPort = h.WireguardPort == 0
			if h.WireguardPrivateKeyManaged != nil && !*h.WireguardPrivateKeyManaged {
				adoptedLocalHosts = append(adoptedLocalHosts, name)
			}
		}

		updated := mesh.HostWG{
			WireguardIP:        h.WireguardIP,
			WireguardPublicKey: h.WireguardPublicKey,
			WireguardPort:      h.WireguardPort,
		}
		if needKey {
			priv, pub, keyErr := mesh.GenerateKeyPair()
			if keyErr != nil {
				return fmt.Errorf("host %s: %w", name, keyErr)
			}
			updated.WireguardPublicKey = pub
			privateKeys[name] = priv
		}
		if needIP {
			ip, allocErr := mesh.AllocateMeshIP(clusterName, name, cidr, taken)
			if allocErr != nil {
				return fmt.Errorf("host %s: %w", name, allocErr)
			}
			updated.WireguardIP = ip.String()
			taken[updated.WireguardIP] = struct{}{}
		}
		if needPort {
			updated.WireguardPort = listenPort
		}

		if updated != (mesh.HostWG{
			WireguardIP:        h.WireguardIP,
			WireguardPublicKey: h.WireguardPublicKey,
			WireguardPort:      h.WireguardPort,
		}) {
			changed++
		}
		hostUpdates[name] = updated
	}

	if dryRun {
		printWgDryRun(cmd.OutOrStdout(), manifest, hostUpdates, privateKeys, cidrStr, listenPort, wireGuardChanged, hostNames)
		return nil
	}

	if changed == 0 && !wireGuardChanged {
		if err = mesh.ValidateIdentity(manifest, hostNames); err != nil {
			return err
		}
		ux.Success(cmd.OutOrStdout(), fmt.Sprintf("mesh wg: all %d host(s) already populated - no changes", len(hostNames)))
		return nil
	}

	rawManifest, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	updatedManifest, err := mesh.UpdateClusterYAML(rawManifest, hostUpdates, mesh.WireGuardBlock{
		Enabled:    true,
		MeshCIDR:   cidrStr,
		ListenPort: listenPort,
	})
	if err != nil {
		return err
	}

	// Stage the SOPS hosts file (if private keys changed) and the manifest
	// tempfile together, validate the combined post-state, then commit both.
	// POSIX doesn't give us true atomicity across two paths; on second-commit
	// failure we restore the first file from an in-memory backup and report.
	var stagedHosts *mesh.StagedFile
	if len(privateKeys) > 0 {
		if _, statErr := os.Stat(hostsPath); statErr != nil {
			return fmt.Errorf("hosts-file %s: %w", hostsPath, statErr)
		}
		stagedHosts, err = mesh.StageEncryptedYAML(cmd.Context(), hostsPath, ageKey, func(plaintext []byte) ([]byte, error) {
			updated, updErr := mesh.UpdateHostInventoryYAML(plaintext, privateKeys)
			if updErr != nil {
				return nil, updErr
			}
			// Adopted-local hosts being rotated back into SOPS need their
			// preserve-key markers stripped at the same time — otherwise
			// the next provision would still assert the on-disk key and
			// skip rendering the fresh SOPS one.
			return mesh.ClearAdoptedLocalMarkers(updated, adoptedLocalHosts)
		})
		if err != nil {
			return fmt.Errorf("stage %s: %w", hostsPath, err)
		}
		defer stagedHosts.Discard()
	}

	manifestTmp, err := os.CreateTemp(filepath.Dir(manifestPath), ".cluster-*.yaml.tmp")
	if err != nil {
		return fmt.Errorf("stage manifest: %w", err)
	}
	manifestTmpPath := manifestTmp.Name()
	defer os.Remove(manifestTmpPath) //nolint:errcheck
	if _, writeErr := manifestTmp.Write(updatedManifest); writeErr != nil {
		manifestTmp.Close()
		return fmt.Errorf("write staged manifest: %w", writeErr)
	}
	if closeErr := manifestTmp.Close(); closeErr != nil {
		return fmt.Errorf("close staged manifest: %w", closeErr)
	}

	verifyHostsPath := hostsPath
	if stagedHosts != nil && stagedHosts.TempPath != "" {
		verifyHostsPath = stagedHosts.TempPath
	}
	verified, err := inventory.LoadWithHostsFileNoValidate(manifestTmpPath, verifyHostsPath, ageKey)
	if err != nil {
		return fmt.Errorf("validate staged mesh identity: %w", err)
	}
	if validateErr := mesh.ValidateIdentity(verified, hostNames); validateErr != nil {
		return fmt.Errorf("validate staged mesh identity: %w", validateErr)
	}

	manifestBackup, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read original manifest for rollback: %w", err)
	}

	if err := mesh.CommitManifestAndHosts(manifestPath, manifestTmpPath, manifestBackup, stagedHosts); err != nil {
		return err
	}

	ux.Success(cmd.OutOrStdout(), fmt.Sprintf("mesh wg: updated %d host(s)", changed))
	for _, name := range hostNames {
		u, ok := hostUpdates[name]
		if !ok {
			continue
		}
		fmt.Fprintf(cmd.OutOrStdout(), "  %s -> %s (port %d)\n", name, u.WireguardIP, u.WireguardPort)
	}
	if len(adoptedLocalHosts) > 0 {
		fmt.Fprintln(cmd.OutOrStdout())
		fmt.Fprintln(cmd.OutOrStdout(), "Rotated adopted-local host(s) into SOPS-managed keys. To finish the promotion to gitops_seed:")
		fmt.Fprintln(cmd.OutOrStdout(), "  1. commit cluster.yaml + hosts.enc.yaml")
		fmt.Fprintln(cmd.OutOrStdout(), "  2. run `frameworks cluster provision` — Ansible renders the new private key on each host")
		fmt.Fprintln(cmd.OutOrStdout(), "  3. run `frameworks mesh wg promote <host>` once the node has SyncMesh'd the new public key")
	}
	return nil
}

func printWgDryRun(w io.Writer, manifest *inventory.Manifest, updates map[string]mesh.HostWG, privateKeys map[string]string, cidrStr string, listenPort int, wireGuardChanged bool, hostNames []string) {
	fmt.Fprintln(w, "mesh wg (dry-run): no files would be touched")

	if wireGuardChanged {
		curEnabled, curCIDR, curPort := false, "<unset>", 0
		if manifest.WireGuard != nil {
			curEnabled = manifest.WireGuard.Enabled
			if manifest.WireGuard.MeshCIDR != "" {
				curCIDR = manifest.WireGuard.MeshCIDR
			}
			curPort = manifest.WireGuard.ListenPort
		}
		fmt.Fprintf(w, "  wireguard block: enabled %v→true  mesh_cidr %s→%s  listen_port %d→%d\n",
			curEnabled, curCIDR, cidrStr, curPort, listenPort)
	} else {
		fmt.Fprintln(w, "  wireguard block: no change")
	}

	if len(privateKeys) > 0 {
		fmt.Fprintf(w, "  SOPS hosts file: would decrypt + re-encrypt (%d private key(s) to write)\n", len(privateKeys))
	} else {
		fmt.Fprintln(w, "  SOPS hosts file: no change")
	}

	hostChanges := 0
	for _, name := range hostNames {
		updated, ok := updates[name]
		if !ok {
			continue
		}
		current := manifest.Hosts[name]
		keyChange := privateKeys[name] != ""
		ipChange := current.WireguardIP != updated.WireguardIP
		portChange := current.WireguardPort != updated.WireguardPort
		if !keyChange && !ipChange && !portChange {
			continue
		}
		hostChanges++
		fmt.Fprintf(w, "  %s:\n", name)
		if keyChange {
			fmt.Fprintln(w, "    key:  generate new")
		}
		if ipChange {
			old := current.WireguardIP
			if old == "" {
				old = "<unset>"
			}
			fmt.Fprintf(w, "    ip:   %s → %s\n", old, updated.WireguardIP)
		}
		if portChange {
			fmt.Fprintf(w, "    port: %d → %d\n", current.WireguardPort, updated.WireguardPort)
		}
	}
	if hostChanges == 0 {
		fmt.Fprintln(w, "  hosts: no change")
	}
}

type meshMutationTarget struct {
	manifestPath string
	hostsPath    string
	ageKey       string
}

func resolveMeshMutationTarget(cmd *cobra.Command, manifestPath, hostsPath string) (*meshMutationTarget, error) {
	ageKey := stringFlag(cmd, "age-key").Value
	if strings.TrimSpace(manifestPath) != "" {
		hosts, err := resolveMeshHostsFile(manifestPath, hostsPath)
		if err != nil {
			return nil, err
		}
		return &meshMutationTarget{manifestPath: manifestPath, hostsPath: hosts, ageKey: ageKey}, nil
	}
	gitopsDirFlag := stringFlag(cmd, "gitops-dir")
	usingExplicitLocalGitops := gitopsDirFlag.Changed && strings.TrimSpace(gitopsDirFlag.Value) != ""
	if !usingExplicitLocalGitops && stringFlag(cmd, "github-repo").Value != "" && stringFlag(cmd, "github-repo").Changed {
		return nil, fmt.Errorf("mesh wg mutation requires a local checkout; use --gitops-dir or --manifest instead of --github-repo")
	}
	if !usingExplicitLocalGitops && os.Getenv("FRAMEWORKS_GITHUB_REPO") != "" {
		return nil, fmt.Errorf("mesh wg mutation requires a local checkout; FRAMEWORKS_GITHUB_REPO is read-only for mutation")
	}

	cfg, err := fwcfg.Load()
	if err != nil {
		return nil, err
	}
	rt := fwcfg.GetRuntimeOverrides()
	ctxCfg, err := fwcfg.MaybeActiveContext(rt, fwcfg.OSEnv{}, cfg)
	if err != nil {
		return nil, err
	}
	if !usingExplicitLocalGitops && ctxCfg.Gitops != nil && ctxCfg.Gitops.Source == fwcfg.GitopsGitHub {
		return nil, fmt.Errorf("mesh wg mutation requires a local checkout; context %q uses GitHub", ctxCfg.Name)
	}
	cwd, _ := os.Getwd() //nolint:errcheck
	rm, err := inventory.ResolveManifestSource(inventory.ResolveInput{
		Manifest:  stringFlag(cmd, "manifest"),
		GitopsDir: gitopsDirFlag,
		GithubRef: stringFlag(cmd, "github-ref"),
		Cluster:   stringFlag(cmd, "cluster"),
		AgeKey:    stringFlag(cmd, "age-key"),
		Env:       fwcfg.OSEnv{},
		Context:   ctxCfg,
		GitHubCfg: cfg.GitHub,
		Cwd:       cwd,
		Stdout:    cmd.OutOrStdout(),
		Ctx:       cmd.Context(),
	})
	if err != nil {
		return nil, err
	}
	if rm.Cleanup != nil {
		defer rm.Cleanup()
	}
	hosts, err := resolveMeshHostsFile(rm.Path, hostsPath)
	if err != nil {
		return nil, err
	}
	return &meshMutationTarget{manifestPath: rm.Path, hostsPath: hosts, ageKey: rm.AgeKey}, nil
}

func resolveMeshHostsFile(manifestPath, explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return "", fmt.Errorf("read manifest: %w", err)
	}
	manifest, err := inventory.ParseManifest(raw)
	if err != nil {
		return "", err
	}
	if manifest.HostsFile != "" {
		if filepath.IsAbs(manifest.HostsFile) {
			return manifest.HostsFile, nil
		}
		return filepath.Join(filepath.Dir(manifestPath), manifest.HostsFile), nil
	}
	return filepath.Join(filepath.Dir(manifestPath), "hosts.enc.yaml"), nil
}

func meshCheckHostNames(manifest *inventory.Manifest) []string {
	if manifest == nil {
		return nil
	}
	if svc, ok := manifest.Services["privateer"]; ok && svc.Enabled {
		return orchestrator.EffectivePrivateerHosts(svc, manifest.Hosts)
	}
	if manifest.WireGuard == nil || !manifest.WireGuard.Enabled {
		return nil
	}
	names := make([]string, 0, len(manifest.Hosts))
	for name := range manifest.Hosts {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
