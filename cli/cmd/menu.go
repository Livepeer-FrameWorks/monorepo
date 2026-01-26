package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

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
	for {
		fmt.Fprintln(cmd.OutOrStdout(), "\n=== Frameworks CLI ===")
		fmt.Fprintln(cmd.OutOrStdout(), "1) Edge Operations")
		fmt.Fprintln(cmd.OutOrStdout(), "2) Services & Health")
		fmt.Fprintln(cmd.OutOrStdout(), "3) Control Plane (Admin)")
		fmt.Fprintln(cmd.OutOrStdout(), "4) Cluster Operations")
		fmt.Fprintln(cmd.OutOrStdout(), "5) DNS & Mesh")
		fmt.Fprintln(cmd.OutOrStdout(), "6) Settings & Contexts")
		fmt.Fprintln(cmd.OutOrStdout(), "0) Exit")
		fmt.Fprint(cmd.OutOrStdout(), "> ")
		choice, _ := reader.ReadString('\n')
		switch strings.TrimSpace(choice) {
		case "1":
			edgeMenu(cmd, reader)
		case "2":
			servicesMenu(cmd, reader)
		case "3":
			controlPlaneMenu(cmd, reader)
		case "4":
			clusterOpsMenu(cmd, reader)
		case "5":
			dnsMeshMenu(cmd, reader)
		case "6":
			settingsMenu(cmd, reader)
		case "0":
			return nil
		default:
			fmt.Fprintln(cmd.OutOrStdout(), "Unknown option")
		}
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
		c, _ := r.ReadString('\n')
		switch strings.TrimSpace(c) {
		case "1":
			_ = newEdgePreflightCmd().Execute()
		case "2":
			_ = newEdgeTuneCmd().Execute()
		case "3":
			_ = newEdgeInitCmd().Execute()
		case "4":
			_ = newEdgeEnrollCmd().Execute()
		case "5":
			_ = newEdgeStatusCmd().Execute()
		case "6":
			_ = newEdgeUpdateCmd().Execute()
		case "7":
			_ = newEdgeCertCmd().Execute()
		case "8":
			_ = newEdgeLogsCmd().Execute()
		case "9":
			_ = newEdgeDoctorCmd().Execute()
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
		c, _ := r.ReadString('\n')
		switch strings.TrimSpace(c) {
		case "1":
			_ = newServicesHealthCmd().Execute()
		case "2":
			svcType := promptInputDefault(r, "Service type", "")
			if svcType == "" {
				fmt.Fprintln(cmd.OutOrStdout(), "Service type is required")
				continue
			}
			hc := newServicesHealthCmd()
			_ = hc.Flags().Set("type", svcType)
			_ = hc.Execute()
		case "3":
			svcType := promptInputDefault(r, "Service type", "")
			if svcType == "" {
				fmt.Fprintln(cmd.OutOrStdout(), "Service type is required")
				continue
			}
			dc := newServicesDiscoverCmd()
			_ = dc.Flags().Set("type", svcType)
			_ = dc.Execute()
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
		c, _ := r.ReadString('\n')
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
			_ = cc.Execute()
		case "2":
			_ = newAdminBootstrapTokensListCmd().Execute()
		case "3":
			tokenID := promptInputDefault(r, "Token ID (leave empty to use name)", "")
			if tokenID != "" {
				rc := newAdminBootstrapTokensRevokeCmd()
				rc.SetArgs([]string{tokenID})
				_ = rc.Execute()
				continue
			}
			name := promptInputDefault(r, "Token name", "")
			rc := newAdminBootstrapTokensRevokeCmd()
			if name != "" {
				_ = rc.Flags().Set("name", name)
			}
			_ = rc.Execute()
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
		c, _ := r.ReadString('\n')
		switch strings.TrimSpace(c) {
		case "1":
			manifest := promptInputDefault(r, "Manifest path", "cluster.yaml")
			cc := newClusterDetectCmd()
			_ = cc.Flags().Set("manifest", manifest)
			_ = cc.Execute()
		case "2":
			manifest := promptInputDefault(r, "Manifest path", "cluster.yaml")
			cc := newClusterDoctorCmd()
			_ = cc.Flags().Set("manifest", manifest)
			_ = cc.Execute()
		case "3":
			manifest := promptInputDefault(r, "Manifest path", "cluster.yaml")
			phase := promptInputDefault(r, "Phase (infrastructure|applications|interfaces|all)", "all")
			cc := newClusterProvisionCmd()
			_ = cc.Flags().Set("manifest", manifest)
			if phase != "" {
				_ = cc.Flags().Set("only", phase)
			}
			_ = cc.Execute()
		case "4":
			manifest := promptInputDefault(r, "Manifest path", "cluster.yaml")
			service := promptInputDefault(r, "Service (postgres|kafka|clickhouse|all)", "all")
			cc := newClusterInitCmd()
			cc.SetArgs([]string{service})
			_ = cc.Flags().Set("manifest", manifest)
			_ = cc.Execute()
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
			_ = cc.Execute()
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
			_ = cc.Execute()
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
			_ = cc.Execute()
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
		c, _ := r.ReadString('\n')
		switch strings.TrimSpace(c) {
		case "1":
			domain := promptInputDefault(r, "Root domain", "frameworks.network")
			cc := newDNSDoctorCmd()
			_ = cc.Flags().Set("domain", domain)
			_ = cc.Execute()
		case "2":
			_ = newMeshStatusCmd().Execute()
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
		c, _ := r.ReadString('\n')
		switch strings.TrimSpace(c) {
		case "1":
			_ = newLoginCmd().Execute()
		case "2":
			_ = newContextListCmd().Execute()
		case "3":
			name := promptInputDefault(r, "Context name", "")
			if name == "" {
				fmt.Fprintln(cmd.OutOrStdout(), "Context name is required")
				continue
			}
			cc := newContextUseCmd()
			cc.SetArgs([]string{name})
			_ = cc.Execute()
		case "4":
			fmt.Fprintln(cmd.OutOrStdout(), "Config: $HOME/.frameworks/config.yaml (if present)")
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
	input, _ := r.ReadString('\n')
	input = strings.TrimSpace(input)
	if input == "" {
		return def
	}
	return input
}
