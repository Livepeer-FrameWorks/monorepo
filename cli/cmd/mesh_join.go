package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"frameworks/cli/internal/ux"
	fwssh "frameworks/cli/pkg/ssh"

	"github.com/spf13/cobra"
)

// newMeshJoinCmd wires `frameworks mesh join <ssh-target>` — an operator-
// side command that renders Privateer's env file and writes it onto the
// target host over SSH. Runs on the operator's machine, mirroring how
// `cluster provision` already delivers config to nodes; the fresh node
// itself has no GitOps checkout, no age key, and no CLI context, so we
// don't try to resolve secrets from there.
//
// Privateer itself generates the WireGuard private key locally, calls
// Bridge's public bootstrap endpoint with only its public half, and
// applies the mesh identity Quartermaster assigns. This command is the
// operator helper that gets SERVICE_TOKEN + the bootstrap hints onto the
// target host in one shot.
func newMeshJoinCmd() *cobra.Command {
	var (
		token         string
		bootstrapAddr string
		nodeName      string
		nodeType      string
		clusterID     string
		envPath       string
		sshUser       string
		sshKey        string
		dryRun        bool
	)

	cmd := &cobra.Command{
		Use:   "join <ssh-target>",
		Short: "Enroll a remote host into a running cluster's mesh via SSH",
		Long: `Resolves SERVICE_TOKEN and other cluster-level config from the same
manifest + SOPS env path 'cluster provision' uses, composes Privateer's
env file in memory, and writes it to /etc/privateer/privateer.env on the
target over SSH.

<ssh-target> is either host or user@host. The SSH user resolution order is
--ssh-user flag > user@ prefix > manifest active-context default.

After the env lands on the target, the operator runs
  ssh <ssh-target> systemctl enable --now frameworks-privateer
and Privateer does the enrollment: generates its keypair locally, POSTs
to BRIDGE_BOOTSTRAP_ADDR, and applies the mesh identity the server assigns.

--dry-run renders the env contents with MESH_JOIN_TOKEN and SERVICE_TOKEN
redacted and does not SSH to the target.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target, err := parseSSHTarget(args[0], sshUser)
			if err != nil {
				return err
			}

			if strings.TrimSpace(token) == "" {
				return fmt.Errorf("--token is required")
			}
			if strings.TrimSpace(bootstrapAddr) == "" {
				return fmt.Errorf("--bootstrap-addr is required (Bridge's public URL, e.g. https://bridge.example.com)")
			}
			if strings.TrimSpace(nodeName) == "" {
				nodeName = target.address
			}
			if strings.TrimSpace(nodeType) == "" {
				nodeType = "core"
			}

			// Resolve the cluster's shared env (SOPS-decrypted) to find
			// SERVICE_TOKEN. Uses the same flags and precedence as every
			// other operator command.
			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return fmt.Errorf("resolve cluster manifest (needed for SERVICE_TOKEN): %w", err)
			}
			defer rc.Cleanup()

			sharedEnv, err := rc.SharedEnv()
			if err != nil {
				return fmt.Errorf("decrypt cluster env (needed for SERVICE_TOKEN): %w", err)
			}
			serviceToken := strings.TrimSpace(sharedEnv["SERVICE_TOKEN"])
			if serviceToken == "" {
				return fmt.Errorf("SERVICE_TOKEN not found in cluster env_files — check the manifest's env_files list and ensure SERVICE_TOKEN is set (same layout `cluster provision` uses)")
			}

			if clusterID == "" {
				clusterID = rc.Cluster
			}

			settings := privateerJoinSettings{
				Token:         token,
				BootstrapAddr: bootstrapAddr,
				NodeName:      nodeName,
				NodeType:      nodeType,
				ClusterID:     clusterID,
				ServiceToken:  serviceToken,
			}
			envContent := buildPrivateerJoinEnv(settings)

			if dryRun {
				redacted := settings
				if redacted.Token != "" {
					redacted.Token = "***"
				}
				redacted.ServiceToken = "***"
				redactedEnv := buildPrivateerJoinEnv(redacted)
				fmt.Fprintf(cmd.OutOrStdout(), "mesh join (dry-run)\n  ssh target:     %s@%s\n  env file:       %s\n  bootstrap:      %s\n  node name:      %s\n  node type:      %s\n  cluster id:     %s\n\n--- env file contents (secrets redacted) ---\n%s", target.user, target.address, envPath, bootstrapAddr, nodeName, nodeType, clusterID, redactedEnv)
				return nil
			}

			if err := writeJoinEnvOverSSH(cmd.Context(), target, sshKey, envPath, envContent); err != nil {
				return err
			}

			ux.Success(cmd.OutOrStdout(), fmt.Sprintf("mesh join: wrote %s on %s@%s", envPath, target.user, target.address))
			fmt.Fprintf(cmd.OutOrStdout(), `
Next: start Privateer on the target
  ssh %s@%s systemctl enable --now frameworks-privateer

Privateer will generate its WireGuard keypair, POST to %s to enroll, and
bring up wg0. Tail the journal to watch the handshake:
  ssh %s@%s journalctl -u frameworks-privateer -f
`, target.user, target.address, bootstrapAddr, target.user, target.address)
			return nil
		},
	}

	cmd.Flags().StringVar(&token, "token", "", "bootstrap token minted by `frameworks admin bootstrap-token create` (required)")
	cmd.Flags().StringVar(&bootstrapAddr, "bootstrap-addr", "", "public Bridge URL the node reaches to enroll (required)")
	cmd.Flags().StringVar(&nodeName, "node-name", "", "node name to register (defaults to the SSH target address)")
	cmd.Flags().StringVar(&nodeType, "node-type", "core", "node_type to register under")
	cmd.Flags().StringVar(&clusterID, "cluster-id", "", "cluster_id hint; defaults to the active context's cluster")
	cmd.Flags().StringVar(&envPath, "env-file", "/etc/privateer/privateer.env", "target path on the remote host")
	cmd.Flags().StringVar(&sshUser, "ssh-user", "", "SSH user (overrides user@ prefix in <ssh-target>)")
	cmd.Flags().StringVar(&sshKey, "ssh-key", "", "path to the SSH private key (default: $SSH_KEY or ~/.ssh/id_ed25519)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "render the env contents with secrets redacted and skip SSH")
	return cmd
}

type sshTarget struct {
	user    string
	address string
}

// parseSSHTarget splits "user@host" / "host". overrideUser wins when set.
// Falls back to "root" when no user is supplied anywhere — matching the
// rest of the provisioning tooling's default.
func parseSSHTarget(raw, overrideUser string) (sshTarget, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return sshTarget{}, fmt.Errorf("<ssh-target> is required (e.g. deploy@host.example.com or just host.example.com)")
	}
	t := sshTarget{address: raw}
	if at := strings.Index(raw, "@"); at > 0 {
		t.user = raw[:at]
		t.address = raw[at+1:]
	}
	if u := strings.TrimSpace(overrideUser); u != "" {
		t.user = u
	}
	if t.user == "" {
		t.user = "root"
	}
	if t.address == "" {
		return sshTarget{}, fmt.Errorf("<ssh-target> is missing host")
	}
	return t, nil
}

type privateerJoinSettings struct {
	Token         string
	BootstrapAddr string
	NodeName      string
	NodeType      string
	ClusterID     string
	ServiceToken  string
}

// buildPrivateerJoinEnv renders the minimum env set Privateer needs to
// enroll via Bridge and authenticate subsequent control-plane RPCs.
// Dotenv-shaped so it's readable by the systemd unit's EnvironmentFile.
func buildPrivateerJoinEnv(s privateerJoinSettings) string {
	var b strings.Builder
	b.WriteString("# Written by `frameworks mesh join`.\n")
	b.WriteString("# Privateer uses MESH_JOIN_TOKEN when MESH_PRIVATE_KEY_FILE is absent,\n")
	b.WriteString("# generates a keypair locally, and calls BRIDGE_BOOTSTRAP_ADDR to enroll.\n")
	if s.ServiceToken != "" {
		fmt.Fprintf(&b, "SERVICE_TOKEN=%s\n", s.ServiceToken)
	}
	if s.Token != "" {
		fmt.Fprintf(&b, "MESH_JOIN_TOKEN=%s\n", s.Token)
	}
	fmt.Fprintf(&b, "BRIDGE_BOOTSTRAP_ADDR=%s\n", s.BootstrapAddr)
	fmt.Fprintf(&b, "MESH_NODE_NAME=%s\n", s.NodeName)
	fmt.Fprintf(&b, "MESH_NODE_TYPE=%s\n", s.NodeType)
	if s.ClusterID != "" {
		fmt.Fprintf(&b, "CLUSTER_ID=%s\n", s.ClusterID)
	}
	b.WriteString("MESH_PRIVATE_KEY_FILE=/etc/privateer/wg.key\n")
	b.WriteString("PRIVATEER_STATIC_PEERS_FILE=/etc/privateer/static-peers.json\n")
	return b.String()
}

// writeLocalTempEnv drops envContent into an OS tempfile and returns its
// path. Caller removes it via removeQuiet.
func writeLocalTempEnv(envContent string) (string, error) {
	f, err := os.CreateTemp("", "privateer-env-*.tmp")
	if err != nil {
		return "", fmt.Errorf("staging tempfile: %w", err)
	}
	path := f.Name()
	if _, err := f.WriteString(envContent); err != nil {
		f.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("write staging: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("close staging: %w", err)
	}
	return path, nil
}

func removeQuiet(path string) {
	if path == "" {
		return
	}
	_ = os.Remove(path) //nolint:errcheck
}

// writeJoinEnvOverSSH uploads envContent to remotePath on the target via
// scp at mode 0600. Ownership is left to the remote (typically root via
// sudo-ssh); the Privateer systemd unit reads the env file as root.
// mkdir of the parent runs first because scp doesn't create intermediate
// directories.
func writeJoinEnvOverSSH(ctx context.Context, target sshTarget, sshKey, remotePath, envContent string) error {
	pool := fwssh.NewPool(30*time.Second, sshKey)
	defer pool.Close() //nolint:errcheck

	conn := &fwssh.ConnectionConfig{
		Address: target.address,
		User:    target.user,
	}

	// scp needs the parent directory to exist. Reusing `install -d` keeps
	// ownership/mode consistent with the existing Privateer role.
	parent := filepath.Dir(remotePath)
	if _, err := pool.Run(ctx, conn, fmt.Sprintf("install -d -m 0755 %s", fwssh.ShellQuote(parent))); err != nil {
		return fmt.Errorf("prepare remote directory %s: %w", parent, err)
	}

	// Stage locally, upload, then clean up the staging file.
	localTmp, err := writeLocalTempEnv(envContent)
	if err != nil {
		return err
	}
	defer removeQuiet(localTmp)

	if err := pool.Upload(ctx, conn, fwssh.UploadOptions{
		LocalPath:  localTmp,
		RemotePath: remotePath,
		Mode:       0o600,
	}); err != nil {
		return fmt.Errorf("upload %s: %w", remotePath, err)
	}
	return nil
}
