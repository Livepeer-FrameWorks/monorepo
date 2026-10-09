package provisioner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// YugabyteCompaction runs full compactions of the DocDB tables behind one YSQL database through yb-admin, one table at
// a time. A full compaction rewrites every SST file of every replica of every tablet of the table and drops deleted
// rows and tombstones older than the tservers' history retention (--timestamp_history_retention_interval_sec).
type YugabyteCompaction struct {
	// Primary is a tserver with local superuser YSQL and the yb-admin client; it resolves the catalog and runs yb-admin.
	Primary YugabyteNode
	// TServers are measured for SST size; a tserver that cannot be read is reported and skipped.
	TServers []YugabyteCompactionTServer
	// MasterAddresses is the comma-separated master RPC address list yb-admin connects to.
	MasterAddresses string
	Database        string
}

// YugabyteCompactionTServer is one tserver whose tablet replicas are measured.
type YugabyteCompactionTServer struct {
	Node YugabyteNode
	// WebAddress is the tserver web server as host:port. Empty reads --webserver_interface and --webserver_port from
	// the tserver flagfile the yugabyte role writes.
	WebAddress string
}

// YugabyteCompactionTarget is one DocDB table a compaction request names. A colocated database keeps every colocated
// table and index in one shared tablet, so one target covers all of them.
type YugabyteCompactionTarget struct {
	// Name is the YSQL relation, or "colocated tablet" for the shared tablet of a colocated database.
	Name string
	// TableID is the DocDB table id passed to compact_table_by_id.
	TableID string
	// Relations are the YSQL tables and indexes whose data this compaction rewrites.
	Relations []string
	Colocated bool
	// Tablets is the number of distinct tablets and Replicas the tablet peers measured across the tservers.
	Tablets  int
	Replicas int
	// SSTBytes and SSTFiles sum every measured replica.
	SSTBytes int64
	SSTFiles int
}

// YugabyteCompactionMeasurement is the SST state of a set of targets, plus the tservers that could not be read.
type YugabyteCompactionMeasurement struct {
	Unreachable []string
}

// yugabyteCompactionCatalogQuery lists every relation with its own storage in the current database: tables,
// materialized views, and secondary indexes outside system schemas, as pipe-separated rows under psql -tA. Primary-key
// indexes are stored inside their table, partitioned parents hold no rows, and vector indexes live with their base
// table, so none of those is a separate DocDB table. The DocDB table id of a YSQL relation is the database oid, a fixed
// marker, and the relation's relfilenode, which changes when a statement such as TRUNCATE rewrites the table.
const yugabyteCompactionCatalogQuery = `
SELECT n.nspname || '.' || c.relname,
       c.relkind,
       COALESCE(tn.nspname || '.' || tc.relname, ''),
       lpad(to_hex(d.oid::bigint), 8, '0') || '0000300080000000' || lpad(to_hex(c.relfilenode::bigint), 8, '0'),
       p.is_colocated,
       p.num_tablets
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
JOIN pg_database d ON d.datname = current_database()
LEFT JOIN pg_index i ON i.indexrelid = c.oid
LEFT JOIN pg_class tc ON tc.oid = i.indrelid
LEFT JOIN pg_namespace tn ON tn.oid = tc.relnamespace
LEFT JOIN pg_am am ON am.oid = c.relam
CROSS JOIN LATERAL yb_table_properties(c.oid) p
WHERE c.relkind IN ('r', 'm', 'i')
  AND n.nspname NOT IN ('pg_catalog', 'information_schema')
  AND n.nspname NOT LIKE 'pg_toast%'
  AND NOT COALESCE(i.indisprimary, false)
  AND COALESCE(am.amname, '') NOT IN ('ybhnsw', 'hnsw')
ORDER BY 1`

// yugabyteCompactionRelation is one row of yugabyteCompactionCatalogQuery.
type yugabyteCompactionRelation struct {
	Relation string
	Kind     string
	// Table is the indexed table of an index and empty for a table or materialized view.
	Table     string
	TableID   string
	Colocated bool
	Tablets   int
}

func parseYugabyteCompactionRelations(output string) ([]yugabyteCompactionRelation, error) {
	var relations []yugabyteCompactionRelation
	for line := range strings.SplitSeq(strings.TrimSpace(output), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		cols := strings.Split(line, "|")
		if len(cols) != 6 {
			return nil, fmt.Errorf("catalog row %q has %d columns, want 6", line, len(cols))
		}
		relation := yugabyteCompactionRelation{Relation: cols[0], Kind: cols[1], Table: cols[2], TableID: cols[3]}
		if err := scanPsqlValue(cols[4], &relation.Colocated); err != nil {
			return nil, fmt.Errorf("catalog row %q: %w", line, err)
		}
		if err := scanPsqlValue(cols[5], &relation.Tablets); err != nil {
			return nil, fmt.Errorf("catalog row %q: %w", line, err)
		}
		if !yugabyteTableIDPattern.MatchString(relation.TableID) {
			return nil, fmt.Errorf("catalog row %q: table id %q is not 32 hex digits", line, relation.TableID)
		}
		relations = append(relations, relation)
	}
	return relations, nil
}

var yugabyteTableIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// resolveYugabyteCompactionTargets turns requested tables into compaction targets. No requested table selects every
// table and materialized view. A requested name is schema.table, or a bare table name that exactly one schema holds;
// any name that names no table, or more than one, refuses the whole request. Each selected table brings its secondary
// indexes, because a delete writes tombstones into them too. Colocated relations collapse into one target for their
// shared tablet.
func resolveYugabyteCompactionTargets(relations []yugabyteCompactionRelation, requested []string) ([]YugabyteCompactionTarget, error) {
	tables := map[string]yugabyteCompactionRelation{}
	byBareName := map[string][]string{}
	for _, relation := range relations {
		if relation.Kind == "i" {
			continue
		}
		tables[relation.Relation] = relation
		_, bare, _ := strings.Cut(relation.Relation, ".")
		byBareName[bare] = append(byBareName[bare], relation.Relation)
	}
	selected := map[string]struct{}{}
	if len(requested) == 0 {
		for name := range tables {
			selected[name] = struct{}{}
		}
	}
	var problems []string
	for _, name := range requested {
		name = strings.TrimSpace(name)
		if _, ok := tables[name]; ok {
			selected[name] = struct{}{}
			continue
		}
		if !strings.Contains(name, ".") {
			switch matches := byBareName[name]; len(matches) {
			case 1:
				selected[matches[0]] = struct{}{}
				continue
			case 0:
			default:
				sort.Strings(matches)
				problems = append(problems, fmt.Sprintf("%s is ambiguous: %s", name, strings.Join(matches, ", ")))
				continue
			}
		}
		problems = append(problems, fmt.Sprintf("%s is not a table or materialized view in this database", name))
	}
	if len(problems) > 0 {
		return nil, errors.New(strings.Join(problems, "; "))
	}

	var targets []YugabyteCompactionTarget
	var colocated *YugabyteCompactionTarget
	for _, relation := range relations {
		owner := relation.Relation
		if relation.Kind == "i" {
			owner = relation.Table
		}
		if _, ok := selected[owner]; !ok {
			continue
		}
		if relation.Colocated {
			if colocated == nil {
				colocated = &YugabyteCompactionTarget{Name: "colocated tablet", TableID: relation.TableID, Colocated: true}
			}
			colocated.Relations = append(colocated.Relations, relation.Relation)
			continue
		}
		targets = append(targets, YugabyteCompactionTarget{Name: relation.Relation, TableID: relation.TableID, Relations: []string{relation.Relation}, Tablets: relation.Tablets})
	}
	if colocated != nil {
		targets = append([]YugabyteCompactionTarget{*colocated}, targets...)
	}
	if len(targets) == 0 {
		return nil, errors.New("the database holds no user tables")
	}
	return targets, nil
}

// Resolve reads the database catalog, selects the requested tables, and proves every target's table id is a table
// the masters know in this database before anything is compacted.
func (c *YugabyteCompaction) Resolve(ctx context.Context, requested []string) ([]YugabyteCompactionTarget, error) {
	out, err := c.Primary.Query(ctx, c.Database, yugabyteCompactionCatalogQuery)
	if err != nil {
		return nil, fmt.Errorf("read the catalog of %s: %w", c.Database, err)
	}
	relations, err := parseYugabyteCompactionRelations(out)
	if err != nil {
		return nil, err
	}
	targets, err := resolveYugabyteCompactionTargets(relations, requested)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", c.Database, err)
	}
	listed, err := c.Primary.Shell(ctx, c.adminScript(0, "list_tables include_db_type include_table_id"))
	if err != nil {
		return nil, fmt.Errorf("list tables on the masters: %w", err)
	}
	known := yugabyteMasterTableIDs(listed, c.Database)
	for _, target := range targets {
		if _, ok := known[target.TableID]; !ok {
			return nil, fmt.Errorf("the masters list no table %s in %s for %s; refusing to compact an id the catalog did not confirm", target.TableID, c.Database, target.Name)
		}
	}
	return targets, nil
}

// yugabyteMasterTableIDs reads `yb-admin list_tables include_db_type include_table_id`, whose rows are
// ysql.<database>.<table> <table id>, and returns the ids listed in database.
func yugabyteMasterTableIDs(output, database string) map[string]struct{} {
	prefix := "ysql." + database + "."
	ids := map[string]struct{}{}
	for line := range strings.SplitSeq(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.HasPrefix(fields[0], prefix) {
			continue
		}
		ids[fields[len(fields)-1]] = struct{}{}
	}
	return ids
}

// yugabyteLocalTabletsQuery lists the serving tablet replicas of the current database on the tserver that answers,
// with the DocDB table each belongs to.
const yugabyteLocalTabletsQuery = "SELECT tablet_id, table_id FROM yb_local_tablets WHERE namespace_name = current_database() AND state = 'TABLET_DATA_READY'"

// yugabyteTabletMetricsScript prints the per-tablet SST metrics of the tserver web server at %s, or of the address the
// tserver flagfile names when %s is empty.
const yugabyteTabletMetricsScript = `set -eu
web=%s
if [ -z "$web" ]; then
  conf=/opt/yugabyte/conf/tserver.conf
  web="$(sed -n 's/^--webserver_interface=//p' "$conf" | head -n 1):$(sed -n 's/^--webserver_port=//p' "$conf" | head -n 1)"
fi
curl -fsS --max-time 30 "http://${web}/metrics?metrics=rocksdb_current_version_sst_files_size,rocksdb_current_version_num_sst_files"
`

type yugabyteTabletSST struct {
	Bytes int64
	Files int
}

// parseYugabyteTabletMetrics reads the tserver /metrics JSON and returns the SST size and file count per tablet.
func parseYugabyteTabletMetrics(output string) (map[string]yugabyteTabletSST, error) {
	var entities []struct {
		Type    string `json:"type"`
		ID      string `json:"id"`
		Metrics []struct {
			Name  string          `json:"name"`
			Value json.RawMessage `json:"value"`
		} `json:"metrics"`
	}
	if err := json.Unmarshal([]byte(output), &entities); err != nil {
		return nil, fmt.Errorf("decode tserver metrics: %w", err)
	}
	tablets := map[string]yugabyteTabletSST{}
	for _, entity := range entities {
		if entity.Type != "tablet" {
			continue
		}
		sst := tablets[entity.ID]
		for _, metric := range entity.Metrics {
			value, err := strconv.ParseInt(strings.TrimSpace(string(metric.Value)), 10, 64)
			if err != nil {
				continue
			}
			switch metric.Name {
			case "rocksdb_current_version_sst_files_size":
				sst.Bytes = value
			case "rocksdb_current_version_num_sst_files":
				sst.Files = int(value)
			}
		}
		tablets[entity.ID] = sst
	}
	return tablets, nil
}

// Measure fills each target's distinct tablets, replicas, SST bytes, and SST files from every tserver it can read. A
// colocated target is measured by the tablets whose table is not one of the database's own distributed tables.
func (c *YugabyteCompaction) Measure(ctx context.Context, targets []YugabyteCompactionTarget) (YugabyteCompactionMeasurement, error) {
	var measurement YugabyteCompactionMeasurement
	byID := map[string]int{}
	colocated := -1
	for i := range targets {
		targets[i].Tablets, targets[i].Replicas, targets[i].SSTBytes, targets[i].SSTFiles = 0, 0, 0, 0
		if targets[i].Colocated {
			colocated = i
			continue
		}
		byID[targets[i].TableID] = i
	}
	distinct := make([]map[string]struct{}, len(targets))
	for i := range distinct {
		distinct[i] = map[string]struct{}{}
	}
	read := 0
	for _, tserver := range c.TServers {
		replicas, err := tserver.Node.Query(ctx, c.Database, yugabyteLocalTabletsQuery)
		if err != nil {
			measurement.Unreachable = append(measurement.Unreachable, fmt.Sprintf("%s: %v", tserver.Node.Name(), err))
			continue
		}
		metricsOut, err := tserver.Node.Shell(ctx, fmt.Sprintf(yugabyteTabletMetricsScript, shellQuote(tserver.WebAddress)))
		if err != nil {
			measurement.Unreachable = append(measurement.Unreachable, fmt.Sprintf("%s: %v", tserver.Node.Name(), err))
			continue
		}
		metrics, err := parseYugabyteTabletMetrics(metricsOut)
		if err != nil {
			measurement.Unreachable = append(measurement.Unreachable, fmt.Sprintf("%s: %v", tserver.Node.Name(), err))
			continue
		}
		read++
		for line := range strings.SplitSeq(strings.TrimSpace(replicas), "\n") {
			tabletID, tableID, ok := strings.Cut(strings.TrimSpace(line), "|")
			if !ok {
				continue
			}
			index, known := byID[tableID]
			if !known {
				if colocated < 0 || !isYugabyteParentTableID(tableID) {
					continue
				}
				index = colocated
			}
			sst := metrics[tabletID]
			distinct[index][tabletID] = struct{}{}
			targets[index].Replicas++
			targets[index].SSTBytes += sst.Bytes
			targets[index].SSTFiles += sst.Files
		}
	}
	for i := range targets {
		targets[i].Tablets = len(distinct[i])
	}
	if read == 0 {
		return measurement, fmt.Errorf("no tserver could be measured: %s", strings.Join(measurement.Unreachable, "; "))
	}
	return measurement, nil
}

// isYugabyteParentTableID reports whether a DocDB table id is the parent table of a colocated database or tablegroup,
// whose tablet holds every colocated relation.
func isYugabyteParentTableID(id string) bool {
	return strings.HasSuffix(id, ".colocation.parent.uuid") || strings.HasSuffix(id, ".tablegroup.parent.uuid")
}

// Compact asks the masters for a full compaction of target and waits up to timeout for every replica to finish it.
// yb-admin returns an error once the timeout passes; the tservers keep compacting regardless.
func (c *YugabyteCompaction) Compact(ctx context.Context, target YugabyteCompactionTarget, timeout time.Duration) (string, error) {
	seconds := max(int(timeout.Round(time.Second)/time.Second), 1)
	// The SSH command outlives yb-admin's own wait by a margin so a timeout is reported by yb-admin, not by a cut
	// connection.
	runCtx, cancel := context.WithTimeout(ctx, timeout+2*time.Minute)
	defer cancel()
	out, err := c.Primary.Shell(runCtx, c.adminScript(seconds, YugabyteCompactCommand(target.TableID, seconds)))
	return strings.TrimSpace(out), err
}

// YugabyteCompactCommand is the yb-admin command that fully compacts one DocDB table and waits up to seconds for it.
func YugabyteCompactCommand(tableID string, seconds int) string {
	return fmt.Sprintf("compact_table_by_id %s %d", shellQuote(tableID), seconds)
}

// adminScript runs a yb-admin command against the configured masters. A positive waitSeconds raises the RPC timeout
// above that wait so each master RPC is not the first thing to give up.
func (c *YugabyteCompaction) adminScript(waitSeconds int, command string) string {
	rpcTimeout := 60 * time.Second
	if wait := time.Duration(waitSeconds) * time.Second; wait > rpcTimeout {
		rpcTimeout = wait
	}
	return YugabyteBinaryResolverShell + fmt.Sprintf(`
set -eu
admin="$(fw_yb_bin yb-admin)"
"$admin" --master_addresses %s --timeout_ms %d %s
`, shellQuote(c.MasterAddresses), rpcTimeout.Milliseconds(), command)
}

// SortYugabyteCompactionTargets orders targets largest first, so the tables that hold the most dead data compact
// first.
func SortYugabyteCompactionTargets(targets []YugabyteCompactionTarget) {
	slices.SortStableFunc(targets, func(a, b YugabyteCompactionTarget) int {
		switch {
		case a.SSTBytes > b.SSTBytes:
			return -1
		case a.SSTBytes < b.SSTBytes:
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
}
