package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/ssh"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billingentitlements"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/nodeidentity"
	"github.com/lib/pq"
	"github.com/spf13/cobra"
)

const (
	postExpandReportDatabase = "quartermaster"
	postExpandReportRowLimit = 50
	postExpandNotRunPrefix   = "post-expand report: not run"
)

// postExpandSchemaReadySQL reports whether the v0.3.0 Quartermaster expand
// columns the report reads exist. The report SQL references them directly, so
// it cannot even be parsed on a database without them.
const postExpandSchemaReadySQL = `BEGIN READ ONLY;
SELECT
  EXISTS (SELECT 1 FROM information_schema.columns
          WHERE table_schema = 'quartermaster' AND table_name = 'node_fingerprints'
            AND column_name = 'node_identity_public_key_ed25519')
  AND EXISTS (SELECT 1 FROM information_schema.columns
              WHERE table_schema = 'quartermaster' AND table_name = 'tenants'
                AND column_name = 'billing_entitlements_observed_at')
  AND EXISTS (SELECT 1 FROM information_schema.tables
              WHERE table_schema = 'quartermaster' AND table_name = 'billing_entitlement_handoffs');
COMMIT;`

// customerKeylessNodesSQL is the runtime-enrolled counterpart of
// nodeidentity.ManagedKeylessNodesSQL. These nodes do not block the release;
// the strict identity check refuses each one when it next connects.
const customerKeylessNodesSQL = `
SELECT node.node_id,
       node.cluster_id,
       fingerprint.tenant_id::text AS tenant_id,
       node.enrollment_origin,
       GREATEST(node.last_heartbeat, fingerprint.last_seen) AS last_seen
FROM quartermaster.node_fingerprints fingerprint
JOIN quartermaster.infrastructure_nodes node ON node.node_id = fingerprint.node_id
JOIN quartermaster.infrastructure_clusters cluster ON cluster.cluster_id = node.cluster_id
WHERE node.status = 'active'
  AND cluster.is_active = true
  AND node.enrollment_origin = 'runtime_enrolled'
  AND (
      fingerprint.node_identity_public_key_ed25519 IS NULL
      OR octet_length(fingerprint.node_identity_public_key_ed25519) <> 32
  )
ORDER BY node.node_id`

// unobservedDNSGrantsSQL selects the tenants whose custom subdomain / domain
// grant quartermaster_tenant_dns_entitlements_v0_3_0 sets to false: that
// migration clears both flags on every tenant still at the 'epoch' observation.
const unobservedDNSGrantsSQL = `
SELECT tenant.id::text AS tenant_id,
       tenant.name,
       tenant.subdomain,
       tenant.custom_domain,
       tenant.custom_subdomain_enabled,
       tenant.custom_domain_enabled
FROM quartermaster.tenants tenant
WHERE tenant.billing_entitlements_observed_at = 'epoch'::timestamptz
  AND (tenant.custom_subdomain_enabled OR tenant.custom_domain_enabled)
ORDER BY tenant.id`

// postExpandReportSQL returns one JSON document with the totals, the first
// postExpandReportRowLimit rows of each set, and whether Purser's DNS handoff
// receipt exists. The document is cast to jsonb because json_agg output spans
// lines and jsonb text is always a single line, which the parser reads.
func postExpandReportSQL() string {
	limited := func(name, orderBy string) string {
		return fmt.Sprintf(`COALESCE((SELECT json_agg(r ORDER BY r.%[2]s) FROM (SELECT * FROM %[1]s ORDER BY %[2]s LIMIT %[3]d) r), '[]'::json)`,
			name, orderBy, postExpandReportRowLimit)
	}
	return `BEGIN READ ONLY;
SET LOCAL statement_timeout = '20s';
WITH managed AS (` + nodeidentity.ManagedKeylessNodesSQL + `),
customer AS (` + customerKeylessNodesSQL + `),
dns AS (` + unobservedDNSGrantsSQL + `)
SELECT json_build_object(
  'expand_applied', true,
  'managed_keyless_total', (SELECT COUNT(*) FROM managed),
  'managed_keyless', ` + limited("managed", "node_id") + `,
  'customer_keyless_total', (SELECT COUNT(*) FROM customer),
  'customer_keyless', ` + limited("customer", "node_id") + `,
  'dns_grants_total', (SELECT COUNT(*) FROM dns),
  'dns_grants', ` + limited("dns", "tenant_id") + `,
  'dns_handoff_present', EXISTS (
    SELECT 1 FROM quartermaster.billing_entitlement_handoffs
    WHERE handoff_key = ` + pq.QuoteLiteral(billingentitlements.TenantDNSHandoffKey) + `
  )
)::jsonb;
COMMIT;`
}

type postExpandKeylessNode struct {
	NodeID           string  `json:"node_id"`
	ClusterID        string  `json:"cluster_id"`
	TenantID         *string `json:"tenant_id"`
	EnrollmentOrigin string  `json:"enrollment_origin"`
	LastSeen         *string `json:"last_seen"`
}

type postExpandDNSGrant struct {
	TenantID               string  `json:"tenant_id"`
	Name                   string  `json:"name"`
	Subdomain              *string `json:"subdomain"`
	CustomDomain           *string `json:"custom_domain"`
	CustomSubdomainEnabled bool    `json:"custom_subdomain_enabled"`
	CustomDomainEnabled    bool    `json:"custom_domain_enabled"`
}

type postExpandReport struct {
	ExpandApplied        bool                    `json:"expand_applied"`
	ManagedKeylessTotal  int64                   `json:"managed_keyless_total"`
	ManagedKeyless       []postExpandKeylessNode `json:"managed_keyless"`
	CustomerKeylessTotal int64                   `json:"customer_keyless_total"`
	CustomerKeyless      []postExpandKeylessNode `json:"customer_keyless"`
	DNSGrantsTotal       int64                   `json:"dns_grants_total"`
	DNSGrants            []postExpandDNSGrant    `json:"dns_grants"`
	DNSHandoffPresent    bool                    `json:"dns_handoff_present"`
}

// postExpandReportScript runs the schema probe and, only when it passes, the
// report on the database host. The last stdout line is always the JSON
// document; SQL is fed on stdin so its quoting never meets the shell.
func postExpandReportScript(target postgresSnapshotTarget) string {
	peer := "0"
	if target.UsePeerAuth {
		peer = "1"
	}
	var b strings.Builder
	b.WriteString("set +e\n")
	fmt.Fprintf(&b, "PORT=%d\n", target.Port)
	b.WriteString("USER_NAME=" + ssh.ShellQuote(target.User) + "\n")
	b.WriteString("PASSWORD=" + ssh.ShellQuote(target.Password) + "\n")
	b.WriteString("BINARY=" + ssh.ShellQuote(target.Binary) + "\n")
	b.WriteString("DB=" + ssh.ShellQuote(postExpandReportDatabase) + "\n")
	b.WriteString("PEER=" + peer + "\n")
	b.WriteString(`SQL_BIN=""
for candidate in "$BINARY" /home/yugabyte/tserver/bin/ysqlsh /opt/yugabyte/bin/ysqlsh /usr/local/bin/ysqlsh; do
  if command -v "$candidate" >/dev/null 2>&1; then
    SQL_BIN="$(command -v "$candidate")"
    break
  fi
done
if [ -z "$SQL_BIN" ]; then
  echo "$BINARY not found on the database host"
  exit 127
fi
run_sql() {
  if [ "$PEER" = "1" ] && command -v sudo >/dev/null 2>&1 && sudo -n true >/dev/null 2>&1; then
    sudo -u "$USER_NAME" "$SQL_BIN" -X -q -t -A -v ON_ERROR_STOP=1 -p "$PORT" -d "$DB" -P pager=off -f - 2>&1
  elif [ "$PEER" = "1" ]; then
    "$SQL_BIN" -X -q -t -A -v ON_ERROR_STOP=1 -p "$PORT" -d "$DB" -P pager=off -f - 2>&1
  else
    PGPASSWORD="$PASSWORD" "$SQL_BIN" -X -q -t -A -v ON_ERROR_STOP=1 -h 127.0.0.1 -p "$PORT" -U "$USER_NAME" -d "$DB" -P pager=off -f - 2>&1
  fi
}
`)
	fmt.Fprintf(&b, "READY=\"$(printf '%%s\\n' %s | run_sql)\"\nstatus=$?\n", ssh.ShellQuote(postExpandSchemaReadySQL))
	b.WriteString(`if [ "$status" -ne 0 ]; then printf '%s\n' "$READY"; exit "$status"; fi
READY="$(printf '%s' "$READY" | tr -d '[:space:]')"
if [ "$READY" != "t" ]; then echo '{"expand_applied":false}'; exit 0; fi
`)
	fmt.Fprintf(&b, "printf '%%s\\n' %s | run_sql\n", ssh.ShellQuote(postExpandReportSQL()))
	return b.String()
}

// parsePostExpandReport reads the JSON document from the last non-empty
// stdout line.
func parsePostExpandReport(stdout string) (postExpandReport, error) {
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	if !strings.HasPrefix(last, "{") {
		return postExpandReport{}, fmt.Errorf("unexpected report output: %q", strings.TrimSpace(stdout))
	}
	var report postExpandReport
	if err := json.Unmarshal([]byte(last), &report); err != nil {
		return postExpandReport{}, fmt.Errorf("decode report: %w", err)
	}
	return report, nil
}

// runPostExpandReportScriptFn executes the report script on the database host.
// Tests substitute it to return scripted psql output.
var runPostExpandReportScriptFn = func(ctx context.Context, pool *ssh.Pool, sshKey string, target postgresSnapshotTarget, script string) (*ssh.CommandResult, error) {
	runner, err := snapshotRunner(pool, sshKey, target.Host, 60*time.Second)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	return runner.RunScript(ctx, script)
}

// runReleasePostExpandReport prints the keyless-node and DNS-grant report that
// `release apply` shows between expand and the service upgrades. It never
// fails the release: the data-migration verifiers remain the gate, so every
// problem reaching the database is printed as the report's outcome.
func runReleasePostExpandReport(ctx context.Context, cmd *cobra.Command, rc *resolvedCluster, pool *ssh.Pool) {
	out := cmd.OutOrStdout()
	fmt.Fprintln(out, "  Post-expand report (Quartermaster, read-only; the data-migration verifiers remain the gate)")
	targets, err := postgresSnapshotTargets(rc)
	if err != nil {
		fmt.Fprintf(out, "  %s (resolve database target: %v)\n", postExpandNotRunPrefix, err)
		return
	}
	var target *postgresSnapshotTarget
	for i := range targets {
		for _, database := range targets[i].Databases {
			if database == postExpandReportDatabase {
				target = &targets[i]
				break
			}
		}
		if target != nil {
			break
		}
	}
	if target == nil {
		fmt.Fprintf(out, "  %s (no Quartermaster database in this manifest)\n", postExpandNotRunPrefix)
		return
	}
	result, err := runPostExpandReportScriptFn(ctx, pool, stringFlag(cmd, "ssh-key").Value, *target, postExpandReportScript(*target))
	if err == nil && result != nil && result.ExitCode != 0 {
		err = fmt.Errorf("exited %d: %s", result.ExitCode, strings.TrimSpace(result.Stdout+"\n"+result.Stderr))
	}
	if err == nil && result == nil {
		err = fmt.Errorf("no result")
	}
	if err != nil {
		ux.Warn(out, fmt.Sprintf("%s (query on %s failed: %v)", postExpandNotRunPrefix, target.HostName, err))
		return
	}
	report, err := parsePostExpandReport(result.Stdout)
	if err != nil {
		ux.Warn(out, fmt.Sprintf("%s (%v)", postExpandNotRunPrefix, err))
		return
	}
	writePostExpandReport(out, report)
}

func writePostExpandReport(out io.Writer, report postExpandReport) {
	if !report.ExpandApplied {
		fmt.Fprintf(out, "  %s (expand not applied)\n", postExpandNotRunPrefix)
		return
	}
	if report.ManagedKeylessTotal == 0 && report.CustomerKeylessTotal == 0 && report.DNSGrantsTotal == 0 {
		fmt.Fprintln(out, "  post-expand report: nothing pending (no keyless nodes, no DNS grants to clear)")
		return
	}

	fmt.Fprintf(out, "  Operator-managed nodes without an identity key: %d (postdeploy data-migration verify blocks until 0)\n", report.ManagedKeylessTotal)
	writePostExpandKeylessRows(out, report.ManagedKeyless, report.ManagedKeylessTotal)
	fmt.Fprintf(out, "  Customer-enrolled nodes without an identity key: %d (do not block the release; each is refused at its next connect until re-enrolled)\n", report.CustomerKeylessTotal)
	writePostExpandKeylessRows(out, report.CustomerKeyless, report.CustomerKeylessTotal)
	if report.ManagedKeylessTotal > 0 || report.CustomerKeylessTotal > 0 {
		fmt.Fprintln(out, "  Re-enroll each keyless node: issue a fresh edge_node token for its tenant and cluster")
		fmt.Fprintln(out, "  (`frameworks admin bootstrap-tokens create --kind edge_node --tenant-id <tenant> --cluster-id <cluster>`), set it as")
		fmt.Fprintln(out, "  EDGE_ENROLLMENT_TOKEN together with HELMSMAN_ROTATE_NODE_IDENTITY=true on the node, and restart its edge stack once.")
	}

	fmt.Fprintf(out, "  DNS grants quartermaster_tenant_dns_entitlements_v0_3_0 will clear: %d\n", report.DNSGrantsTotal)
	for _, grant := range report.DNSGrants {
		fmt.Fprintf(out, "    - tenant %s (%s)  custom subdomain=%s%s  custom domain=%s%s\n",
			grant.TenantID, grant.Name,
			postExpandOnOff(grant.CustomSubdomainEnabled), postExpandOptional(grant.Subdomain),
			postExpandOnOff(grant.CustomDomainEnabled), postExpandOptional(grant.CustomDomain))
	}
	writePostExpandTruncation(out, len(report.DNSGrants), report.DNSGrantsTotal)
	if report.DNSGrantsTotal > 0 {
		if report.DNSHandoffPresent {
			fmt.Fprintf(out, "  Purser DNS entitlement handoff %q: recorded; the migration clears these grants when it runs\n", billingentitlements.TenantDNSHandoffKey)
		} else {
			fmt.Fprintf(out, "  Purser DNS entitlement handoff %q: not recorded yet; the migration waits for it before clearing anything\n", billingentitlements.TenantDNSHandoffKey)
		}
	}
}

func writePostExpandKeylessRows(out io.Writer, rows []postExpandKeylessNode, total int64) {
	for _, row := range rows {
		fmt.Fprintf(out, "    - %s  cluster=%s  tenant=%s  origin=%s  last seen=%s\n",
			row.NodeID, row.ClusterID, postExpandOr(row.TenantID, "-"), row.EnrollmentOrigin, postExpandOr(row.LastSeen, "never"))
	}
	writePostExpandTruncation(out, len(rows), total)
}

func writePostExpandTruncation(out io.Writer, shown int, total int64) {
	if int64(shown) < total {
		fmt.Fprintf(out, "    (showing %d of %d)\n", shown, total)
	}
}

func postExpandOnOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

func postExpandOptional(v *string) string {
	if v == nil || strings.TrimSpace(*v) == "" {
		return ""
	}
	return " (" + *v + ")"
}

func postExpandOr(v *string, fallback string) string {
	if v == nil || strings.TrimSpace(*v) == "" {
		return fallback
	}
	return *v
}
