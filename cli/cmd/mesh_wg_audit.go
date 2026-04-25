package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"text/tabwriter"
	"time"

	"frameworks/cli/internal/mesh"
	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/inventory"
	pb "frameworks/pkg/proto"

	"github.com/spf13/cobra"
)

// defaultLivenessWindow is 3× Privateer's default sync interval (30s). A
// node that has missed three SyncMesh round-trips in a row has not been
// recently accepted by Quartermaster.
const defaultLivenessWindow = 90 * time.Second

// Node-origin vocabulary, mirrored from pkg/database/sql/schema/quartermaster.sql
// and api_tenants/internal/grpc/server.go. A row's enrollment_origin governs
// whether GitOps is authoritative for its WireGuard identity.
const (
	enrollmentOriginGitopsSeed      = "gitops_seed"
	enrollmentOriginRuntimeEnrolled = "runtime_enrolled"
	enrollmentOriginAdoptedLocal    = "adopted_local"
)

func newMeshWgAuditCmd() *cobra.Command {
	var (
		livenessWindow time.Duration
		format         string
		clusterFilter  string
		hostFilter     string
	)
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Compare GitOps WireGuard identity against Quartermaster's recorded peer state",
		Long: `Reads the GitOps cluster manifest and cross-references it with the
infrastructure_nodes rows Quartermaster has stored for the same cluster.

Mismatches on gitops_seed or adopted_local rows are reported as errors
(those origins mean GitOps is authoritative). runtime_enrolled rows are
printed as informational — they are expected to not appear in GitOps
until promoted via 'mesh reconcile --write-gitops'.

The LIVE column reflects last_heartbeat freshness. Quartermaster's
SyncMesh validates the agent's reported public key against the stored
value before accepting the heartbeat, so a fresh heartbeat is a strong
signal that the runtime key matches Quartermaster. A stale or missing
heartbeat means the agent has not been recently accepted by
Quartermaster — common causes include the agent being down, the agent
unable to reach Quartermaster (network or auth), and the agent running
with a key Quartermaster's stored value rejects.

--format=json emits one JSON document per audit run; the exit code is the
same as table format (non-zero on authoritative divergence). --cluster
and --host filter the printed rows after the audit runs; filtering does
not affect the divergence count or exit code.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return err
			}
			defer rc.Cleanup()

			hostNames := meshCheckHostNames(rc.Manifest)
			// Validate GitOps identity first; if that fails, audit is meaningless.
			if validateErr := mesh.ValidateIdentity(rc.Manifest, hostNames); validateErr != nil {
				return fmt.Errorf("%w\n\nRun: frameworks mesh wg generate --manifest %s", validateErr, rc.ManifestPath)
			}

			client, err := getMeshQuartermasterGRPCClient()
			if err != nil {
				return fmt.Errorf("connect to Quartermaster: %w", err)
			}
			defer client.Close()

			// Build the set of clusters this manifest owns so we can filter
			// QM rows. manifest.Profile ("production"/"development") is not a
			// cluster ID — HostCluster/AllClusterIDs resolve the real ones.
			manifestClusters := map[string]bool{}
			for _, id := range rc.Manifest.AllClusterIDs() {
				manifestClusters[id] = true
			}

			// List once without a cluster filter; we match per host below by
			// (node_name, cluster_id) to handle multi-cluster manifests.
			resp, err := client.ListNodes(context.Background(), "", "", "", nil)
			if err != nil {
				return fmt.Errorf("list infrastructure_nodes: %w", err)
			}

			findings := auditMeshIdentity(rc.Manifest, hostNames, resp.GetNodes(), manifestClusters, time.Now(), livenessWindow)
			displayed := filterAuditFindings(findings, clusterFilter, hostFilter)

			switch format {
			case "json":
				if err := printAuditJSON(cmd.OutOrStdout(), displayed, rc.Manifest.AllClusterIDs()); err != nil {
					return err
				}
			case "", "table":
				printAuditReport(cmd.OutOrStdout(), displayed, rc.Manifest.AllClusterIDs())
			default:
				return fmt.Errorf("unknown --format %q (valid: table, json)", format)
			}

			// Exit code reflects the full audit, not the filtered view —
			// otherwise --host=core-1 could hide a divergence elsewhere.
			if findings.hasErrors() {
				return fmt.Errorf("mesh wg audit: %d authoritative host(s) diverge from Quartermaster", findings.errorCount())
			}
			if format != "json" {
				ux.Success(cmd.OutOrStdout(), fmt.Sprintf("mesh wg audit: %d host(s) match Quartermaster", len(findings.rows)))
			}
			return nil
		},
	}
	cmd.Flags().DurationVar(&livenessWindow, "liveness-window", defaultLivenessWindow, "Heartbeat freshness window for the LIVE column")
	cmd.Flags().StringVar(&format, "format", "table", "Output format: table or json")
	cmd.Flags().StringVar(&clusterFilter, "cluster-filter", "", "Show only rows in this cluster ID (does not affect exit code)")
	cmd.Flags().StringVar(&hostFilter, "host", "", "Show only rows for this host name (does not affect exit code)")
	return cmd
}

// filterAuditFindings returns a copy of f with rows narrowed to those
// matching clusterID (when non-empty) and host (when non-empty). Filtering
// is purely cosmetic — callers should still inspect the unfiltered
// findings for severity-driven exit codes.
func filterAuditFindings(f auditFindings, clusterID, host string) auditFindings {
	if clusterID == "" && host == "" {
		return f
	}
	filtered := auditFindings{}
	for _, r := range f.rows {
		if clusterID != "" && r.clusterID != clusterID {
			continue
		}
		if host != "" && r.host != host {
			continue
		}
		filtered.rows = append(filtered.rows, r)
	}
	return filtered
}

type auditSeverity int

const (
	auditOK auditSeverity = iota
	auditInfo
	auditWarn
	auditError
)

type auditLiveness int

const (
	livenessUnknown auditLiveness = iota // no QM row, or QM has no last_heartbeat
	livenessStale                        // last_heartbeat older than the window
	livenessFresh                        // last_heartbeat within the window
)

type auditRow struct {
	host      string
	clusterID string
	origin    string
	severity  auditSeverity
	message   string
	gitopsIP  string
	qmIP      string
	live      auditLiveness
	// revision is the mesh_revision the Privateer agent reported on its
	// most recent SyncMesh. Empty if the agent has never reported a
	// revision (older client, fresh row, or stuck before first managed
	// apply). Surfaces stale agents to operators.
	revision string
}

type auditFindings struct {
	rows []auditRow
}

func (f auditFindings) hasErrors() bool {
	return f.errorCount() > 0
}

func (f auditFindings) errorCount() int {
	n := 0
	for _, r := range f.rows {
		if r.severity == auditError {
			n++
		}
	}
	return n
}

// qmKey is a composite key for joining QM rows against manifest hosts.
// node_name alone is not unique across clusters — two clusters could legitimately
// have a host called "core-1".
type qmKey struct {
	nodeName  string
	clusterID string
}

// classifyLiveness reports whether a QM row's last_heartbeat is fresh
// relative to (now - window). nil last_heartbeat is treated as unknown,
// not stale, since "never seen" and "haven't seen recently" are
// operationally distinct.
func classifyLiveness(node *pb.InfrastructureNode, now time.Time, window time.Duration) auditLiveness {
	if node == nil || node.LastHeartbeat == nil {
		return livenessUnknown
	}
	hb := node.LastHeartbeat.AsTime()
	if now.Sub(hb) > window {
		return livenessStale
	}
	return livenessFresh
}

// auditMeshIdentity compares the manifest's per-host WireGuard identity against
// Quartermaster's recorded infrastructure_nodes rows. manifestClusters is the
// set of cluster IDs this manifest owns (from Manifest.AllClusterIDs); QM rows
// in other clusters are ignored entirely. now and livenessWindow drive the
// per-row LIVE column without touching identity-comparison severity — a
// stale heartbeat does not turn an otherwise clean row into an error.
func auditMeshIdentity(manifest *inventory.Manifest, hostNames []string, qmNodes []*pb.InfrastructureNode, manifestClusters map[string]bool, now time.Time, livenessWindow time.Duration) auditFindings {
	qmByKey := make(map[qmKey]*pb.InfrastructureNode, len(qmNodes))
	for _, n := range qmNodes {
		qmByKey[qmKey{nodeName: n.GetNodeName(), clusterID: n.GetClusterId()}] = n
	}

	var rows []auditRow
	seenQM := make(map[qmKey]bool, len(qmByKey))
	// Clusters for which the manifest declares at least one host. Unmatched QM
	// rows only count as drift in these clusters — a cluster that appears in
	// the `clusters:` block but has no declared hosts yet isn't authoritative
	// over anything.
	clustersWithHosts := map[string]bool{}

	for _, name := range hostNames {
		host := manifest.Hosts[name]
		clusterID := manifest.HostCluster(name)
		// If the manifest can't place the host in a cluster, fall back to the
		// single-cluster auto-ID used by the provisioner (Type-Profile).
		if clusterID == "" && len(manifest.Clusters) == 0 {
			for id := range manifestClusters {
				clusterID = id
				break
			}
		}
		clustersWithHosts[clusterID] = true
		key := qmKey{nodeName: name, clusterID: clusterID}
		qm := qmByKey[key]
		seenQM[key] = true

		if qm == nil {
			rows = append(rows, auditRow{
				host:      name,
				clusterID: clusterID,
				origin:    "-",
				severity:  auditWarn,
				message:   "declared in GitOps, no row in Quartermaster (not yet provisioned?)",
				gitopsIP:  host.WireguardIP,
			})
			continue
		}

		origin := qm.GetEnrollmentOrigin()
		qmIP := ""
		if qm.WireguardIp != nil {
			qmIP = *qm.WireguardIp
		}
		qmPubKey := ""
		if qm.WireguardPublicKey != nil {
			qmPubKey = *qm.WireguardPublicKey
		}
		qmPort := int32(0)
		if qm.WireguardPort != nil {
			qmPort = *qm.WireguardPort
		}

		var mismatches []string
		if host.WireguardIP != qmIP {
			mismatches = append(mismatches, fmt.Sprintf("wireguard_ip GitOps=%q QM=%q", host.WireguardIP, qmIP))
		}
		if host.WireguardPublicKey != qmPubKey {
			mismatches = append(mismatches, fmt.Sprintf("wireguard_public_key GitOps=%q QM=%q", host.WireguardPublicKey, qmPubKey))
		}
		if int32(host.WireguardPort) != qmPort {
			mismatches = append(mismatches, fmt.Sprintf("wireguard_port GitOps=%d QM=%d", host.WireguardPort, qmPort))
		}

		revision := ""
		if qm.AppliedMeshRevision != nil {
			revision = *qm.AppliedMeshRevision
		}
		row := auditRow{
			host:      name,
			clusterID: clusterID,
			origin:    origin,
			gitopsIP:  host.WireguardIP,
			qmIP:      qmIP,
			live:      classifyLiveness(qm, now, livenessWindow),
			revision:  revision,
		}
		if len(mismatches) == 0 {
			row.severity = auditOK
			row.message = "match"
		} else {
			// gitops_seed and adopted_local both mean GitOps is authoritative;
			// any divergence from the manifest is an error. A runtime_enrolled
			// row that has a GitOps host is an operator/plumbing mistake —
			// surface as warn rather than error.
			switch origin {
			case enrollmentOriginGitopsSeed, enrollmentOriginAdoptedLocal:
				row.severity = auditError
			default:
				row.severity = auditWarn
			}
			row.message = joinMismatches(mismatches)
		}
		rows = append(rows, row)
	}

	// QM rows that belong to a cluster we declared hosts in, but aren't matched
	// by any manifest host. QM rows in unrelated clusters — or in clusters
	// that exist in the manifest but have no declared hosts yet — are ignored.
	for _, n := range qmNodes {
		cid := n.GetClusterId()
		if !clustersWithHosts[cid] {
			continue
		}
		key := qmKey{nodeName: n.GetNodeName(), clusterID: cid}
		if seenQM[key] {
			continue
		}
		origin := n.GetEnrollmentOrigin()
		revision := ""
		if n.AppliedMeshRevision != nil {
			revision = *n.AppliedMeshRevision
		}
		row := auditRow{
			host:      n.GetNodeName(),
			clusterID: cid,
			origin:    origin,
			live:      classifyLiveness(n, now, livenessWindow),
			revision:  revision,
		}
		if n.WireguardIp != nil {
			row.qmIP = *n.WireguardIp
		}
		switch origin {
		case enrollmentOriginRuntimeEnrolled:
			row.severity = auditInfo
			row.message = "runtime-enrolled, not yet promoted into GitOps"
		case enrollmentOriginGitopsSeed, enrollmentOriginAdoptedLocal:
			row.severity = auditError
			row.message = "claims GitOps authority but no matching host in cluster.yaml"
		default:
			row.severity = auditWarn
			row.message = fmt.Sprintf("unexpected enrollment_origin=%q with no GitOps host", origin)
		}
		rows = append(rows, row)
	}

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].clusterID != rows[j].clusterID {
			return rows[i].clusterID < rows[j].clusterID
		}
		return rows[i].host < rows[j].host
	})
	return auditFindings{rows: rows}
}

func joinMismatches(m []string) string {
	out := ""
	for i, s := range m {
		if i > 0 {
			out += "; "
		}
		out += s
	}
	return out
}

func printAuditReport(w io.Writer, f auditFindings, clusterIDs []string) {
	label := "-"
	if len(clusterIDs) == 1 {
		label = clusterIDs[0]
	} else if len(clusterIDs) > 1 {
		label = fmt.Sprintf("%v", clusterIDs)
	}
	fmt.Fprintf(w, "mesh wg audit (clusters=%s)\n", label)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "CLUSTER\tHOST\tORIGIN\tSEVERITY\tLIVE\tREVISION\tDETAIL")
	for _, r := range f.rows {
		cluster := r.clusterID
		if cluster == "" {
			cluster = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", cluster, r.host, r.origin, severityLabel(r.severity), livenessLabel(r.live), revisionLabel(r.revision), r.message)
	}
	tw.Flush()
}

// revisionLabel renders the agent-reported mesh revision in the audit
// table. Long hex hashes are truncated to the first 12 chars (same
// convention git uses) so the column stays readable.
func revisionLabel(rev string) string {
	if rev == "" {
		return "-"
	}
	if len(rev) > 12 {
		return rev[:12]
	}
	return rev
}

func severityLabel(s auditSeverity) string {
	switch s {
	case auditOK:
		return "ok"
	case auditInfo:
		return "info"
	case auditWarn:
		return "warn"
	case auditError:
		return "ERROR"
	}
	return "?"
}

func livenessLabel(l auditLiveness) string {
	switch l {
	case livenessFresh:
		return "live"
	case livenessStale:
		return "stale"
	default:
		return "-"
	}
}

// auditJSONRow is the export shape for --format=json. It uses string
// labels rather than the internal enum ints so consumers don't need to
// track the iota ordering across CLI versions.
type auditJSONRow struct {
	Cluster  string `json:"cluster"`
	Host     string `json:"host"`
	Origin   string `json:"origin"`
	Severity string `json:"severity"`
	Live     string `json:"live"`
	Revision string `json:"revision,omitempty"`
	Message  string `json:"message"`
	GitopsIP string `json:"gitops_ip,omitempty"`
	QMIP     string `json:"qm_ip,omitempty"`
}

type auditJSONReport struct {
	Clusters []string       `json:"clusters"`
	Rows     []auditJSONRow `json:"rows"`
}

func printAuditJSON(w io.Writer, f auditFindings, clusterIDs []string) error {
	rows := make([]auditJSONRow, 0, len(f.rows))
	for _, r := range f.rows {
		cluster := r.clusterID
		if cluster == "" {
			cluster = "-"
		}
		rows = append(rows, auditJSONRow{
			Cluster:  cluster,
			Host:     r.host,
			Origin:   r.origin,
			Severity: severityLabel(r.severity),
			Live:     livenessLabel(r.live),
			Revision: r.revision,
			Message:  r.message,
			GitopsIP: r.gitopsIP,
			QMIP:     r.qmIP,
		})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(auditJSONReport{Clusters: clusterIDs, Rows: rows})
}
