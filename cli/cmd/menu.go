package cmd

import (
	"bufio"
	"fmt"
	"os"
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
		fmt.Fprintln(cmd.OutOrStdout(), "4) Infrastructure (Provisioning)")
		fmt.Fprintln(cmd.OutOrStdout(), "5) Developer Workspace")
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
			infraMenu(cmd, reader)
		case "5":
			devWorkspaceMenu(cmd, reader)
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
			fmt.Fprintln(cmd.OutOrStdout(), "TODO: frameworks services health")
		case "2":
			fmt.Fprintln(cmd.OutOrStdout(), "TODO: frameworks services health --service <type>")
		case "3":
			fmt.Fprintln(cmd.OutOrStdout(), "TODO: frameworks services discover --type <name>")
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
		fmt.Fprintln(cmd.OutOrStdout(), "4) Drain node (future)")
		fmt.Fprintln(cmd.OutOrStdout(), "5) Undrain node (future)")
		fmt.Fprintln(cmd.OutOrStdout(), "0) Back")
		fmt.Fprint(cmd.OutOrStdout(), "> ")
		c, _ := r.ReadString('\n')
		switch strings.TrimSpace(c) {
		case "1":
			fmt.Fprintln(cmd.OutOrStdout(), "TODO: frameworks services tokens create")
		case "2":
			fmt.Fprintln(cmd.OutOrStdout(), "TODO: frameworks services tokens list")
		case "3":
			fmt.Fprintln(cmd.OutOrStdout(), "TODO: frameworks services tokens revoke")
		case "4", "5":
			fmt.Fprintln(cmd.OutOrStdout(), "TODO: Foghorn drain/undrain when available")
		case "0":
			return
		default:
			fmt.Fprintln(cmd.OutOrStdout(), "Unknown option")
		}
	}
}

func infraMenu(cmd *cobra.Command, r *bufio.Reader) {
	for {
		fmt.Fprintln(cmd.OutOrStdout(), "\n-- Infrastructure (Provisioning) --")
		fmt.Fprintln(cmd.OutOrStdout(), "1) Plan (future)")
		fmt.Fprintln(cmd.OutOrStdout(), "2) Apply (future)")
		fmt.Fprintln(cmd.OutOrStdout(), "3) Destroy (future)")
		fmt.Fprintln(cmd.OutOrStdout(), "0) Back")
		fmt.Fprint(cmd.OutOrStdout(), "> ")
		c, _ := r.ReadString('\n')
		switch strings.TrimSpace(c) {
		case "1", "2", "3":
			fmt.Fprintln(cmd.OutOrStdout(), "TODO: infra provisioning coming soon")
		case "0":
			return
		default:
			fmt.Fprintln(cmd.OutOrStdout(), "Unknown option")
		}
	}
}

func devWorkspaceMenu(cmd *cobra.Command, r *bufio.Reader) {
	for {
		fmt.Fprintln(cmd.OutOrStdout(), "\n-- Developer Workspace --")
		fmt.Fprintln(cmd.OutOrStdout(), "1) Workspace init (clone repos)")
		fmt.Fprintln(cmd.OutOrStdout(), "2) Build Helmsman (future)")
		fmt.Fprintln(cmd.OutOrStdout(), "3) Build Mist (future)")
		fmt.Fprintln(cmd.OutOrStdout(), "4) Start dev env (future)")
		fmt.Fprintln(cmd.OutOrStdout(), "5) Stop dev env (future)")
		fmt.Fprintln(cmd.OutOrStdout(), "0) Back")
		fmt.Fprint(cmd.OutOrStdout(), "> ")
		c, _ := r.ReadString('\n')
		switch strings.TrimSpace(c) {
		case "1":
			fmt.Fprintln(cmd.OutOrStdout(), "TODO: frameworks workspace init")
		case "2", "3", "4", "5":
			fmt.Fprintln(cmd.OutOrStdout(), "TODO: coming soon")
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
		fmt.Fprintln(cmd.OutOrStdout(), "1) Login (Gateway)")
		fmt.Fprintln(cmd.OutOrStdout(), "2) Switch context")
		fmt.Fprintln(cmd.OutOrStdout(), "3) Show config path")
		fmt.Fprintln(cmd.OutOrStdout(), "0) Back")
		fmt.Fprint(cmd.OutOrStdout(), "> ")
		c, _ := r.ReadString('\n')
		switch strings.TrimSpace(c) {
		case "1":
			_ = newLoginCmd().Execute()
		case "2":
			fmt.Fprintln(cmd.OutOrStdout(), "TODO: frameworks context use <name>")
		case "3":
			fmt.Fprintln(cmd.OutOrStdout(), "Config: $HOME/.frameworks/config.yaml (if present)")
		case "0":
			return
		default:
			fmt.Fprintln(cmd.OutOrStdout(), "Unknown option")
		}
	}
}
