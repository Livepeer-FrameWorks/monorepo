//go:build schema_verify

package provisioner

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestYugabyteCompactionDropsDeletedRows fills a distributed table, deletes every row, and compacts the database
// through yb-admin the way `cluster yugabyte compact` does. The tservers' history retention is lowered for the test so
// the deleted rows are past it; the table's SST size must then fall far below what the inserts and tombstones took.
func TestYugabyteCompactionDropsDeletedRows(t *testing.T) {
	requireDocker(t)
	container := ybStart(t, fmt.Sprintf("fw-sv-yb-compact-%d", time.Now().UnixNano()))
	host := ybSQLHost(container)
	const database = "compact_contract"
	ybApply(t, container, "yugabyte", "DROP DATABASE IF EXISTS "+database+"; CREATE DATABASE "+database+" WITH COLOCATION = true;")
	t.Cleanup(func() { ybDropDatabase(t, container, database) })
	ybApply(t, container, database, `
CREATE TABLE public.events (id bigint PRIMARY KEY, payload text NOT NULL) WITH (colocation = false) SPLIT INTO 2 TABLETS;
CREATE INDEX events_bucket ON public.events ((id % 97));
CREATE TABLE public.settings (key text PRIMARY KEY, value text);
INSERT INTO public.settings VALUES ('a', 'b');`)

	setRetention := func(seconds int) error {
		_, err := docker(t, "", "exec", container, "/home/yugabyte/bin/yb-ts-cli", "--server_address", host+":9100", "set_flag", "timestamp_history_retention_interval_sec", fmt.Sprint(seconds))
		return err
	}
	if err := setRetention(0); err != nil {
		t.Fatalf("lower tserver history retention: %v", err)
	}
	t.Cleanup(func() {
		if err := setRetention(900); err != nil {
			t.Errorf("restore tserver history retention: %v", err)
		}
	})

	node := ybRelayoutNode(container, host)
	compaction := &YugabyteCompaction{
		Primary:         node,
		TServers:        []YugabyteCompactionTServer{{Node: node, WebAddress: host + ":9000"}},
		MasterAddresses: host + ":7100",
		Database:        database,
	}
	ctx, cancel := ybStatementContext(t)
	defer cancel()

	if _, err := compaction.Resolve(ctx, []string{"public.missing"}); err == nil || !strings.Contains(err.Error(), "public.missing") {
		t.Fatalf("resolve of an unknown table = %v, want a refusal naming it", err)
	}
	targets, err := compaction.Resolve(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]YugabyteCompactionTarget{}
	for _, target := range targets {
		names[target.Name] = target
	}
	events, ok := names["public.events"]
	if !ok || len(targets) != 3 || !names["colocated tablet"].Colocated {
		t.Fatalf("targets = %+v, want the colocated tablet, public.events, and public.events_bucket", targets)
	}

	ybApply(t, container, database, "INSERT INTO public.events SELECT g, repeat(md5(g::text), 8) FROM generate_series(1, 200000) g;")
	ybFlushTable(t, container, host, events.TableID)
	ybApply(t, container, database, "DELETE FROM public.events;")
	ybFlushTable(t, container, host, events.TableID)

	if _, err = compaction.Measure(ctx, targets); err != nil {
		t.Fatal(err)
	}
	before := map[string]YugabyteCompactionTarget{}
	for _, target := range targets {
		before[target.Name] = target
		t.Logf("before: %s tablets=%d replicas=%d sst_files=%d sst_bytes=%d", target.Name, target.Tablets, target.Replicas, target.SSTFiles, target.SSTBytes)
	}
	if got := before["public.events"]; got.Tablets != 2 || got.SSTBytes < 8<<20 || got.SSTFiles < 2*got.Tablets {
		t.Fatalf("public.events before compaction = %+v, want 2 tablets holding the flushed inserts and tombstones", got)
	}
	if got := before["colocated tablet"]; got.Tablets != 1 {
		t.Fatalf("colocated tablet = %+v, want its one shared tablet measured", got)
	}

	for _, target := range targets {
		started := time.Now()
		out, compactErr := compaction.Compact(ctx, target, 5*time.Minute)
		if compactErr != nil {
			t.Fatalf("compact %s: %v", target.Name, compactErr)
		}
		if !strings.Contains(out, "Compacted") || !strings.Contains(out, target.TableID) {
			t.Fatalf("compact %s printed %q, want yb-admin to report the table compacted", target.Name, out)
		}
		t.Logf("compacted %s in %s: %s", target.Name, time.Since(started).Round(time.Millisecond), out)
	}

	if _, err = compaction.Measure(ctx, targets); err != nil {
		t.Fatal(err)
	}
	for _, target := range targets {
		t.Logf("after: %s tablets=%d replicas=%d sst_files=%d sst_bytes=%d", target.Name, target.Tablets, target.Replicas, target.SSTFiles, target.SSTBytes)
		if target.SSTFiles > target.Replicas {
			t.Errorf("%s has %d SST files across %d replicas after a full compaction, want at most one per replica", target.Name, target.SSTFiles, target.Replicas)
		}
		if target.Name == "public.events" && target.SSTBytes*20 > before[target.Name].SSTBytes {
			t.Errorf("public.events SST size fell from %d to only %d bytes; the deleted rows were not dropped", before[target.Name].SSTBytes, target.SSTBytes)
		}
	}
	if got := ybQuery(t, container, database, "SELECT value FROM public.settings WHERE key = 'a'"); got != "b" {
		t.Fatalf("colocated row after compaction = %q, want b", got)
	}
}

func ybFlushTable(t *testing.T, container, host, tableID string) {
	t.Helper()
	if out, err := docker(t, "", "exec", container, "yb-admin", "--master_addresses", host+":7100", "flush_table_by_id", tableID, "120"); err != nil {
		t.Fatalf("flush %s: %v\n%s", tableID, err, out)
	}
}
