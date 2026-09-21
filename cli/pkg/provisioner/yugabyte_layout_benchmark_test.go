//go:build schema_verify

package provisioner

import (
	"fmt"
	"math/rand/v2"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"frameworks/cli/internal/releases"
	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
)

const (
	ybBenchmarkRows              = 200
	ybBenchmarkSessionsPerTable  = 4
	ybBenchmarkUpdatesPerSession = 250
	// A colocated table is reclassified when its p99 exceeds both twice the distributed p99 and this floor, so
	// sub-millisecond noise on an idle fixture never flips a classification.
	ybBenchmarkP99Floor = 20 * time.Millisecond
)

var ybBenchmarkTimingPattern = regexp.MustCompile(`(?m)^Time: ([0-9.]+) ms`)

type ybBenchmarkColumn struct {
	Name       string
	Type       string
	NotNull    bool
	HasDefault bool
	Generated  bool
	PrimaryKey bool
	Unique     bool
}

type ybBenchmarkResult struct {
	Table    string
	Samples  []time.Duration
	Errors   int
	Failures []string
}

func (r ybBenchmarkResult) percentile(p float64) time.Duration {
	if len(r.Samples) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), r.Samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	index := int(p * float64(len(sorted)-1))
	return sorted[index]
}

// TestYugabyteColocatedWriteRateBenchmark measures every write_rate_benchmark table under concurrent keyed updates,
// once with all of a database's benchmark tables sharing a colocated parent tablet and once distributed. It is opt-in
// because it takes minutes and its numbers inform classification rather than gate a build.
func TestYugabyteColocatedWriteRateBenchmark(t *testing.T) {
	if os.Getenv("FRAMEWORKS_YUGABYTE_BENCHMARK") != "1" {
		t.Skip("set FRAMEWORKS_YUGABYTE_BENCHMARK=1 (make verify-yugabyte-layout-benchmark) to measure write_rate_benchmark tables")
	}
	requireDocker(t)
	name := ybStart(t, fmt.Sprintf("fw-sv-yb-benchmark-%d", time.Now().UnixNano()))
	for _, database := range releases.ServiceDatabaseNames() {
		layout := ybDatabaseLayout(t, database)
		if !layout.Colocated() || len(layout.WriteRateBenchmark) == 0 {
			continue
		}
		t.Run(database, func(t *testing.T) {
			baseline, err := dbsql.Content.ReadFile("schema/" + database + ".sql")
			if err != nil {
				t.Fatalf("read %s baseline: %v", database, err)
			}
			results := map[string]map[string]ybBenchmarkResult{}
			for _, placement := range []struct {
				label  string
				layout *DatabaseLayout
			}{{"colocated", layout}, {"distributed", nil}} {
				benchDatabase := fmt.Sprintf("%s_bench_%s", database, placement.label)
				ybCreateDatabase(t, name, benchDatabase, placement.layout)
				t.Cleanup(func() { ybDropDatabase(t, name, benchDatabase) })
				ybApply(t, name, benchDatabase, string(baseline))
				results[placement.label] = ybRunWriteBenchmark(t, name, benchDatabase, layout.WriteRateBenchmark)
			}
			for _, table := range layout.WriteRateBenchmark {
				colocated, distributed := results["colocated"][table], results["distributed"][table]
				wantSamples := ybBenchmarkSessionsPerTable * ybBenchmarkUpdatesPerSession
				if len(colocated.Samples) != wantSamples || len(distributed.Samples) != wantSamples {
					t.Errorf("benchmark %s: measured %d colocated and %d distributed updates, want %d each; a table the benchmark cannot drive is not evidence",
						table, len(colocated.Samples), len(distributed.Samples), wantSamples)
					continue
				}
				if colocated.Errors > 0 || distributed.Errors > 0 {
					t.Errorf("benchmark %s: %d colocated and %d distributed sessions or statements failed:\n%s",
						table, colocated.Errors, distributed.Errors, strings.Join(append(colocated.Failures, distributed.Failures...), "\n"))
					continue
				}
				t.Logf("benchmark %s: colocated p50=%s p99=%s | distributed p50=%s p99=%s",
					table, colocated.percentile(0.5), colocated.percentile(0.99),
					distributed.percentile(0.5), distributed.percentile(0.99))
				if p99 := colocated.percentile(0.99); p99 > 2*distributed.percentile(0.99) && p99 > ybBenchmarkP99Floor {
					t.Errorf("%s colocated p99 %s exceeds twice its distributed p99 %s; classify it distributed", table, p99, distributed.percentile(0.99))
				}
			}
		})
	}
}

// ybRunWriteBenchmark copies each table without constraints or triggers, seeds a fleet-sized row set, and runs every
// table's update sessions concurrently so colocated copies contend for the same parent tablet.
func ybRunWriteBenchmark(t *testing.T, name, database string, tables []string) map[string]ybBenchmarkResult {
	t.Helper()
	type workload struct {
		table   string
		scripts []string
	}
	var workloads []workload
	for _, table := range tables {
		copyTable := "public.bench_" + strings.ReplaceAll(table, ".", "_")
		ybApply(t, name, database, fmt.Sprintf("CREATE TABLE %s (LIKE %s INCLUDING DEFAULTS INCLUDING INDEXES);", copyTable, table))
		columns := ybBenchmarkColumns(t, name, database, copyTable)
		insert, keys, update, ok := ybBenchmarkStatements(copyTable, columns)
		if !ok {
			continue
		}
		ybApply(t, name, database, insert)
		keyRows := strings.Split(ybQuery(t, name, database, keys), "\n")
		if len(keyRows) == 0 || keyRows[0] == "" {
			continue
		}
		var primaryKey []ybBenchmarkColumn
		for _, column := range columns {
			if column.PrimaryKey {
				primaryKey = append(primaryKey, column)
			}
		}
		scripts := make([]string, ybBenchmarkSessionsPerTable)
		for session := range scripts {
			var script strings.Builder
			script.WriteString("\\timing on\n")
			for range ybBenchmarkUpdatesPerSession {
				values := strings.Split(keyRows[rand.IntN(len(keyRows))], "|")
				predicates := make([]string, len(primaryKey))
				for i, column := range primaryKey {
					predicates[i] = fmt.Sprintf("%s = %s::%s", relayoutIdentifier(column.Name), relayoutLiteral(values[i]), column.Type)
				}
				fmt.Fprintf(&script, "%s WHERE %s;\n", update, strings.Join(predicates, " AND "))
			}
			scripts[session] = script.String()
		}
		workloads = append(workloads, workload{table: table, scripts: scripts})
	}

	results := map[string]ybBenchmarkResult{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, work := range workloads {
		for _, script := range work.scripts {
			wg.Add(1)
			go func(table, script string) {
				defer wg.Done()
				out, sessionErr := dockerWithTimeout(t, 10*time.Minute, script, "exec", "-i", name, "bash", "-c",
					fmt.Sprintf("ysqlsh -X -h %s -U yugabyte -d %s 2>&1", ybSQLHost(name), database))
				var samples []time.Duration
				for _, match := range ybBenchmarkTimingPattern.FindAllStringSubmatch(out, -1) {
					if milliseconds, err := strconv.ParseFloat(match[1], 64); err == nil {
						samples = append(samples, time.Duration(milliseconds*float64(time.Millisecond)))
					}
				}
				mu.Lock()
				defer mu.Unlock()
				result := results[table]
				result.Table = table
				result.Samples = append(result.Samples, samples...)
				if sessionErr != nil {
					result.Errors++
					result.Failures = append(result.Failures, fmt.Sprintf("session: %v", sessionErr))
				}
				for line := range strings.SplitSeq(out, "\n") {
					if strings.Contains(line, "ERROR:") {
						result.Errors++
						result.Failures = append(result.Failures, strings.TrimSpace(line))
					}
				}
				results[table] = result
			}(work.table, script)
		}
	}
	wg.Wait()
	return results
}

func ybBenchmarkColumns(t *testing.T, name, database, table string) []ybBenchmarkColumn {
	t.Helper()
	out := ybQuery(t, name, database, fmt.Sprintf(`
SELECT a.attname, format_type(a.atttypid, a.atttypmod), a.attnotnull, a.atthasdef OR a.attidentity <> '', a.attgenerated <> '',
       EXISTS (SELECT 1 FROM pg_index i WHERE i.indrelid = a.attrelid AND i.indisprimary AND a.attnum = ANY(i.indkey)),
       EXISTS (SELECT 1 FROM pg_index i WHERE i.indrelid = a.attrelid AND i.indisunique AND a.attnum = ANY(i.indkey))
  FROM pg_attribute a
 WHERE a.attrelid = %s::regclass AND a.attnum > 0 AND NOT a.attisdropped
 ORDER BY a.attnum`, relayoutLiteral(table)))
	var columns []ybBenchmarkColumn
	for line := range strings.SplitSeq(out, "\n") {
		fields := strings.Split(line, "|")
		if len(fields) != 7 {
			continue
		}
		columns = append(columns, ybBenchmarkColumn{
			Name: fields[0], Type: fields[1], NotNull: fields[2] == "t", HasDefault: fields[3] == "t",
			Generated: fields[4] == "t", PrimaryKey: fields[5] == "t", Unique: fields[6] == "t",
		})
	}
	return columns
}

// ybBenchmarkStatements renders the seed insert, the key query, and the update prefix for a copied table. It reports
// false when a required column has a type without a generated value or no column can be updated safely.
func ybBenchmarkStatements(table string, columns []ybBenchmarkColumn) (string, string, string, bool) {
	var names, values, keys []string
	update := ""
	for _, column := range columns {
		if column.Generated {
			continue
		}
		if column.PrimaryKey || (column.NotNull && !column.HasDefault) {
			value, ok := ybBenchmarkSeedValue(column.Type)
			if !ok {
				return "", "", "", false
			}
			names = append(names, relayoutIdentifier(column.Name))
			values = append(values, value)
		}
		if column.PrimaryKey {
			keys = append(keys, relayoutIdentifier(column.Name)+"::text")
			continue
		}
		if update == "" && !column.Unique {
			if expression, ok := ybBenchmarkUpdateValue(column); ok {
				update = fmt.Sprintf("UPDATE %s SET %s = %s", table, relayoutIdentifier(column.Name), expression)
			}
		}
	}
	if len(keys) == 0 || update == "" {
		return "", "", "", false
	}
	insert := fmt.Sprintf("INSERT INTO %s (%s) SELECT %s FROM generate_series(1, %d) AS g ON CONFLICT DO NOTHING;",
		table, strings.Join(names, ", "), strings.Join(values, ", "), ybBenchmarkRows)
	keyQuery := fmt.Sprintf("SELECT %s FROM %s", strings.Join(keys, ", "), table)
	return insert, keyQuery, update, true
}

var ybBenchmarkVarcharPattern = regexp.MustCompile(`^character(?: varying)?\((\d+)\)$`)

func ybBenchmarkSeedValue(columnType string) (string, bool) {
	if match := ybBenchmarkVarcharPattern.FindStringSubmatch(columnType); match != nil {
		return fmt.Sprintf("left('bench-' || g, %s)", match[1]), true
	}
	switch {
	case columnType == "uuid":
		return "gen_random_uuid()", true
	case columnType == "text" || columnType == "character varying":
		return "'bench-' || g", true
	case columnType == "bigint" || columnType == "integer" || strings.HasPrefix(columnType, "numeric") || columnType == "double precision" || columnType == "real":
		return "g", true
	case columnType == "smallint":
		return "(g % 30000)::smallint", true
	case columnType == "boolean":
		return "(g % 2 = 0)", true
	case strings.HasPrefix(columnType, "timestamp"):
		return "now() + make_interval(secs => g)", true
	case columnType == "date":
		return "current_date + g", true
	case columnType == "jsonb" || columnType == "json":
		return "'{}'", true
	case columnType == "bytea":
		return "decode('00', 'hex')", true
	case columnType == "inet":
		return "'10.0.0.1'::inet", true
	case strings.HasSuffix(columnType, "[]"):
		return "'{}'", true
	case columnType == "interval":
		return "interval '1 second'", true
	}
	return "", false
}

func ybBenchmarkUpdateValue(column ybBenchmarkColumn) (string, bool) {
	name := relayoutIdentifier(column.Name)
	if match := ybBenchmarkVarcharPattern.FindStringSubmatch(column.Type); match != nil {
		return fmt.Sprintf("left(md5(random()::text), %s)", match[1]), true
	}
	switch {
	case strings.HasPrefix(column.Type, "timestamp"):
		return "now()", true
	case column.Type == "bigint" || column.Type == "integer":
		return fmt.Sprintf("coalesce(%s, 0) + 1", name), true
	case column.Type == "boolean":
		return fmt.Sprintf("NOT coalesce(%s, false)", name), true
	case column.Type == "text" || column.Type == "character varying":
		return "md5(random()::text)", true
	case column.Type == "jsonb":
		return `'{"benchmark": true}'::jsonb`, true
	}
	return "", false
}
