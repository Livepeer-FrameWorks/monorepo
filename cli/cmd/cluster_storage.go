package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"frameworks/cli/internal/controlplane"
	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"

	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"

	"github.com/spf13/cobra"
)

func newClusterStorageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "storage",
		Short: "Inspect and adopt cluster artifact-storage backends",
		Long: `Manage the immutable S3 storage descriptor a media cluster is bound to.

Each media cell is bound to exactly one S3 backend (bucket/endpoint/region/prefix).
Quartermaster persists that descriptor on the cluster row, Chandler serves from it,
and Foghorn establishes an immutable cell identity against it on first boot. A cell
that predates this feature (its backend lives only in STORAGE_S3_* env) is brought
forward with 'storage adopt', which reads what the live Foghorn replicas are actually
running and reconciles it into Quartermaster.`,
	}
	cmd.AddCommand(newClusterStorageAdoptCmd())
	cmd.AddCommand(newClusterStorageDescriptorCmd())
	return cmd
}

// newClusterStorageDescriptorCmd prints a cluster's S3 descriptor as JSON. It reads only three non-secret S3 fields, so
// it does a STRUCTURAL read — strict-parse the cluster.yaml passed via --manifest and validate just that descriptor —
// and deliberately does NOT go through resolveClusterManifest / LoadWithHosts, which would decrypt the SOPS host
// inventory. The destructive thumbnail cutover consumes this, so it must not depend on host-secret resolution.
func newClusterStorageDescriptorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "descriptor <cluster>",
		Short:  "Print a cluster's S3 descriptor as JSON (structural manifest read; no host inventory)",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := strings.TrimSpace(stringFlag(cmd, "manifest").Value)
			if path == "" {
				return fmt.Errorf("cluster storage descriptor requires --manifest <cluster.yaml>")
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("read manifest %s: %w", path, err)
			}
			m, err := inventory.ParseManifest(data) // strict (KnownFields), no host inventory, no SOPS
			if err != nil {
				return err
			}
			return emitClusterDescriptor(cmd.OutOrStdout(), m, args[0])
		},
	}
	return cmd
}

// emitClusterDescriptor writes {"bucket","prefix","endpoint"} for a cluster, or an error when the cluster is absent,
// declares no S3 backend, has an unset prefix, or any field contains a control character (a simple S3 identifier never
// does; this feeds a destructive path, so a weird value fails closed rather than being addressed). Split from the
// command so validation + serialization are unit-testable without a live manifest.
func emitClusterDescriptor(w io.Writer, m *inventory.Manifest, clusterID string) error {
	cc, ok := m.Clusters[clusterID]
	if !ok {
		return fmt.Errorf("cluster %q not found in manifest", clusterID)
	}
	if strings.TrimSpace(cc.S3Bucket) == "" {
		return fmt.Errorf("cluster %q declares no s3_bucket (no storage backend)", clusterID)
	}
	if strings.TrimSpace(cc.S3Endpoint) == "" {
		return fmt.Errorf("cluster %q declares no s3_endpoint (descriptor incomplete)", clusterID)
	}
	if cc.S3Prefix == nil {
		return fmt.Errorf("cluster %q has no s3_prefix (descriptor incomplete)", clusterID)
	}
	prefix := *cc.S3Prefix
	for _, f := range []struct{ name, val string }{{"s3_bucket", cc.S3Bucket}, {"s3_prefix", prefix}, {"s3_endpoint", cc.S3Endpoint}} {
		if hasControlChar(f.val) {
			return fmt.Errorf("cluster %q %s contains a control character; refusing", clusterID, f.name)
		}
	}
	b, err := json.Marshal(struct {
		Bucket   string `json:"bucket"`
		Prefix   string `json:"prefix"`
		Endpoint string `json:"endpoint"`
	}{Bucket: cc.S3Bucket, Prefix: prefix, Endpoint: cc.S3Endpoint})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(b))
	return err
}

// hasControlChar reports whether s contains any ASCII control character (below 0x20 or DEL).
func hasControlChar(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

func newClusterStorageAdoptCmd() *cobra.Command {
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "adopt <cluster>",
		Short: "Adopt a legacy cell's deployed S3 backend into Quartermaster",
		Long: `Adopt the S3 backend a cell's Foghorn replicas are actually running with.

Reads the DEPLOYED effective S3 tuple (STORAGE_S3_BUCKET / ENDPOINT / REGION / PREFIX)
from EVERY live Foghorn replica in the cluster and requires them to agree (a split cell
is refused). It then reconciles that descriptor directly into the Quartermaster cluster
row via the AdoptClusterStorageDescriptor RPC and reads the row back to verify.

Quartermaster enforces immutability: a cluster that already declares a DIFFERENT
descriptor is refused (a repoint of an immutable backend must be resolved by hand); an
unset prefix on an already-established descriptor is filled exactly once (the migration
transition); an already-matching descriptor is idempotent.

Credentials are never touched: only bucket/endpoint/region/prefix are reconciled; access
keys stay in the per-cluster env files.`,
		Example: `  frameworks cluster storage adopt media-us --dry-run
  frameworks cluster storage adopt media-us`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return err
			}
			defer rc.Cleanup()
			if err := requirePlatformIfImplicitManifest(rc, cmd.OutOrStdout()); err != nil {
				return err
			}
			return runStorageAdopt(cmd, rc, args[0], dryRun)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Read the deployed descriptor and report the decision without writing Quartermaster")
	return cmd
}

// deployedDescriptor is the S3 descriptor a single Foghorn replica is actually running with, read from its live
// environment (not the manifest, which is only the DESIRED state and may not match what is deployed).
type deployedDescriptor struct {
	host, bucket, endpoint, region, prefix string
}

// readDeployedFoghornDescriptorFn is the live-replica descriptor read (test seam). nil in production, where it SSHes
// the host and inspects the running Foghorn container's environment.
var readDeployedFoghornDescriptorFn func(ctx context.Context, pool *ssh.Pool, host inventory.Host, deployName string) (deployedDescriptor, error)

// storageAdoptQM is the narrow Quartermaster surface the adopt flow calls. *qmclient.GRPCClient satisfies it; tests
// inject a fake.
type storageAdoptQM interface {
	AdoptClusterStorageDescriptor(ctx context.Context, clusterID, bucket, endpoint, region, prefix string) (*quartermasterpb.ClusterResponse, error)
}

func runStorageAdopt(cmd *cobra.Command, rc *resolvedCluster, clusterID string, dryRun bool) error {
	manifest := rc.Manifest
	if _, ok := manifest.Clusters[clusterID]; !ok {
		return fmt.Errorf("cluster %q not found in manifest", clusterID)
	}

	hosts := foghornHostsInCluster(manifest, clusterID)
	if len(hosts) == 0 {
		return fmt.Errorf("cluster %q has no enabled Foghorn replica to read a deployed descriptor from — adoption reads live state, not the manifest", clusterID)
	}

	sshKey := stringFlag(cmd, "ssh-key").Value
	sshPool := ssh.NewPool(30*time.Second, sshKey)
	defer sshPool.Close()

	ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
	defer cancel()

	consensus, err := deployedDescriptorConsensus(ctx, sshPool, hosts)
	if err != nil {
		return err
	}

	ux.Heading(cmd.OutOrStdout(), fmt.Sprintf("Adopting storage backend for cluster %q", clusterID))
	fmt.Fprintf(cmd.OutOrStdout(), "  Deployed (consensus of %d replica(s)): bucket=%q endpoint=%q region=%q prefix=%q\n",
		len(hosts), consensus.bucket, consensus.endpoint, effectiveS3Region(consensus.region), consensus.prefix)

	if dryRun {
		fmt.Fprintln(cmd.OutOrStdout(), "\n[DRY-RUN] Would reconcile this descriptor into Quartermaster (idempotent; a repoint would be refused). Re-run without --dry-run to apply.")
		return nil
	}

	qc, jwt, cleanup, err := clusterStorageQMClient(cmd.Context())
	if err != nil {
		return err
	}
	defer cleanup()

	// AdoptClusterStorageDescriptor requires platform-operator authorization — inject the operator JWT so the client
	// sends it (a service token is refused by Quartermaster). Without an operator login the RPC fails PermissionDenied.
	if strings.TrimSpace(jwt) == "" {
		return fmt.Errorf("cluster storage adopt requires a platform-operator login: no operator JWT is available (run the operator auth flow first)")
	}
	callCtx := context.WithValue(cmd.Context(), ctxkeys.KeyJWTToken, jwt)

	if err := adoptDeployedDescriptor(callCtx, cmd, qc, clusterID, consensus); err != nil {
		return err
	}
	syncClusterDescriptorToManifest(rc, clusterID, consensus)
	fmt.Fprintf(cmd.OutOrStdout(), "  Quartermaster is now authoritative for cluster %q. Record the descriptor in your SOURCE gitops manifest (s3_bucket/s3_endpoint/s3_region/s3_prefix on the cluster) so the pre-deploy gate agrees on future runs.\n", clusterID)
	return nil
}

// syncClusterDescriptorToManifest records the descriptor on the IN-MEMORY manifest cluster so the SAME run's
// downstream (manifest-based) Foghorn pre-deploy gate agrees. It NEVER writes the manifest to disk: the RESOLVED
// manifest carries host inventory decrypted+merged from the encrypted hosts file, so serializing it would materialize
// external IPs / SSH users / key-file metadata into plaintext cluster.yaml (and clobber comments/order). Quartermaster
// is the authority (the adopt RPC persisted the descriptor there); the operator records the descriptor in their SOURCE
// gitops manifest, not here.
func syncClusterDescriptorToManifest(rc *resolvedCluster, clusterID string, d deployedDescriptor) {
	cc, ok := rc.Manifest.Clusters[clusterID]
	if !ok {
		return
	}
	prefix := d.prefix
	cc.S3Bucket, cc.S3Endpoint, cc.S3Region, cc.S3Prefix = d.bucket, d.endpoint, d.region, &prefix
	rc.Manifest.Clusters[clusterID] = cc
}

// errNoDeployedS3Backend: EVERY Foghorn replica is running without an S3 backend (empty STORAGE_S3_BUCKET). Adoption
// is NOT APPLICABLE to such a cell. errReplicaDescriptorDisagreement: replicas run DIFFERENT backends — a split that
// adoption must never paper over, INCLUDING the mixed case where some replicas have S3 and some do not (Blocked).
// errFoghornNotDeployed: no Foghorn is deployed on the host (a positive absence signal, distinct from an unreachable
// deployed Foghorn). All are sentinels so callers can classify the outcome.
var (
	errNoDeployedS3Backend           = errors.New("no deployed S3 backend on any replica")
	errReplicaDescriptorDisagreement = errors.New("foghorn replicas disagree on the deployed descriptor")
	errFoghornNotDeployed            = errors.New("foghorn is not deployed")
)

// deployedDescriptorConsensus reads EVERY replica and aggregates the full picture before classifying — never deciding
// from whichever host happened to be read first. Outcomes, in precedence order:
//   - any replica UNREACHABLE (deployed but a transport/read error) → generic error → the caller Blocks (agreement
//     could not be confirmed);
//   - some replicas deployed with S3 and some without → disagreement (a split cell);
//   - some deployed-with-S3, rest merely NOT deployed → the deployed subset's consensus (the not-yet-deployed replicas
//     will adopt from Quartermaster when they come up);
//   - all deployed replicas run without S3 → errNoDeployedS3Backend (not applicable);
//   - NO replica deployed at all → errFoghornNotDeployed (greenfield).
func deployedDescriptorConsensus(ctx context.Context, pool *ssh.Pool, hosts []inventory.Host) (deployedDescriptor, error) {
	var withS3, withoutS3 []deployedDescriptor
	var notDeployed, unreachable int
	var firstUnreachErr error
	for _, h := range hosts {
		d, err := readDeployedFoghornDescriptor(ctx, pool, h, "foghorn")
		switch {
		case err == nil && strings.TrimSpace(d.bucket) != "":
			withS3 = append(withS3, d)
		case err == nil:
			withoutS3 = append(withoutS3, d)
		case errors.Is(err, errFoghornNotDeployed):
			notDeployed++
		default:
			unreachable++
			if firstUnreachErr == nil {
				firstUnreachErr = fmt.Errorf("foghorn replica %s: %w", h.ExternalIP, err)
			}
		}
	}
	// An unreachable DEPLOYED replica means we cannot confirm the whole pool agrees — fail closed (Blocked).
	if unreachable > 0 {
		return deployedDescriptor{}, fmt.Errorf("%d foghorn replica(s) unreachable; cannot confirm backend agreement: %w", unreachable, firstUnreachErr)
	}
	deployed := len(withS3) + len(withoutS3)
	if deployed == 0 {
		return deployedDescriptor{}, fmt.Errorf("no foghorn replica is deployed on any of %d host(s): %w", len(hosts), errFoghornNotDeployed)
	}
	// Mixed among the DEPLOYED replicas: some run S3, some do not — a split cell that must NOT be adopted.
	if len(withS3) > 0 && len(withoutS3) > 0 {
		return deployedDescriptor{}, fmt.Errorf("%d deployed replica(s) run S3, %d do not: %w — reconcile the replicas' STORAGE_S3_* first", len(withS3), len(withoutS3), errReplicaDescriptorDisagreement)
	}
	if len(withS3) == 0 {
		return deployedDescriptor{}, fmt.Errorf("all %d deployed replica(s) report no STORAGE_S3_BUCKET: %w", len(withoutS3), errNoDeployedS3Backend)
	}
	// All deployed-with-S3 replicas must agree on the full descriptor.
	consensus := withS3[0]
	for _, d := range withS3[1:] {
		if d.bucket != consensus.bucket || d.endpoint != consensus.endpoint ||
			effectiveS3Region(d.region) != effectiveS3Region(consensus.region) || d.prefix != consensus.prefix {
			return deployedDescriptor{}, fmt.Errorf("%s=(bucket=%q,endpoint=%q,region=%q,prefix=%q) vs %s=(bucket=%q,endpoint=%q,region=%q,prefix=%q): %w — reconcile the replicas' STORAGE_S3_* first",
				consensus.host, consensus.bucket, consensus.endpoint, consensus.region, consensus.prefix,
				d.host, d.bucket, d.endpoint, d.region, d.prefix, errReplicaDescriptorDisagreement)
		}
	}
	return consensus, nil
}

// adoptDeployedDescriptor reconciles the deployed descriptor into Quartermaster and verifies the read-back. The RPC is
// idempotent and enforces immutability, so a re-run or a matching descriptor is a no-op and a repoint is refused.
func adoptDeployedDescriptor(ctx context.Context, cmd *cobra.Command, qc storageAdoptQM, clusterID string, d deployedDescriptor) error {
	resp, err := qc.AdoptClusterStorageDescriptor(ctx, clusterID, d.bucket, d.endpoint, d.region, d.prefix)
	if err != nil {
		return fmt.Errorf("adopt into Quartermaster: %w", err)
	}
	got := resp.GetCluster()
	if got == nil {
		return fmt.Errorf("adopt into Quartermaster: empty cluster in read-back response")
	}
	// Read-back verification: Quartermaster must now report exactly what we adopted (region compared on its effective
	// value, matching how Foghorn/Chandler default an omitted region).
	if got.GetS3Bucket() != d.bucket || got.GetS3Endpoint() != d.endpoint ||
		effectiveS3Region(got.GetS3Region()) != effectiveS3Region(d.region) || got.GetS3Prefix() != d.prefix {
		return fmt.Errorf("read-back mismatch after adopt: Quartermaster reports bucket=%q endpoint=%q region=%q prefix=%q but the deployed descriptor is bucket=%q endpoint=%q region=%q prefix=%q",
			got.GetS3Bucket(), got.GetS3Endpoint(), effectiveS3Region(got.GetS3Region()), got.GetS3Prefix(),
			d.bucket, d.endpoint, effectiveS3Region(d.region), d.prefix)
	}
	ux.Success(cmd.OutOrStdout(), fmt.Sprintf("cluster %q storage descriptor adopted into Quartermaster and verified", clusterID))
	return nil
}

// foghornHostsInCluster returns every host running an enabled Foghorn (by deploy name) in the given cluster.
func foghornHostsInCluster(manifest *inventory.Manifest, clusterID string) []inventory.Host {
	var hosts []inventory.Host
	seen := map[string]bool{}
	for name, svc := range manifest.Services {
		if !svc.Enabled {
			continue
		}
		dn, err := resolveDeployName(name, svc)
		if err != nil || dn != "foghorn" {
			continue
		}
		if serviceUpgradeCluster(manifest, svc) != clusterID {
			continue
		}
		for _, hostName := range serviceHosts(svc) {
			if seen[hostName] {
				continue
			}
			if h, ok := manifest.GetHost(hostName); ok {
				seen[hostName] = true
				hosts = append(hosts, h)
			}
		}
	}
	return hosts
}

// readDeployedFoghornDescriptor reads one replica's deployed S3 descriptor from its live environment over SSH.
// Reading the live environment (not the manifest) is what makes adoption reflect what is actually deployed rather than
// an edited-but-unapplied manifest. Both supported service modes are handled: a docker Foghorn's env is read from the
// running container, a native Foghorn's from its systemd EnvironmentFile.
func readDeployedFoghornDescriptor(ctx context.Context, pool *ssh.Pool, host inventory.Host, deployName string) (deployedDescriptor, error) {
	if readDeployedFoghornDescriptorFn != nil {
		return readDeployedFoghornDescriptorFn(ctx, pool, host, deployName)
	}
	state, err := detect.NewDetector(pool, host).Detect(ctx, deployName)
	if err != nil {
		return deployedDescriptor{}, fmt.Errorf("detect %s: %w", deployName, err)
	}
	if !state.Exists {
		return deployedDescriptor{}, fmt.Errorf("%s is not deployed on %s: %w", deployName, host.ExternalIP, errFoghornNotDeployed)
	}

	var envText string
	switch state.Mode {
	case "docker":
		envText, err = runSSHDescriptorRead(ctx, pool, host,
			fmt.Sprintf("docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' %s", "frameworks-"+deployName))
	case "native":
		envText, err = runSSHDescriptorRead(ctx, pool, host,
			fmt.Sprintf("cat /etc/frameworks/%s.env", deployName))
	default:
		return deployedDescriptor{}, fmt.Errorf("cannot read deployed descriptor from %s: unsupported foghorn service mode %q (only docker and native are supported)", host.ExternalIP, state.Mode)
	}
	if err != nil {
		return deployedDescriptor{}, err
	}
	return parseDeployedS3Env(host.ExternalIP, envText), nil
}

// runSSHDescriptorRead runs a read-only command on host and returns stdout, erroring on transport failure or non-zero
// exit so the caller can distinguish "read it" from "could not read it".
func runSSHDescriptorRead(ctx context.Context, pool *ssh.Pool, host inventory.Host, command string) (string, error) {
	cfg := &ssh.ConnectionConfig{
		Address:  host.ExternalIP,
		Port:     22,
		User:     host.User,
		HostName: host.Name,
		Timeout:  15 * time.Second,
	}
	res, err := pool.Run(ctx, cfg, command)
	if err != nil {
		return "", fmt.Errorf("ssh: %w", err)
	}
	if res == nil || res.ExitCode != 0 {
		stderr := ""
		if res != nil {
			stderr = strings.TrimSpace(res.Stderr)
		}
		return "", fmt.Errorf("command %q failed on %s: %s", command, host.ExternalIP, stderr)
	}
	return res.Stdout, nil
}

// parseDeployedS3Env extracts the STORAGE_S3_* descriptor from KEY=VALUE lines (docker inspect env or a systemd
// EnvironmentFile). Values may be surrounded by quotes (env-file style); they are stripped.
func parseDeployedS3Env(host, envText string) deployedDescriptor {
	d := deployedDescriptor{host: host}
	for _, line := range strings.Split(envText, "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		switch strings.TrimSpace(k) {
		case "STORAGE_S3_BUCKET":
			d.bucket = v
		case "STORAGE_S3_ENDPOINT":
			d.endpoint = v
		case "STORAGE_S3_REGION":
			d.region = v
		case "STORAGE_S3_PREFIX":
			d.prefix = v
		}
	}
	return d
}

// clusterStorageQMClient builds an authenticated Quartermaster gRPC client from the active cluster context, mirroring
// the other cluster-lifecycle commands. It also returns the operator JWT so the caller can authorize the
// operator-only AdoptClusterStorageDescriptor RPC (a service token alone is refused).
func clusterStorageQMClient(ctx context.Context) (*qmclient.GRPCClient, string, func(), error) {
	ctxCfg, err := activeClusterLifecycleContextWithAuth(ctx)
	if err != nil {
		return nil, "", nil, err
	}
	ep, err := controlplane.ResolveGRPC(ctx, ctxCfg, "quartermaster")
	if err != nil {
		return nil, "", nil, err
	}
	qc, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{
		GRPCAddr:      ep.Address,
		Timeout:       15 * time.Second,
		Logger:        logging.NewLogger(),
		ServiceToken:  ctxCfg.Auth.ServiceToken,
		AllowInsecure: ep.AllowInsecure,
		ServerName:    ep.ServerName,
	})
	if err != nil {
		ep.Cleanup()
		return nil, "", nil, fmt.Errorf("failed to connect to Quartermaster gRPC: %w", err)
	}
	return qc, ctxCfg.Auth.JWT, ep.Cleanup, nil
}
