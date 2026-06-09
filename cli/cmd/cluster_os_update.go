package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/ansiblerun"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/provisioner"
	fwssh "frameworks/cli/pkg/ssh"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
)

// osUpdateHostAddress mirrors provisioner.hostAddressFor (unexported there).
// Inlined here to avoid widening the provisioner package's public surface
// for a single fleet-level command.
func osUpdateHostAddress(h inventory.Host) string {
	if h.ExternalIP != "" {
		return h.ExternalIP
	}
	return h.Name
}

// osUpdateCollectionsPath mirrors provisioner.ansibleCollectionsPath.
func osUpdateCollectionsPath(ansibleRoot, cacheCollectionsPath string) string {
	localCollectionsPath := filepath.Join(ansibleRoot, "collections")
	if cacheCollectionsPath == "" || cacheCollectionsPath == localCollectionsPath {
		return localCollectionsPath
	}
	return localCollectionsPath + string(os.PathListSeparator) + cacheCollectionsPath
}

// newClusterOSCmd is the parent for OS-level fleet operations (currently
// just `update`, with --check / --refresh / --apply gating mutation).
func newClusterOSCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "os",
		Short: "OS-level fleet operations (updates, tuning verification)",
		Long: `Operator-driven OS maintenance.

Production nodes do not install OS updates in the background; the
node_tuning role disables unattended-upgrades and the apt-daily timers.
Use these subcommands to apply updates on a maintenance window.`,
	}
	cmd.AddCommand(newClusterOSUpdateCmd())
	return cmd
}

// newClusterOSUpdateCmd handles both --check inventory and the
// mutating apply path. Flags:
//
//	--check    Inventory of pending updates across the fleet.
//	--refresh  Refresh apt lists during --check (only mutation in check mode).
//	--apply    Run the mutating upgrade playbook (default when --check is off).
//	--hosts    Comma-separated host names to limit the run.
//	--no-reboot Skip the reboot step even if /var/run/reboot-required exists.
func newClusterOSUpdateCmd() *cobra.Command {
	var (
		check         bool
		refresh       bool
		apply         bool
		hostsCSV      string
		noReboot      bool
		mode          string
		jsonMode      bool
		serial        int
		continueOnErr bool
	)

	cmd := &cobra.Command{
		Use:   "update",
		Short: "Inspect or apply pending OS updates across the fleet",
		Long: `Check mode (default): runs apt-get -s upgrade + needrestart -r l on
every host and prints a per-host summary. Pass --refresh when the check
should refresh apt indexes before simulating the upgrade.

Apply mode (--apply): runs the mutating playbook on each host serially by
default, drains nothing (drain is operator responsibility for v1), upgrades
packages, try-restarts affected services, and reboots if required. Honors
--serial and --no-reboot.`,
		Example: `  frameworks cluster os update                    # check mode (default)
  frameworks cluster os update --refresh          # check + refresh apt lists
  frameworks cluster os update --apply            # apply mutating upgrades
  frameworks cluster os update --apply --no-reboot
  frameworks cluster os update --hosts edge-fra-01,edge-fra-02`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !check && !apply {
				check = true
			}
			if check && apply {
				return fmt.Errorf("--check and --apply are mutually exclusive")
			}

			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return err
			}
			defer rc.Cleanup()

			if check {
				return runOSUpdateCheck(cmd, rc, hostsCSV, refresh, jsonMode)
			}
			return runOSUpdateApply(cmd, rc, hostsCSV, mode, noReboot, serial, continueOnErr)
		},
	}

	cmd.Flags().BoolVar(&check, "check", false, "Inventory of pending updates (default when --apply is absent)")
	cmd.Flags().BoolVar(&refresh, "refresh", false, "In --check mode, refresh apt lists (the only mutation check mode performs)")
	cmd.Flags().BoolVar(&apply, "apply", false, "Run the mutating upgrade playbook")
	cmd.Flags().StringVar(&hostsCSV, "hosts", "", "Comma-separated host names to limit the run")
	cmd.Flags().BoolVar(&noReboot, "no-reboot", false, "Skip reboot even if /var/run/reboot-required exists (apply only)")
	cmd.Flags().StringVar(&mode, "mode", "safe", "Upgrade mode for the apt module: safe | full (apply only)")
	cmd.Flags().BoolVar(&jsonMode, "json", false, "Emit one JSON object per host in check mode")
	cmd.Flags().IntVar(&serial, "serial", 1, "How many hosts to process in parallel (apply only)")
	cmd.Flags().BoolVar(&continueOnErr, "continue-on-error", false, "Soft-fail per host instead of halting the fleet run (apply only)")

	return cmd
}

func runOSUpdateCheck(cmd *cobra.Command, rc *resolvedCluster, hostsCSV string, refresh, jsonMode bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	ux.Heading(cmd.OutOrStdout(), "Checking OS update state across fleet")

	sshKey := stringFlag(cmd, "ssh-key").Value
	pool := fwssh.NewPool(30*time.Second, sshKey)
	defer pool.Close()

	hosts, err := osUpdateHostList(rc.Manifest, hostsCSV)
	if err != nil {
		return err
	}
	if len(hosts) == 0 {
		return fmt.Errorf("no hosts resolved from manifest")
	}

	base := provisioner.NewBaseProvisioner("os-update-check", pool)
	results := make([]osUpdateCheckResult, len(hosts))
	g, gCtx := errgroup.WithContext(ctx)
	g.SetLimit(8)
	for i, h := range hosts {
		i, h := i, h
		g.Go(func() error {
			result, err := runOSUpdateHostCheck(gCtx, base, h, refresh)
			results[i] = result
			if err != nil {
				return err
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}

	sort.Slice(results, func(i, j int) bool { return results[i].Host < results[j].Host })
	pending := false
	for _, result := range results {
		if result.Pending {
			pending = true
		}
		if jsonMode {
			if err := json.NewEncoder(cmd.OutOrStdout()).Encode(result); err != nil {
				return err
			}
			continue
		}
		if result.Skipped {
			fmt.Fprintf(cmd.OutOrStdout(), "%s: skipped (non-Debian host)\n", result.Host)
			continue
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s: %s", result.Host, result.UpgradeSummary)
		if result.AptListAgeSeconds >= 0 {
			fmt.Fprintf(cmd.OutOrStdout(), " (apt lists age=%ds)", result.AptListAgeSeconds)
		}
		if len(result.NeedrestartUnits) > 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "; units=%s", strings.Join(result.NeedrestartUnits, ","))
		}
		if result.RebootRequired {
			fmt.Fprintf(cmd.OutOrStdout(), "; reboot required")
			if len(result.RebootRequiredPkgs) > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), " (%s)", strings.Join(result.RebootRequiredPkgs, ","))
			}
		}
		fmt.Fprintln(cmd.OutOrStdout())
	}
	if pending {
		return &ExitCodeError{Code: 2, Message: "OS updates pending"}
	}
	return nil
}

func runOSUpdateApply(cmd *cobra.Command, rc *resolvedCluster, hostsCSV, mode string, noReboot bool, serial int, continueOnErr bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()

	if mode != "safe" && mode != "full" {
		return fmt.Errorf("--mode must be 'safe' or 'full', got %q", mode)
	}
	if serial < 1 {
		serial = 1
	}

	ux.Heading(cmd.OutOrStdout(), "Applying OS updates across fleet")

	return runOSUpdatePlaybook(ctx, cmd, rc, osUpdatePlaybookOpts{
		PlaybookFile: "playbooks/cluster_os_update.yml",
		HostsCSV:     hostsCSV,
		ExtraVars: map[string]any{
			"os_update_mode":          mode,
			"no_reboot":               noReboot,
			"os_update_serial":        serial,
			"os_update_halt_on_error": !continueOnErr,
		},
	})
}

type osUpdatePlaybookOpts struct {
	PlaybookFile string
	HostsCSV     string
	ExtraVars    map[string]any
}

// runOSUpdatePlaybook renders an inventory from the manifest and runs the
// named mutating playbook. Output streams through to the operator's terminal.
func runOSUpdatePlaybook(ctx context.Context, cmd *cobra.Command, rc *resolvedCluster, opts osUpdatePlaybookOpts) error {
	manifest := rc.Manifest
	root, err := provisioner.FindAnsibleRoot()
	if err != nil {
		return fmt.Errorf("locate ansible root: %w", err)
	}
	exec, err := ansiblerun.NewExecutor()
	if err != nil {
		return err
	}
	cache, err := (&ansiblerun.CollectionEnsurer{
		RequirementsFile: filepath.Join(root, "requirements.yml"),
	}).Ensure(ctx)
	if err != nil {
		return fmt.Errorf("ensure ansible collections + roles: %w", err)
	}

	sshKey := stringFlag(cmd, "ssh-key").Value
	hosts, err := osUpdateHostList(manifest, opts.HostsCSV)
	if err != nil {
		return err
	}
	if len(hosts) == 0 {
		return fmt.Errorf("no hosts resolved from manifest")
	}

	invHosts := make([]ansiblerun.Host, 0, len(hosts))
	hostNames := make([]string, 0, len(hosts))
	for _, h := range hosts {
		invHosts = append(invHosts, ansiblerun.Host{
			Name:       h.Name,
			Address:    osUpdateHostAddress(h),
			User:       h.User,
			PrivateKey: sshKey,
		})
		hostNames = append(hostNames, h.Name)
	}

	invDir, err := os.MkdirTemp("", "frameworks-os-update-*")
	if err != nil {
		return fmt.Errorf("mkdtemp: %w", err)
	}
	defer os.RemoveAll(invDir)

	renderer := &ansiblerun.InventoryRenderer{}
	invPath, err := renderer.Render(invDir, ansiblerun.Inventory{
		Hosts: invHosts,
		Groups: []ansiblerun.Group{{
			Name:  "os_update",
			Hosts: hostNames,
		}},
	})
	if err != nil {
		return fmt.Errorf("render inventory: %w", err)
	}

	envVars := map[string]string{
		"ANSIBLE_COLLECTIONS_PATH": osUpdateCollectionsPath(root, cache.CollectionsPath),
		"ANSIBLE_ROLES_PATH":       cache.RolesPath,
	}
	for _, k := range []string{"SOPS_AGE_KEY_FILE", "SOPS_AGE_KEY", "HOME", "USER", "PATH"} {
		if v := os.Getenv(k); v != "" {
			envVars[k] = v
		}
	}

	return exec.Execute(ctx, ansiblerun.ExecuteOptions{
		Playbook:   filepath.Join(root, opts.PlaybookFile),
		Inventory:  invPath,
		ExtraVars:  opts.ExtraVars,
		PrivateKey: sshKey,
		Become:     true,
		WorkDir:    root,
		EnvVars:    envVars,
	})
}

// osUpdateHostList returns the manifest hosts that the OS-update command
// should target. When hostsCSV is empty, all manifest hosts are returned.
// Hosts in the manifest are presumed Debian-family; the playbook itself
// short-circuits with `meta: end_host` on non-Debian, so over-selection
// here is harmless.
func osUpdateHostList(manifest *inventory.Manifest, hostsCSV string) ([]inventory.Host, error) {
	if manifest == nil {
		return nil, nil
	}
	want := map[string]bool{}
	if hostsCSV != "" {
		for _, name := range splitCSVStrings(hostsCSV) {
			want[name] = true
		}
	}
	out := make([]inventory.Host, 0, len(manifest.Hosts))
	matched := make(map[string]bool, len(want))
	for _, h := range manifest.Hosts {
		// Filter against the immutable want set; mutating it mid-loop would
		// disable filtering for hosts visited after the last wanted match.
		if len(want) > 0 && !want[h.Name] {
			continue
		}
		out = append(out, h)
		matched[h.Name] = true
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	if len(want) > 0 {
		missing := make([]string, 0, len(want))
		for name := range want {
			if !matched[name] {
				missing = append(missing, name)
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			return nil, fmt.Errorf("unknown host(s): %s", strings.Join(missing, ", "))
		}
	}
	return out, nil
}

type osUpdateCheckResult struct {
	Host               string   `json:"host"`
	Skipped            bool     `json:"skipped,omitempty"`
	AptListAgeSeconds  int64    `json:"apt_list_age_seconds"`
	UpgradeSummary     string   `json:"upgrade_summary"`
	NeedrestartUnits   []string `json:"needrestart_units"`
	RebootRequired     bool     `json:"reboot_required"`
	RebootRequiredPkgs []string `json:"reboot_required_pkgs"`
	Pending            bool     `json:"pending"`
}

func runOSUpdateHostCheck(ctx context.Context, base *provisioner.BaseProvisioner, host inventory.Host, refresh bool) (osUpdateCheckResult, error) {
	result := osUpdateCheckResult{
		Host:              host.Name,
		AptListAgeSeconds: -1,
		UpgradeSummary:    "0 upgraded, 0 newly installed, 0 to remove",
	}
	runner, err := base.GetRunner(host)
	if err != nil {
		return result, fmt.Errorf("%s: connect: %w", host.Name, err)
	}

	script := osUpdateCheckScript(refresh)
	command := "if [ \"$(id -u)\" -eq 0 ]; then sh -c " + fwssh.ShellQuote(script) + "; else sudo sh -c " + fwssh.ShellQuote(script) + "; fi"
	cmdResult, err := runner.Run(ctx, command)
	if err != nil {
		return result, fmt.Errorf("%s: check failed: %w", host.Name, err)
	}
	return parseOSUpdateCheckOutput(cmdResult.Stdout, result), nil
}

// parseOSUpdateCheckOutput folds the FW_* key/value lines emitted by
// osUpdateCheckScript into the result and derives the Pending flag. It is pure:
// base carries the caller-initialized defaults (Host, AptListAgeSeconds=-1,
// the empty UpgradeSummary baseline).
func parseOSUpdateCheckOutput(stdout string, base osUpdateCheckResult) osUpdateCheckResult {
	result := base
	for _, line := range strings.Split(stdout, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "FW_OS_FAMILY":
			if value == "skip" {
				result.Skipped = true
			}
		case "FW_APT_LIST_AGE_SECONDS":
			if age, parseErr := strconv.ParseInt(value, 10, 64); parseErr == nil {
				result.AptListAgeSeconds = age
			}
		case "FW_UPGRADE_SUMMARY":
			result.UpgradeSummary = value
		case "FW_NEEDRESTART_UNIT":
			if value != "" {
				result.NeedrestartUnits = append(result.NeedrestartUnits, value)
			}
		case "FW_REBOOT_REQUIRED":
			result.RebootRequired = value == "true"
		case "FW_REBOOT_PKG":
			if value != "" {
				result.RebootRequiredPkgs = append(result.RebootRequiredPkgs, value)
			}
		}
	}
	result.Pending = !strings.HasPrefix(result.UpgradeSummary, "0 upgraded") ||
		len(result.NeedrestartUnits) > 0 ||
		result.RebootRequired
	return result
}

func osUpdateCheckScript(refresh bool) string {
	refreshValue := "false"
	if refresh {
		refreshValue = "true"
	}
	return fmt.Sprintf(`set -eu
if [ ! -f /etc/debian_version ]; then
  echo FW_OS_FAMILY=skip
  exit 0
fi
echo FW_OS_FAMILY=debian
if [ %s = true ]; then
  DEBIAN_FRONTEND=noninteractive apt-get update -qq
fi
if [ -e /var/lib/apt/periodic/update-success-stamp ]; then
  now=$(date +%%s)
  stamp=$(stat -c %%Y /var/lib/apt/periodic/update-success-stamp)
  echo FW_APT_LIST_AGE_SECONDS=$((now - stamp))
else
  echo FW_APT_LIST_AGE_SECONDS=-1
fi
DEBIAN_FRONTEND=noninteractive apt-get -s upgrade | awk '
  /^[0-9]+ upgraded/ { print "FW_UPGRADE_SUMMARY="$0; found=1 }
  END { if (!found) print "FW_UPGRADE_SUMMARY=0 upgraded, 0 newly installed, 0 to remove" }
'
if ! command -v needrestart >/dev/null 2>&1; then
  echo "needrestart is required; run frameworks cluster provision to apply node_tuning" >&2
  exit 42
fi
needrestart -r l -b | sed -n 's/^NEEDRESTART-SVC:[[:space:]]*/FW_NEEDRESTART_UNIT=/p'
if [ -e /var/run/reboot-required ]; then
  echo FW_REBOOT_REQUIRED=true
  if [ -e /var/run/reboot-required.pkgs ]; then
    sed 's/^/FW_REBOOT_PKG=/' /var/run/reboot-required.pkgs
  fi
else
  echo FW_REBOOT_REQUIRED=false
fi
`, refreshValue)
}
