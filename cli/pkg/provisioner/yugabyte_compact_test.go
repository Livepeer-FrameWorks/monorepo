package provisioner

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// compactFakeNode answers catalog queries and shell scripts from canned output and records every script it runs.
type compactFakeNode struct {
	name     string
	catalog  string
	tablets  string
	listed   string
	metrics  string
	shellErr error
	scripts  []string
}

func (n *compactFakeNode) Name() string { return n.name }

func (n *compactFakeNode) Query(_ context.Context, _, sql string) (string, error) {
	if strings.Contains(sql, "yb_local_tablets") {
		return n.tablets, nil
	}
	return n.catalog, nil
}

func (n *compactFakeNode) Shell(_ context.Context, script string) (string, error) {
	n.scripts = append(n.scripts, script)
	switch {
	case strings.Contains(script, "list_tables"):
		return n.listed, nil
	case strings.Contains(script, "/metrics?"):
		return n.metrics, nil
	}
	return "Compacted [000033e6000030008000000000004000] tables.", n.shellErr
}

// compactTestCatalog is ordered by relation name, as yugabyteCompactionCatalogQuery returns it.
const compactTestCatalog = `audit.events|r||000033e6000030008000000000004020|f|1
public.events|r||000033e6000030008000000000004000|f|3
public.events_by_tenant|i|public.events|000033e6000030008000000000004005|f|3
public.rollup|m||000033e6000030008000000000004030|f|1
public.settings|r||000033e6000030008000000000004010|t|1
public.settings_key|i|public.settings|000033e6000030008000000000004011|t|1
`

func compactTestRelations(t *testing.T) []yugabyteCompactionRelation {
	t.Helper()
	relations, err := parseYugabyteCompactionRelations(compactTestCatalog)
	if err != nil {
		t.Fatal(err)
	}
	return relations
}

func compactTargetNames(targets []YugabyteCompactionTarget) []string {
	names := make([]string, 0, len(targets))
	for _, target := range targets {
		names = append(names, target.Name)
	}
	return names
}

func TestResolveYugabyteCompactionTargetsDefaultsToEveryTable(t *testing.T) {
	targets, err := resolveYugabyteCompactionTargets(compactTestRelations(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"colocated tablet", "audit.events", "public.events", "public.events_by_tenant", "public.rollup"}
	if got := compactTargetNames(targets); !slices.Equal(got, want) {
		t.Fatalf("targets = %v, want %v", got, want)
	}
	colocated := targets[0]
	if !colocated.Colocated || colocated.TableID != "000033e6000030008000000000004010" || !slices.Equal(colocated.Relations, []string{"public.settings", "public.settings_key"}) {
		t.Fatalf("colocated target = %+v, want one target for the shared tablet covering settings and its index", colocated)
	}
}

func TestResolveYugabyteCompactionTargetsSelectsTablesWithTheirIndexes(t *testing.T) {
	targets, err := resolveYugabyteCompactionTargets(compactTestRelations(t), []string{"public.events", "rollup"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"public.events", "public.events_by_tenant", "public.rollup"}
	if got := compactTargetNames(targets); !slices.Equal(got, want) {
		t.Fatalf("targets = %v, want %v", got, want)
	}
	if targets[0].TableID != "000033e6000030008000000000004000" || targets[0].Tablets != 3 {
		t.Fatalf("events target = %+v", targets[0])
	}
}

func TestResolveYugabyteCompactionTargetsRefusesUnknownAndAmbiguousNames(t *testing.T) {
	for name, requested := range map[string][]string{
		"unknown table":       {"public.events", "public.missing"},
		"ambiguous bare name": {"events"},
		"index is not table":  {"public.events_by_tenant"},
		"other schema":        {"audit.settings"},
	} {
		t.Run(name, func(t *testing.T) {
			if targets, err := resolveYugabyteCompactionTargets(compactTestRelations(t), requested); err == nil {
				t.Fatalf("resolve %v = %v, want a refusal", requested, compactTargetNames(targets))
			}
		})
	}
	_, err := resolveYugabyteCompactionTargets(compactTestRelations(t), []string{"events"})
	if err == nil || !strings.Contains(err.Error(), "audit.events, public.events") {
		t.Fatalf("ambiguous refusal = %v, want both candidates named", err)
	}
}

func TestParseYugabyteCompactionRelationsRejectsMalformedRows(t *testing.T) {
	for _, row := range []string{
		"public.events|r||000033e6000030008000000000004000|f",
		"public.events|r||not-a-table-id|f|3",
		"public.events|r||000033e6000030008000000000004000|maybe|3",
	} {
		if _, err := parseYugabyteCompactionRelations(row); err == nil {
			t.Fatalf("row %q parsed, want an error", row)
		}
	}
}

func TestYugabyteCompactionResolveRefusesAnIDTheMastersDoNotList(t *testing.T) {
	node := &compactFakeNode{
		name:    "yb-1",
		catalog: compactTestCatalog,
		listed:  "ysql.app.events 000033e6000030008000000000004000\nysql.other.rollup 000033e6000030008000000000004030\n",
	}
	compaction := &YugabyteCompaction{Primary: node, MasterAddresses: "10.0.0.1:7100", Database: "app"}
	if _, err := compaction.Resolve(context.Background(), []string{"public.events"}); err == nil {
		t.Fatal("resolve succeeded although the index id is not listed by the masters")
	}
	_, err := compaction.Resolve(context.Background(), []string{"public.rollup"})
	if err == nil || !strings.Contains(err.Error(), "000033e6000030008000000000004030") {
		t.Fatalf("resolve of a table listed only in another database = %v, want a refusal naming its id", err)
	}

	node.listed += "ysql.app.events_by_tenant 000033e6000030008000000000004005\n"
	targets, err := compaction.Resolve(context.Background(), []string{"public.events"})
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 {
		t.Fatalf("targets = %v", compactTargetNames(targets))
	}
	if !strings.Contains(node.scripts[len(node.scripts)-1], "--master_addresses '10.0.0.1:7100' --timeout_ms 60000 list_tables include_db_type include_table_id") {
		t.Fatalf("list script = %s", node.scripts[len(node.scripts)-1])
	}
}

func TestYugabyteCompactionCompactRunsCompactTableByIDWithTheTimeout(t *testing.T) {
	node := &compactFakeNode{name: "yb-1"}
	compaction := &YugabyteCompaction{Primary: node, MasterAddresses: "10.0.0.1:7100,10.0.0.2:7100", Database: "app"}
	target := YugabyteCompactionTarget{Name: "public.events", TableID: "000033e6000030008000000000004000"}
	if _, err := compaction.Compact(context.Background(), target, 90*time.Minute); err != nil {
		t.Fatal(err)
	}
	script := node.scripts[0]
	for _, want := range []string{
		`admin="$(fw_yb_bin yb-admin)"`,
		"--master_addresses '10.0.0.1:7100,10.0.0.2:7100' --timeout_ms 5400000 compact_table_by_id '000033e6000030008000000000004000' 5400",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("compact script lacks %q:\n%s", want, script)
		}
	}

	node.shellErr = errors.New("exit 1: Timed out waiting for FlushTables")
	if _, err := compaction.Compact(context.Background(), target, time.Minute); err == nil {
		t.Fatal("a yb-admin failure was not returned")
	}
}

func TestYugabyteCompactionMeasureSumsReplicasAcrossTServers(t *testing.T) {
	metrics := func(entries string) string { return "[" + entries + "]" }
	tablet := func(id string, bytes, files int) string {
		return `{"type":"tablet","id":"` + id + `","attributes":{},"metrics":[{"name":"rocksdb_current_version_sst_files_size","value":` +
			strconv.Itoa(bytes) + `},{"name":"rocksdb_current_version_num_sst_files","value":` + strconv.Itoa(files) + `}]}`
	}
	one := &compactFakeNode{
		name:    "yb-1",
		tablets: "t1|000033e6000030008000000000004000\nt2|000033e6000030008000000000004000\nc1|000033e6000030008000000000004001.colocation.parent.uuid\nx1|000033e6000030008000000000009999\n",
		metrics: metrics(tablet("t1", 100, 2) + "," + tablet("t2", 50, 1) + "," + tablet("c1", 7, 1) + `,{"type":"server","id":"yb.tabletserver","metrics":[]}`),
	}
	two := &compactFakeNode{
		name:    "yb-2",
		tablets: "t1|000033e6000030008000000000004000\n",
		metrics: metrics(tablet("t1", 110, 3)),
	}
	compaction := &YugabyteCompaction{Primary: one, Database: "app", TServers: []YugabyteCompactionTServer{
		{Node: one, WebAddress: "10.0.0.1:9000"}, {Node: two},
	}}
	targets := []YugabyteCompactionTarget{
		{Name: "colocated tablet", TableID: "000033e6000030008000000000004010", Colocated: true},
		{Name: "public.events", TableID: "000033e6000030008000000000004000"},
	}
	if _, err := compaction.Measure(context.Background(), targets); err != nil {
		t.Fatal(err)
	}
	if got := targets[1]; got.Tablets != 2 || got.Replicas != 3 || got.SSTBytes != 260 || got.SSTFiles != 6 {
		t.Fatalf("events = %+v, want 2 tablets, 3 replicas, 260 bytes, 6 files", got)
	}
	if got := targets[0]; got.Tablets != 1 || got.Replicas != 1 || got.SSTBytes != 7 {
		t.Fatalf("colocated = %+v, want the parent tablet", got)
	}
	if !strings.Contains(one.scripts[0], "web='10.0.0.1:9000'") || !strings.Contains(two.scripts[0], "web=''") {
		t.Fatalf("metrics scripts did not carry the web address: %q / %q", one.scripts[0], two.scripts[0])
	}
}

func TestYugabyteMasterTableIDsReadsOnlyTheDatabase(t *testing.T) {
	ids := yugabyteMasterTableIDs("ysql.app.events 0001\nysql.app2.events 0002\nycql.app.events 0003\nysql.app.parent.tablename 0004.colocation.parent.uuid\n", "app")
	if _, ok := ids["0001"]; !ok || len(ids) != 2 {
		t.Fatalf("ids = %v", ids)
	}
}

func TestSortYugabyteCompactionTargetsLargestFirst(t *testing.T) {
	targets := []YugabyteCompactionTarget{{Name: "b", SSTBytes: 1}, {Name: "a", SSTBytes: 1}, {Name: "c", SSTBytes: 9}}
	SortYugabyteCompactionTargets(targets)
	if got := compactTargetNames(targets); !slices.Equal(got, []string{"c", "a", "b"}) {
		t.Fatalf("order = %v", got)
	}
}
