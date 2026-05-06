package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	fwcfg "frameworks/cli/internal/config"
	"frameworks/cli/internal/ux"

	"github.com/spf13/cobra"
)

type menuSection struct {
	key         string // "account", "edge", "services", "control-plane", "cluster", "dns-mesh", "settings"
	label       string
	recommended bool
}

// menuSectionsForPersona returns the sections in display order with
// recommendation tags set for the active persona. No section is ever
// hidden; power users keep access to everything.
func menuSectionsForPersona(p fwcfg.Persona) []menuSection {
	account := menuSection{key: "account", label: "Account & Hosted"}
	edge := menuSection{key: "edge", label: "Edge Operations"}
	services := menuSection{key: "services", label: "Services & Health"}
	controlPlane := menuSection{key: "control-plane", label: "Control Plane (Admin)"}
	cluster := menuSection{key: "cluster", label: "Cluster Operations"}
	dnsMesh := menuSection{key: "dns-mesh", label: "DNS & Mesh"}
	settings := menuSection{key: "settings", label: "Settings & Contexts"}

	switch p {
	case fwcfg.PersonaUser, fwcfg.PersonaEdge:
		account.recommended = true
		return []menuSection{account, settings, edge, services, cluster, controlPlane, dnsMesh}
	case fwcfg.PersonaSelfHosted:
		edge.recommended = true
		return []menuSection{edge, account, settings, services, cluster, controlPlane, dnsMesh}
	case fwcfg.PersonaPlatform:
		cluster.recommended = true
		controlPlane.recommended = true
		return []menuSection{cluster, controlPlane, services, dnsMesh, edge, account, settings}
	default:
		return []menuSection{account, edge, services, controlPlane, cluster, dnsMesh, settings}
	}
}

func newMenuCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "menu",
		Short: "Interactive menu for Frameworks operations",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMainMenu(cmd)
		},
	}
}

func runMainMenu(cmd *cobra.Command) error {
	reader := bufio.NewReader(os.Stdin)
	sections := menuSectionsForPersona(activePersona())
	out := cmd.OutOrStdout()

	for {
		fmt.Fprintln(out, "\n=== Frameworks CLI ===")
		for i, s := range sections {
			tag := ""
			if s.recommended {
				tag = " [Recommended]"
			}
			fmt.Fprintf(out, "%d) %s%s\n", i+1, s.label, tag)
		}
		fmt.Fprintln(out, "0) Exit")
		fmt.Fprint(out, "> ")
		choice, _ := reader.ReadString('\n') //nolint:errcheck // interactive prompt; err yields empty string -> unknown-option path below

		trimmed := strings.TrimSpace(choice)
		if trimmed == "0" {
			return nil
		}
		idx, convErr := strconv.Atoi(trimmed)
		if convErr != nil || idx < 1 || idx > len(sections) {
			ux.Warn(out, "Unknown option")
			continue
		}
		section := sections[idx-1]
		switch section.key {
		case "account":
			accountMenu(cmd, reader)
		case "edge":
			edgeMenu(cmd, reader)
		case "services":
			servicesMenu(cmd, reader)
		case "control-plane":
			controlPlaneMenu(cmd, reader)
		case "cluster":
			clusterOpsMenu(cmd, reader)
		case "dns-mesh":
			dnsMeshMenu(cmd, reader)
		case "settings":
			settingsMenu(cmd, reader)
		}
	}
}

func accountMenu(cmd *cobra.Command, r *bufio.Reader) {
	for {
		fmt.Fprintln(cmd.OutOrStdout(), "\n-- Account & Hosted --")
		fmt.Fprintln(cmd.OutOrStdout(), "1) Login")
		fmt.Fprintln(cmd.OutOrStdout(), "2) Logout")
		fmt.Fprintln(cmd.OutOrStdout(), "3) Context check")
		fmt.Fprintln(cmd.OutOrStdout(), "0) Back")
		fmt.Fprint(cmd.OutOrStdout(), "> ")
		c, ok := readMenuChoice(cmd, r)
		if !ok {
			return
		}
		switch strings.TrimSpace(c) {
		case "1":
			runMenuCommand(cmd, newLoginCmd())
		case "2":
			runMenuCommand(cmd, newLogoutCmd())
		case "3":
			runMenuCommand(cmd, newContextCheckCmd())
		case "0":
			return
		default:
			fmt.Fprintln(cmd.OutOrStdout(), "Unknown option")
		}
	}
}

func readMenuChoice(cmd *cobra.Command, r *bufio.Reader) (string, bool) {
	choice, err := r.ReadString('\n')
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "read menu choice: %v\n", err)
		return "", false
	}
	return choice, true
}

func runMenuCommand(parent, child *cobra.Command) {
	child.SetOut(parent.OutOrStdout())
	child.SetErr(parent.ErrOrStderr())
	if err := child.Execute(); err != nil {
		fmt.Fprintf(parent.ErrOrStderr(), "%v\n", err)
	}
}

func edgeMenu(cmd *cobra.Command, r *bufio.Reader) {
	for {
		fmt.Fprintln(cmd.OutOrStdout(), "\n-- Edge Operations --")
		fmt.Fprintln(cmd.OutOrStdout(), "1) Preflight checks")
		fmt.Fprintln(cmd.OutOrStdout(), "2) Tune host (sysctl/limits)")
		fmt.Fprintln(cmd.OutOrStdout(), "3) Init (.edge.env + templates)")
		fmt.Fprintln(cmd.OutOrStdout(), "4) Enroll (start stack)")
		fmt.Fprintln(cmd.OutOrStdout(), "5) Status")
		fmt.Fprintln(cmd.OutOrStdout(), "6) Update (pull + up -d)")
		fmt.Fprintln(cmd.OutOrStdout(), "7) Cert renew")
		fmt.Fprintln(cmd.OutOrStdout(), "8) Logs")
		fmt.Fprintln(cmd.OutOrStdout(), "9) Doctor")
		fmt.Fprintln(cmd.OutOrStdout(), "0) Back")
		fmt.Fprint(cmd.OutOrStdout(), "> ")
		c, ok := readMenuChoice(cmd, r)
		if !ok {
			return
		}
		switch strings.TrimSpace(c) {
		case "1":
			runMenuCommand(cmd, newEdgePreflightCmd())
		case "2":
			runMenuCommand(cmd, newEdgeTuneCmd())
		case "3":
			runMenuCommand(cmd, newEdgeInitCmd())
		case "4":
			runMenuCommand(cmd, newEdgeEnrollCmd())
		case "5":
			runMenuCommand(cmd, newEdgeStatusCmd())
		case "6":
			runMenuCommand(cmd, newEdgeUpdateCmd())
		case "7":
			runMenuCommand(cmd, newEdgeCertCmd())
		case "8":
			runMenuCommand(cmd, newEdgeLogsCmd())
		case "9":
			runMenuCommand(cmd, newEdgeDoctorCmd())
		case "0":
			return
		default:
			fmt.Fprintln(cmd.OutOrStdout(), "Unknown option")
		}
	}
}

func servicesMenu(cmd *cobra.Command, r *bufio.Reader) {
	for {
		fmt.Fprintln(cmd.OutOrStdout(), "\n-- Services & Health --")
		fmt.Fprintln(cmd.OutOrStdout(), "1) Health overview")
		fmt.Fprintln(cmd.OutOrStdout(), "2) Service health by type")
		fmt.Fprintln(cmd.OutOrStdout(), "3) Discover instances")
		fmt.Fprintln(cmd.OutOrStdout(), "0) Back")
		fmt.Fprint(cmd.OutOrStdout(), "> ")
		c, ok := readMenuChoice(cmd, r)
		if !ok {
			return
		}
		switch strings.TrimSpace(c) {
		case "1":
			runMenuCommand(cmd, newServicesHealthCmd())
		case "2":
			svcType := promptInputDefault(r, "Service type", "")
			if svcType == "" {
				fmt.Fprintln(cmd.OutOrStdout(), "Service type is required")
				continue
			}
			hc := newServicesHealthCmd()
			_ = hc.Flags().Set("type", svcType)
			runMenuCommand(cmd, hc)
		case "3":
			svcType := promptInputDefault(r, "Service type", "")
			if svcType == "" {
				fmt.Fprintln(cmd.OutOrStdout(), "Service type is required")
				continue
			}
			dc := newServicesDiscoverCmd()
			_ = dc.Flags().Set("type", svcType)
			runMenuCommand(cmd, dc)
		case "0":
			return
		default:
			fmt.Fprintln(cmd.OutOrStdout(), "Unknown option")
		}
	}
}

func controlPlaneMenu(cmd *cobra.Command, r *bufio.Reader) {
	for {
		fmt.Fprintln(cmd.OutOrStdout(), "\n-- Control Plane (Admin) --")
		fmt.Fprintln(cmd.OutOrStdout(), "1) Create bootstrap token")
		fmt.Fprintln(cmd.OutOrStdout(), "2) List bootstrap tokens")
		fmt.Fprintln(cmd.OutOrStdout(), "3) Revoke bootstrap token")
		fmt.Fprintln(cmd.OutOrStdout(), "0) Back")
		fmt.Fprint(cmd.OutOrStdout(), "> ")
		c, ok := readMenuChoice(cmd, r)
		if !ok {
			return
		}
		switch strings.TrimSpace(c) {
		case "1":
			kind := promptInputDefault(r, "Token kind (edge_node|service|infrastructure_node)", "edge_node")
			tenantID := ""
			clusterID := ""
			if kind == "edge_node" {
				tenantID = promptInputDefault(r, "Tenant ID", "")
				clusterID = promptInputDefault(r, "Cluster ID", "")
			}
			expectedIP := promptInputDefault(r, "Expected IP (optional)", "")
			ttl := promptInputDefault(r, "TTL (e.g. 24h)", "")
			name := promptInputDefault(r, "Name", "")
			usageLimit := promptInputDefault(r, "Usage limit (optional)", "")

			cc := newAdminBootstrapTokensCreateCmd()
			_ = cc.Flags().Set("kind", kind)
			if tenantID != "" {
				_ = cc.Flags().Set("tenant-id", tenantID)
			}
			if clusterID != "" {
				_ = cc.Flags().Set("cluster-id", clusterID)
			}
			if expectedIP != "" {
				_ = cc.Flags().Set("expected-ip", expectedIP)
			}
			if ttl != "" {
				_ = cc.Flags().Set("ttl", ttl)
			}
			if name != "" {
				_ = cc.Flags().Set("name", name)
			}
			if usageLimit != "" {
				if v, err := strconv.Atoi(usageLimit); err == nil {
					_ = cc.Flags().Set("usage-limit", fmt.Sprintf("%d", v))
				}
			}
			runMenuCommand(cmd, cc)
		case "2":
			runMenuCommand(cmd, newAdminBootstrapTokensListCmd())
		case "3":
			tokenID := promptInputDefault(r, "Token ID (leave empty to use name)", "")
			if tokenID != "" {
				rc := newAdminBootstrapTokensRevokeCmd()
				rc.SetArgs([]string{tokenID})
				runMenuCommand(cmd, rc)
				continue
			}
			name := promptInputDefault(r, "Token name", "")
			rc := newAdminBootstrapTokensRevokeCmd()
			if name != "" {
				_ = rc.Flags().Set("name", name)
			}
			runMenuCommand(cmd, rc)
		case "0":
			return
		default:
			fmt.Fprintln(cmd.OutOrStdout(), "Unknown option")
		}
	}
}

func clusterOpsMenu(cmd *cobra.Command, r *bufio.Reader) {
	for {
		fmt.Fprintln(cmd.OutOrStdout(), "\n-- Cluster Operations --")
		fmt.Fprintln(cmd.OutOrStdout(), "1) Detect")
		fmt.Fprintln(cmd.OutOrStdout(), "2) Doctor")
		fmt.Fprintln(cmd.OutOrStdout(), "3) Provision")
		fmt.Fprintln(cmd.OutOrStdout(), "4) Init")
		fmt.Fprintln(cmd.OutOrStdout(), "5) Upgrade")
		fmt.Fprintln(cmd.OutOrStdout(), "6) Logs")
		fmt.Fprintln(cmd.OutOrStdout(), "7) Restart")
		fmt.Fprintln(cmd.OutOrStdout(), "0) Back")
		fmt.Fprint(cmd.OutOrStdout(), "> ")
		c, ok := readMenuChoice(cmd, r)
		if !ok {
			return
		}
		switch strings.TrimSpace(c) {
		case "1":
			manifest := promptInputDefault(r, "Manifest path", "cluster.yaml")
			cc := newClusterDetectCmd()
			_ = cc.Flags().Set("manifest", manifest)
			runMenuCommand(cmd, cc)
		case "2":
			manifest := promptInputDefault(r, "Manifest path", "cluster.yaml")
			cc := newClusterDoctorCmd()
			_ = cc.Flags().Set("manifest", manifest)
			runMenuCommand(cmd, cc)
		case "3":
			manifest := promptInputDefault(r, "Manifest path", "cluster.yaml")
			phase := promptInputDefault(r, "Phase (infrastructure|applications|interfaces|all)", "all")
			cc := newClusterProvisionCmd()
			_ = cc.Flags().Set("manifest", manifest)
			if phase != "" {
				_ = cc.Flags().Set("only", phase)
			}
			runMenuCommand(cmd, cc)
		case "4":
			manifest := promptInputDefault(r, "Manifest path", "cluster.yaml")
			service := promptInputDefault(r, "Service (postgres|kafka|clickhouse|all)", "all")
			cc := newClusterInitCmd()
			cc.SetArgs([]string{service})
			_ = cc.Flags().Set("manifest", manifest)
			runMenuCommand(cmd, cc)
		case "5":
			manifest := promptInputDefault(r, "Manifest path", "cluster.yaml")
			service := promptInputDefault(r, "Service to upgrade", "")
			if service == "" {
				fmt.Fprintln(cmd.OutOrStdout(), "Service name is required")
				continue
			}
			version := promptInputDefault(r, "Version (stable|rc|vX.Y.Z)", "")
			cc := newClusterUpgradeCmd()
			cc.SetArgs([]string{service})
			_ = cc.Flags().Set("manifest", manifest)
			if version != "" {
				_ = cc.Flags().Set("version", version)
			}
			runMenuCommand(cmd, cc)
		case "6":
			manifest := promptInputDefault(r, "Manifest path", "cluster.yaml")
			service := promptInputDefault(r, "Service to tail", "")
			if service == "" {
				fmt.Fprintln(cmd.OutOrStdout(), "Service name is required")
				continue
			}
			cc := newClusterLogsCmd()
			cc.SetArgs([]string{service})
			_ = cc.Flags().Set("manifest", manifest)
			runMenuCommand(cmd, cc)
		case "7":
			manifest := promptInputDefault(r, "Manifest path", "cluster.yaml")
			service := promptInputDefault(r, "Service to restart", "")
			if service == "" {
				fmt.Fprintln(cmd.OutOrStdout(), "Service name is required")
				continue
			}
			cc := newClusterRestartCmd()
			cc.SetArgs([]string{service})
			_ = cc.Flags().Set("manifest", manifest)
			runMenuCommand(cmd, cc)
		case "0":
			return
		default:
			fmt.Fprintln(cmd.OutOrStdout(), "Unknown option")
		}
	}
}

func dnsMeshMenu(cmd *cobra.Command, r *bufio.Reader) {
	for {
		fmt.Fprintln(cmd.OutOrStdout(), "\n-- DNS & Mesh --")
		fmt.Fprintln(cmd.OutOrStdout(), "1) DNS doctor")
		fmt.Fprintln(cmd.OutOrStdout(), "2) Mesh status")
		fmt.Fprintln(cmd.OutOrStdout(), "0) Back")
		fmt.Fprint(cmd.OutOrStdout(), "> ")
		c, ok := readMenuChoice(cmd, r)
		if !ok {
			return
		}
		switch strings.TrimSpace(c) {
		case "1":
			domain := promptInputDefault(r, "Root domain", "frameworks.network")
			cc := newDNSDoctorCmd()
			_ = cc.Flags().Set("domain", domain)
			runMenuCommand(cmd, cc)
		case "2":
			runMenuCommand(cmd, newMeshStatusCmd())
		case "0":
			return
		default:
			fmt.Fprintln(cmd.OutOrStdout(), "Unknown option")
		}
	}
}

func settingsMenu(cmd *cobra.Command, r *bufio.Reader) {
	for {
		fmt.Fprintln(cmd.OutOrStdout(), "\n-- Settings & Contexts --")
		fmt.Fprintln(cmd.OutOrStdout(), "1) Login (Bridge)")
		fmt.Fprintln(cmd.OutOrStdout(), "2) List contexts")
		fmt.Fprintln(cmd.OutOrStdout(), "3) Switch context")
		fmt.Fprintln(cmd.OutOrStdout(), "4) Show config path")
		fmt.Fprintln(cmd.OutOrStdout(), "0) Back")
		fmt.Fprint(cmd.OutOrStdout(), "> ")
		c, ok := readMenuChoice(cmd, r)
		if !ok {
			return
		}
		switch strings.TrimSpace(c) {
		case "1":
			runMenuCommand(cmd, newLoginCmd())
		case "2":
			runMenuCommand(cmd, newContextListCmd())
		case "3":
			name := promptInputDefault(r, "Context name", "")
			if name == "" {
				fmt.Fprintln(cmd.OutOrStdout(), "Context name is required")
				continue
			}
			cc := newContextUseCmd()
			cc.SetArgs([]string{name})
			runMenuCommand(cmd, cc)
		case "4":
			path, err := fwcfg.ConfigPath()
			if err != nil {
				path = "(unavailable)"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Config: %s (if present)\n", path)
		case "0":
			return
		default:
			fmt.Fprintln(cmd.OutOrStdout(), "Unknown option")
		}
	}
}

func promptInputDefault(r *bufio.Reader, label, def string) string {
	if def != "" {
		fmt.Fprintf(os.Stdout, "%s [%s]: ", label, def)
	} else {
		fmt.Fprintf(os.Stdout, "%s: ", label)
	}
	input, err := r.ReadString('\n')
	if err != nil {
		fmt.Fprintf(os.Stderr, "read input: %v\n", err)
		return def
	}
	input = strings.TrimSpace(input)
	if input == "" {
		return def
	}
	return input
}
