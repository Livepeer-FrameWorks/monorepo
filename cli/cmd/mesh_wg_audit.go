package cmd

import (
	"context"
	"fmt"
	"io"
	"sort"
	"text/tabwriter"

	"frameworks/cli/internal/mesh"
	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/inventory"
	pb "frameworks/pkg/proto"

	"github.com/spf13/cobra"
)

// Node-origin vocabulary, mirrored from pkg/database/sql/schema/quartermaster.sql
// and api_tenants/internal/grpc/server.go. A row's enrollment_origin governs
// whether GitOps is authoritative for its WireGuard identity.
const (
	enrollmentOriginGitopsSeed      = "gitops_seed"
	enrollmentOriginRuntimeEnrolled = "runtime_enrolled"
	enrollmentOriginAdoptedLocal    = "adopted_local"
)

func newMeshWgAuditCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "audit",
		Short: "Compare GitOps WireGuard identity against Quartermaster's recorded peer state",
		Long: `Reads the GitOps cluster manifest and cross-references it with the
infrastructure_nodes rows Quartermaster has stored for the same cluster.

Mismatches on gitops_seed or adopted_local rows are reported as errors
(those origins mean GitOps is authoritative). runtime_enrolled rows are
printed as informational — they are expected to not appear in GitOps
until promoted via 'mesh reconcile --write-gitops'.`,
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

			findings := auditMeshIdentity(rc.Manifest, hostNames, resp.GetNodes(), manifestClusters)
			printAuditReport(cmd.OutOrStdout(), findings, rc.Manifest.AllClusterIDs())

			if findings.hasErrors() {
				return fmt.Errorf("mesh wg audit: %d authoritative host(s) diverge from Quartermaster", findings.errorCount())
			}
			ux.Success(cmd.OutOrStdout(), fmt.Sprintf("mesh wg audit: %d host(s) match Quartermaster", len(findings.rows)))
			return nil
		},
	}
}

type auditSeverity int

const (
	auditOK auditSeverity = iota
	auditInfo
	auditWarn
	auditError
)

type auditRow struct {
	host      string
	clusterID string
	origin    string
	severity  auditSeverity
	message   string
	gitopsIP  string
	qmIP      string
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

// auditMeshIdentity compares the manifest's per-host WireGuard identity against
// Quartermaster's recorded infrastructure_nodes rows. manifestClusters is the
// set of cluster IDs this manifest owns (from Manifest.AllClusterIDs); QM rows
// in other clusters are ignored entirely.
func auditMeshIdentity(manifest *inventory.Manifest, hostNames []string, qmNodes []*pb.InfrastructureNode, manifestClusters map[string]bool) auditFindings {
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

		row := auditRow{
			host:      name,
			clusterID: clusterID,
			origin:    origin,
			gitopsIP:  host.WireguardIP,
			qmIP:      qmIP,
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
		row := auditRow{
			host:      n.GetNodeName(),
			clusterID: cid,
			origin:    origin,
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
	fmt.Fprintln(tw, "CLUSTER\tHOST\tORIGIN\tSEVERITY\tDETAIL")
	for _, r := range f.rows {
		cluster := r.clusterID
		if cluster == "" {
			cluster = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", cluster, r.host, r.origin, severityLabel(r.severity), r.message)
	}
	tw.Flush()
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
