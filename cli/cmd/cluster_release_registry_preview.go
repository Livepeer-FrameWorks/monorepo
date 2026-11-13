package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"frameworks/cli/pkg/bootstrap"
	"frameworks/cli/pkg/ssh"
	"github.com/lib/pq"
	"github.com/spf13/cobra"
)

type registryPreviewRow struct {
	InstanceID  string     `json:"instance_id"`
	ServiceName string     `json:"service_name"`
	ClusterID   string     `json:"cluster_id"`
	NodeID      string     `json:"node_id"`
	Protocol    string     `json:"protocol"`
	Port        int        `json:"port"`
	Source      string     `json:"registration_source"`
	Owner       string     `json:"registry_owner"`
	Status      string     `json:"status"`
	NullStatus  bool       `json:"null_status"`
	UpdatedAt   *time.Time `json:"updated_at"`
}

var releaseRegistryInstancesFn = readReleaseRegistryInstances

func registryTuple(service, cluster, node, protocol string, port int) string {
	if protocol == "" {
		protocol = "http"
	}
	return fmt.Sprintf("%s\x1f%s\x1f%s\x1f%s\x1f%d", service, cluster, node, protocol, port)
}

func registryRetirementPreview(desired bootstrap.QuartermasterSection, rows []registryPreviewRow) ([]registryPreviewRow, error) {
	scope := desired.RegistryScope
	if scope == nil || !scope.Complete {
		return nil, nil
	}
	byID := map[string]registryPreviewRow{}
	for _, row := range rows {
		byID[row.InstanceID] = row
	}
	adopted := map[string]bool{}
	for _, item := range scope.Adopt {
		row, found := byID[item.InstanceID]
		adoptable := row.Source == "legacy" || (row.Source == "manifest" && row.Owner == scope.Owner)
		if !found || row.ServiceName != item.ServiceName || row.ClusterID != item.ClusterID || row.NodeID != item.NodeID || row.Protocol != item.Protocol || row.Port != item.Port || !slices.Contains(scope.Clusters, row.ClusterID) || !adoptable {
			return nil, fmt.Errorf("registry adoption %q no longer matches its reviewed tuple and ownership", item.InstanceID)
		}
		adopted[item.InstanceID] = true
	}
	wanted := map[string]bool{}
	for _, entry := range desired.ServiceRegistry {
		var selected *registryPreviewRow
		ambiguous := false
		for i := range rows {
			row := &rows[i]
			if registryTuple(row.ServiceName, row.ClusterID, row.NodeID, row.Protocol, row.Port) != registryTuple(entry.ServiceName, entry.ClusterID, entry.NodeID, entry.Protocol, entry.Port) {
				continue
			}
			// Adoption updates updated_at to the transaction timestamp before
			// the tuple lookup. It therefore takes precedence over old rows.
			if selected != nil && adopted[selected.InstanceID] {
				if adopted[row.InstanceID] {
					ambiguous = true
				}
				continue
			}
			if adopted[row.InstanceID] {
				selected = row
				ambiguous = false
				continue
			}
			if selected == nil || (row.UpdatedAt != nil && (selected.UpdatedAt == nil || row.UpdatedAt.After(*selected.UpdatedAt))) {
				selected = row
				ambiguous = false
				continue
			}
			if (row.UpdatedAt == nil && selected.UpdatedAt == nil) || (row.UpdatedAt != nil && selected.UpdatedAt != nil && row.UpdatedAt.Equal(*selected.UpdatedAt)) {
				ambiguous = true
			}
		}
		if ambiguous {
			return nil, fmt.Errorf("registry tuple for %s has ambiguous equally-recent duplicates", entry.ServiceName)
		}
		if selected == nil {
			continue
		}
		claimable := selected.Owner == scope.Owner || selected.Source == "legacy" || (selected.Source == "manifest" && selected.Owner == "")
		if !claimable {
			return nil, fmt.Errorf("registry instance %s is owned by another manifest or a runtime registrar", selected.InstanceID)
		}
		wanted[selected.InstanceID] = true
	}
	var stopped []registryPreviewRow
	for _, row := range rows {
		managed := adopted[row.InstanceID] || (row.Source == "manifest" && row.Owner == scope.Owner)
		if row.NullStatus || row.Status == "stopped" || !slices.Contains(scope.Clusters, row.ClusterID) || !managed {
			continue
		}
		if !wanted[row.InstanceID] {
			stopped = append(stopped, row)
		}
	}
	slices.SortFunc(stopped, func(a, b registryPreviewRow) int { return strings.Compare(a.InstanceID, b.InstanceID) })
	return stopped, nil
}

func writeRegistryRetirementPreview(ctx context.Context, cmd *cobra.Command, rc *resolvedCluster, pool *ssh.Pool, desired bootstrap.QuartermasterSection) error {
	if desired.RegistryScope == nil || !desired.RegistryScope.Complete {
		return nil
	}
	rows, err := releaseRegistryInstancesFn(ctx, cmd, rc, pool, desired.RegistryScope)
	if err != nil {
		return fmt.Errorf("registry retirement preview incomplete: %w", err)
	}
	stopped, err := registryRetirementPreview(desired, rows)
	if err != nil {
		return err
	}
	return printRegistryRetirements(cmd.OutOrStdout(), desired.RegistryScope.Owner, stopped)
}

func printRegistryRetirements(out io.Writer, owner string, rows []registryPreviewRow) error {
	if _, err := fmt.Fprintf(out, "  Registry owner %s: would stop %d registration(s); apply rechecks ownership and adoption tuples.\n", owner, len(rows)); err != nil {
		return err
	}
	for _, row := range rows {
		if _, err := fmt.Fprintf(out, "    stop %s: %s cluster=%s node=%s %s:%d (source=%s)\n", row.InstanceID, row.ServiceName, row.ClusterID, row.NodeID, row.Protocol, row.Port, row.Source); err != nil {
			return err
		}
	}
	return nil
}

func readReleaseRegistryInstances(ctx context.Context, cmd *cobra.Command, rc *resolvedCluster, pool *ssh.Pool, scope *bootstrap.ServiceRegistryScope) ([]registryPreviewRow, error) {
	targets, err := postgresSnapshotTargets(rc)
	if err != nil {
		return nil, err
	}
	quoted := func(values []string) string {
		if len(values) == 0 {
			return "ARRAY[]::text[]"
		}
		parts := make([]string, len(values))
		for i, value := range values {
			parts[i] = pq.QuoteLiteral(value)
		}
		return "ARRAY[" + strings.Join(parts, ",") + "]::text[]"
	}
	// to_jsonb keeps this read compatible with pre-ownership schemas. The
	// scope is platform inventory, not tenant media, and the output is bounded.
	query := `BEGIN READ ONLY;
SET LOCAL statement_timeout='20s';
SELECT 'registry_retirement=' || COALESCE(json_agg(row_to_json(registry)), '[]'::json)::text FROM (
 SELECT i.instance_id,s.name AS service_name,i.cluster_id,COALESCE(i.node_id,'') AS node_id,i.protocol,i.port,
 COALESCE(to_jsonb(i)->>'registration_source','legacy') AS registration_source,
 COALESCE(to_jsonb(i)->>'registry_owner','') AS registry_owner,COALESCE(i.status,'') AS status,
 i.status IS NULL AS null_status, to_jsonb(i)->>'updated_at' AS updated_at
 FROM quartermaster.service_instances i JOIN quartermaster.services s ON s.service_id=i.service_id
 WHERE i.cluster_id=ANY(` + quoted(scope.Clusters) + `)
 ORDER BY i.instance_id LIMIT 5001
) registry;
COMMIT;`
	for _, target := range targets {
		if !slices.Contains(target.Databases, "quartermaster") {
			continue
		}
		result, err := runPostExpandReportScriptFn(ctx, pool, stringFlag(cmd, "ssh-key").Value, target, mediaAuthorityDatabaseScript(target, []mediaAuthorityDatabaseProbe{{Database: "quartermaster", SQL: query}}))
		if err != nil {
			return nil, err
		}
		if result == nil || result.ExitCode != 0 {
			return nil, fmt.Errorf("quartermaster registry could not be read")
		}
		for _, line := range strings.Split(result.Stdout, "\n") {
			raw, found := strings.CutPrefix(strings.TrimSpace(line), "registry_retirement=")
			if !found {
				continue
			}
			var rows []registryPreviewRow
			if err := json.Unmarshal([]byte(raw), &rows); err != nil {
				return nil, fmt.Errorf("decode registry preview: %w", err)
			}
			if len(rows) > 5000 {
				return nil, fmt.Errorf("registry preview exceeds 5000 rows; narrow the manifest scope")
			}
			return rows, nil
		}
		return nil, fmt.Errorf("quartermaster registry read returned no inventory")
	}
	return nil, fmt.Errorf("manifest declares no Quartermaster database")
}
