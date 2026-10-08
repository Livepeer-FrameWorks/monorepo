package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Thresholds a plan is flagged at. A table counts as large above largeTableRows live rows.
const (
	largeTableRows       = 1000
	sortInputRows        = 1000
	topNSortInputRows    = 200
	readAmplification    = 100
	readRequestsBudget   = 50
	subplanLoops         = 100
	nestedLoopInnerLoops = 100
	slowExecutionMillis  = 50.0
)

type finding struct {
	Kind     string  `json:"kind"`
	Relation string  `json:"relation,omitempty"`
	Index    string  `json:"index,omitempty"`
	Rows     float64 `json:"rows,omitempty"`
	Loops    float64 `json:"loops,omitempty"`
	Detail   string  `json:"detail,omitempty"`
}

type metrics struct {
	ExecutionMillis  float64 `json:"execution_ms"`
	PlanningMillis   float64 `json:"planning_ms"`
	RowsReturned     float64 `json:"rows_returned"`
	StorageRowsRead  float64 `json:"storage_rows_scanned"`
	ReadRequests     float64 `json:"storage_read_requests"`
	WriteRequests    float64 `json:"storage_write_requests"`
	FlushRequests    float64 `json:"storage_flush_requests"`
	CatalogReads     float64 `json:"catalog_read_requests"`
	ReadAmplifiction float64 `json:"read_amplification"`
}

type result struct {
	ID        string         `json:"id"`
	Database  string         `json:"database"`
	Layout    string         `json:"layout"`
	Colocated bool           `json:"colocated"`
	Kind      string         `json:"kind"`
	Name      string         `json:"name"`
	Source    string         `json:"source"`
	CallSites []string       `json:"call_sites,omitempty"`
	Status    string         `json:"status"` // analyzed, plan_only, not_audited
	Reason    string         `json:"reason,omitempty"`
	Params    []paramBinding `json:"params,omitempty"`
	Metrics   *metrics       `json:"metrics,omitempty"`
	Findings  []finding      `json:"findings,omitempty"`
	Flags     []string       `json:"flags,omitempty"`
	planFile  string
}

type databaseRun struct {
	Database  string            `json:"database"`
	Layout    string            `json:"layout"`
	Colocated bool              `json:"colocated"`
	Tables    []tableInfo       `json:"tables"`
	Indexes   []indexInfo       `json:"indexes"`
	Seeding   string            `json:"seeding_duration"`
	Duration  string            `json:"duration"`
	Settings  map[string]string `json:"settings"`
}

type tableInfo struct {
	Table     string `json:"table"`
	Seeded    int    `json:"seeded_rows"`
	Deleted   int    `json:"deleted_rows"`
	LiveRows  int64  `json:"live_rows"`
	Tablets   int    `json:"tablets"`
	Colocated bool   `json:"colocated"`
	Queue     bool   `json:"queue_profile"`
}

type indexInfo struct {
	Table      string `json:"table"`
	Index      string `json:"index"`
	Definition string `json:"definition"`
}

func auditDatabase(eng *engine, opts options, svc service, layout string, queries, shared []query) ([]result, databaseRun, error) {
	ctx := context.Background()
	colocated := false
	if layout == "declared" {
		var err error
		if colocated, err = eng.declaredColocated(svc.database); err != nil {
			return nil, databaseRun{}, err
		}
	}
	// The process id keeps a run clear of a database an interrupted run left behind while Yugabyte still drops it.
	name := fmt.Sprintf("explain_audit_%s_%s_%d", svc.database, layout, os.Getpid())
	db, err := eng.createDatabase(svc.database, name, colocated)
	if err != nil {
		return nil, databaseRun{}, err
	}
	defer func() {
		_ = db.Close()
		if cleanupErr := eng.dropDatabase(name); cleanupErr != nil {
			log.Printf("drop %s: %v", name, cleanupErr)
		}
	}()
	run := databaseRun{Database: svc.database, Layout: layout, Colocated: colocated, Settings: map[string]string{}}
	seedStarted := time.Now()
	tables, err := introspect(ctx, db)
	if err != nil {
		return nil, run, fmt.Errorf("introspect: %w", err)
	}
	s := &seeder{db: db, tables: tables, pools: map[string][]string{}, now: time.Now().UTC(), opts: opts, hot: distributedTables(opts.repo, svc.database)}
	s.plan(svc.database)
	if err = s.seedAll(ctx); err != nil {
		return nil, run, err
	}
	run.Seeding = time.Since(seedStarted).Round(time.Second).String()
	log.Printf("%s/%s: seeded %d tables in %s", svc.database, layout, len(tables), run.Seeding)
	if err = describe(ctx, db, s, &run); err != nil {
		return nil, run, err
	}
	dir := filepath.Join(opts.out, layout, svc.database)
	if err = os.MkdirAll(dir, 0o755); err != nil {
		return nil, run, err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, run, err
	}
	defer conn.Close()
	var results []result
	all := append([]query{}, queries...)
	for _, q := range shared {
		q.Database = svc.database
		q.ID = svc.database + ":shared:" + q.Name
		all = append(all, q)
	}
	for _, q := range all {
		if opts.only != "" && !strings.Contains(q.ID, opts.only) {
			continue
		}
		r := explainQuery(ctx, conn, s, opts, svc.database, q)
		if q.Shared && r.Status == "not_audited" {
			// A shared statement belongs to the databases it prepares on.
			continue
		}
		r.Layout, r.Colocated = layout, colocated
		r.planFile = filepath.Join(dir, safeFile(q.Name)+".json")
		if err := writeJSON(r.planFile, struct {
			result
			SQL  string          `json:"sql"`
			Plan json.RawMessage `json:"plan,omitempty"`
		}{r, q.SQL, planCache[q.ID]}); err != nil {
			return nil, run, err
		}
		delete(planCache, q.ID)
		results = append(results, r)
	}
	return results, run, nil
}

var planCache = map[string]json.RawMessage{}

var unsafeFileRe = regexp.MustCompile(`[^A-Za-z0-9_.#-]`)

func safeFile(name string) string { return unsafeFileRe.ReplaceAllString(name, "_") }

func describe(ctx context.Context, db *sql.DB, s *seeder, run *databaseRun) error {
	for _, setting := range []string{"yb_enable_cbo", "yb_enable_base_scans_cost_model", "yb_enable_batchednl", "yb_bnl_batch_size", "yb_fetch_row_limit", "server_version"} {
		var v string
		if err := db.QueryRowContext(ctx, "SELECT current_setting($1)", setting).Scan(&v); err == nil {
			run.Settings[setting] = v
		}
	}
	for _, t := range s.order {
		info := tableInfo{Table: t.qname, Seeded: t.seededRows, Deleted: t.deletedRows, Queue: t.queue}
		if err := db.QueryRowContext(ctx, "SELECT reltuples::bigint FROM pg_class WHERE oid = $1", t.oid).Scan(&info.LiveRows); err != nil {
			return err
		}
		if err := db.QueryRowContext(ctx, "SELECT num_tablets, is_colocated FROM yb_table_properties($1::oid)", t.oid).Scan(&info.Tablets, &info.Colocated); err != nil {
			return err
		}
		run.Tables = append(run.Tables, info)
	}
	rows, err := db.QueryContext(ctx, `SELECT format('%I.%I', n.nspname, t.relname), format('%I.%I', n.nspname, c.relname), pg_get_indexdef(c.oid)
FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid JOIN pg_class t ON t.oid = i.indrelid JOIN pg_namespace n ON n.oid = t.relnamespace
WHERE n.nspname NOT IN ('pg_catalog', 'information_schema') AND n.nspname NOT LIKE 'pg\_%' ORDER BY 1, 2`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var info indexInfo
		if err := rows.Scan(&info.Table, &info.Index, &info.Definition); err != nil {
			return err
		}
		run.Indexes = append(run.Indexes, info)
	}
	return rows.Err()
}

func explainQuery(ctx context.Context, conn *sql.Conn, s *seeder, opts options, database string, q query) (r result) {
	r = result{ID: q.ID, Database: database, Kind: q.Kind, Name: q.Name, Source: q.Source, CallSites: q.CallSites}
	text := strings.ReplaceAll(q.SQL, dynamicMarker, database)
	const stmt = "explain_audit_stmt"
	if _, err := conn.ExecContext(ctx, "DEALLOCATE ALL"); err != nil {
		r.Status, r.Reason = "not_audited", "deallocate failed: "+err.Error()
		return r
	}
	if _, err := conn.ExecContext(ctx, "PREPARE "+stmt+" AS "+text); err != nil {
		r.Status = "not_audited"
		r.Reason = "prepare failed: " + firstLine(err.Error())
		if q.Dynamic {
			r.Reason = "dynamic SQL (" + dynamicMarker + " parts) does not prepare: " + firstLine(err.Error())
		}
		return r
	}
	defer func() {
		if _, err := conn.ExecContext(ctx, "DEALLOCATE ALL"); err != nil {
			r.Status, r.Reason = "not_audited", "deallocate failed: "+err.Error()
		}
	}()
	var types []string
	var typesText string
	if err := conn.QueryRowContext(ctx, "SELECT array_to_string(parameter_types::text[], '|') FROM pg_prepared_statements WHERE name = $1", stmt).Scan(&typesText); err != nil {
		r.Status, r.Reason = "not_audited", "read parameter types: "+firstLine(err.Error())
		return r
	}
	if typesText != "" {
		types = strings.Split(typesText, "|")
	}
	b := &binder{s: s, queryID: q.ID, now: s.now}
	params := b.bind(text, q.ParamNames, types)
	execute := func(ps []paramBinding) string {
		if len(ps) == 0 {
			return "EXECUTE " + stmt
		}
		lits := make([]string, len(ps))
		for i, p := range ps {
			lits[i] = literal(p)
		}
		return "EXECUTE " + stmt + "(" + strings.Join(lits, ", ") + ")"
	}
	// Sampled values first; then type-only synthetic values, since a sampled value can violate a constraint of a
	// write; then synthetic text as a UUID and as a number, for text parameters the statement casts further.
	variants := [][]paramBinding{params}
	for _, textValue := range []string{"", "uuid", "60"} {
		fallback := make([]paramBinding, len(params))
		for i, p := range params {
			v := b.synthetic(p.Type, nameAt(q.ParamNames, i))
			fallback[i] = paramBinding{Index: p.Index, Type: p.Type, Source: "synthetic (retry)", Value: v.value, Null: v.null}
			if textValue != "" && (p.Type == "text" || strings.HasPrefix(p.Type, "character")) {
				fallback[i].Value = textValue
				if textValue == "uuid" {
					fallback[i].Value = uuidFrom(rand.New(rand.NewSource(int64(i))))
				}
			}
		}
		variants = append(variants, fallback)
	}
	var plan json.RawMessage
	var err, firstErr error
	for _, variant := range variants {
		if plan, err = runExplain(ctx, conn, "EXPLAIN (ANALYZE, DIST, FORMAT JSON) "+execute(variant), opts.queryTimeout); err == nil {
			params = variant
			break
		}
		if firstErr == nil {
			firstErr = err
		}
		if isTimeout(err) {
			break
		}
	}
	r.Params = params
	if err != nil {
		reason := firstLine(firstErr.Error())
		var planOnly json.RawMessage
		var perr error
		for _, variant := range variants {
			if planOnly, perr = runExplain(ctx, conn, "EXPLAIN (FORMAT JSON) "+execute(variant), opts.queryTimeout); perr == nil {
				r.Params = variant
				break
			}
		}
		if perr != nil {
			r.Status, r.Reason = "not_audited", "execute failed: "+reason+"; plan failed: "+firstLine(perr.Error())
			return r
		}
		r.Status, r.Reason = "plan_only", "execute failed: "+reason
		if isTimeout(firstErr) {
			r.addFinding(finding{Kind: "TIMEOUT", Detail: fmt.Sprintf("did not finish within %s", opts.queryTimeout)})
		}
		planCache[q.ID] = planOnly
		analyzePlanShape(planOnly, s, &r)
		return r
	}
	r.Status = "analyzed"
	planCache[q.ID] = plan
	analyze(plan, s, &r)
	return r
}

// runExplain explains inside a transaction that is always rolled back, twice, keeping the second plan: the first
// execution in a session also pays catalog reads that a long-lived connection pool does not.
func runExplain(ctx context.Context, conn *sql.Conn, statement string, timeout time.Duration) (json.RawMessage, error) {
	var out json.RawMessage
	for attempt := 0; attempt < 2; attempt++ {
		raw, err := runExplainAttempt(ctx, conn, statement, timeout)
		if err != nil {
			return nil, err
		}
		out = raw
	}
	return out, nil
}

func runExplainAttempt(ctx context.Context, conn *sql.Conn, statement string, timeout time.Duration) (out json.RawMessage, err error) {
	if _, err = conn.ExecContext(ctx, "BEGIN"); err != nil {
		return nil, err
	}
	defer func() {
		_, rollbackErr := conn.ExecContext(ctx, "ROLLBACK")
		_, unlockErr := conn.ExecContext(ctx, "SELECT pg_advisory_unlock_all()")
		err = errors.Join(err, rollbackErr, unlockErr)
	}()
	if _, err = conn.ExecContext(ctx, fmt.Sprintf("SET LOCAL statement_timeout = %d", timeout.Milliseconds())); err != nil {
		return nil, err
	}
	var text string
	err = conn.QueryRowContext(ctx, statement).Scan(&text)
	return json.RawMessage(text), err
}

func isTimeout(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "statement timeout") || strings.Contains(err.Error(), "canceling statement"))
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

type planNode struct {
	NodeType      string     `json:"Node Type"`
	Relationship  string     `json:"Parent Relationship"`
	SubplanName   string     `json:"Subplan Name"`
	Relation      string     `json:"Relation Name"`
	Schema        string     `json:"Schema"`
	Alias         string     `json:"Alias"`
	Index         string     `json:"Index Name"`
	ActualRows    float64    `json:"Actual Rows"`
	ActualLoops   float64    `json:"Actual Loops"`
	PlanRows      float64    `json:"Plan Rows"`
	TableRowsRead float64    `json:"Storage Table Rows Scanned"`
	IndexRowsRead float64    `json:"Storage Index Rows Scanned"`
	TableReads    float64    `json:"Storage Table Read Requests"`
	IndexReads    float64    `json:"Storage Index Read Requests"`
	SortKey       []string   `json:"Sort Key"`
	StorageFilter string     `json:"Storage Filter"`
	Filter        string     `json:"Filter"`
	RowsRemoved   float64    `json:"Rows Removed by Filter"`
	IndexCond     string     `json:"Index Cond"`
	JoinType      string     `json:"Join Type"`
	Plans         []planNode `json:"Plans"`
}

type planRoot struct {
	Plan          planNode `json:"Plan"`
	Planning      float64  `json:"Planning Time"`
	Execution     float64  `json:"Execution Time"`
	ReadRequests  float64  `json:"Storage Read Requests"`
	RowsScanned   float64  `json:"Storage Rows Scanned"`
	WriteRequests float64  `json:"Storage Write Requests"`
	FlushRequests float64  `json:"Storage Flush Requests"`
	CatalogReads  float64  `json:"Catalog Read Requests"`
}

func (s *seeder) liveRows(relation string) float64 {
	for _, t := range s.tables {
		if t.name == relation {
			return float64(t.seededRows - t.deletedRows)
		}
	}
	return 0
}

func analyze(raw json.RawMessage, s *seeder, r *result) {
	var roots []planRoot
	if err := json.Unmarshal(raw, &roots); err != nil || len(roots) == 0 {
		r.Reason = "unparseable plan"
		return
	}
	root := roots[0]
	m := &metrics{
		ExecutionMillis: root.Execution, PlanningMillis: root.Planning, StorageRowsRead: root.RowsScanned,
		ReadRequests: root.ReadRequests, WriteRequests: root.WriteRequests, FlushRequests: root.FlushRequests, CatalogReads: root.CatalogReads,
	}
	m.RowsReturned = returnedRows(root.Plan)
	m.ReadAmplifiction = root.RowsScanned / maxf(m.RowsReturned, 1)
	r.Metrics = m
	walk(root.Plan, 1, s, r, true)
	if root.RowsScanned >= largeTableRows && m.ReadAmplifiction >= readAmplification {
		r.addFinding(finding{Kind: "READ_AMPLIFICATION", Rows: root.RowsScanned, Detail: fmt.Sprintf("%.0f rows scanned for %.0f returned", root.RowsScanned, m.RowsReturned)})
	}
	if root.ReadRequests >= readRequestsBudget {
		r.addFinding(finding{Kind: "MANY_READ_RPCS", Rows: root.ReadRequests, Detail: fmt.Sprintf("%.0f storage read requests", root.ReadRequests)})
	}
	if root.Execution >= slowExecutionMillis {
		r.addFinding(finding{Kind: "SLOW", Detail: fmt.Sprintf("%.1f ms", root.Execution)})
	}
}

// analyzePlanShape flags what an unexecuted plan already shows: a sequential scan of a large table and a sort.
func analyzePlanShape(raw json.RawMessage, s *seeder, r *result) {
	var roots []planRoot
	if err := json.Unmarshal(raw, &roots); err != nil || len(roots) == 0 {
		return
	}
	walk(roots[0].Plan, 1, s, r, false)
}

// returnedRows counts the rows the statement produced; for a write without RETURNING, the rows it changed.
func returnedRows(n planNode) float64 {
	if n.NodeType == "ModifyTable" && n.ActualRows == 0 && len(n.Plans) > 0 {
		return n.Plans[0].ActualRows * maxf(n.Plans[0].ActualLoops, 1)
	}
	return n.ActualRows * maxf(n.ActualLoops, 1)
}

func walk(n planNode, _ float64, s *seeder, r *result, executed bool) {
	loops := maxf(n.ActualLoops, 1)
	switch n.NodeType {
	case "Seq Scan", "YB Seq Scan":
		live := s.liveRows(n.Relation)
		if live > largeTableRows {
			scanned := n.TableRowsRead * loops
			if !executed {
				scanned = live
			}
			r.addFinding(finding{Kind: "SEQ_SCAN_LARGE", Relation: n.Relation, Rows: scanned, Loops: loops,
				Detail: fmt.Sprintf("table has %.0f live rows; filter %s", live, firstNonEmpty(n.StorageFilter, n.Filter))})
		}
	case "Sort", "Incremental Sort":
		if len(n.Plans) > 0 {
			input := n.Plans[0].ActualRows * maxf(n.Plans[0].ActualLoops, 1)
			if !executed {
				input = n.Plans[0].PlanRows
			}
			output := n.ActualRows * loops
			if !executed {
				output = n.PlanRows
			}
			switch {
			case input >= sortInputRows:
				r.addFinding(finding{Kind: "SORT_LARGE", Rows: input, Detail: "sort keys " + strings.Join(n.SortKey, ", ")})
			case input >= topNSortInputRows && input >= 4*output:
				// ORDER BY ... LIMIT that no index order serves: every call reads and sorts every matching row, so
				// its cost grows with the backlog rather than with the limit.
				r.addFinding(finding{Kind: "SORT_FEEDS_LIMIT", Rows: input, Detail: fmt.Sprintf("sorted %.0f rows to keep %.0f; sort keys %s", input, output, strings.Join(n.SortKey, ", "))})
			}
		}
	case "Nested Loop":
		if len(n.Plans) > 1 && n.Plans[1].ActualLoops >= nestedLoopInnerLoops {
			r.addFinding(finding{Kind: "NESTED_LOOP_UNBATCHED", Relation: innerRelation(n.Plans[1]), Loops: n.Plans[1].ActualLoops,
				Detail: fmt.Sprintf("inner side executed %.0f times", n.Plans[1].ActualLoops)})
		}
	}
	if n.Relationship == "SubPlan" && n.ActualLoops >= subplanLoops {
		r.addFinding(finding{Kind: "CORRELATED_SUBPLAN", Relation: innerRelation(n), Loops: n.ActualLoops,
			Detail: fmt.Sprintf("%s executed %.0f times", n.SubplanName, n.ActualLoops)})
	}
	if (n.Index != "") && executed {
		read := (n.TableRowsRead + n.IndexRowsRead) * loops
		got := n.ActualRows * loops
		if read >= largeTableRows && read/maxf(got, 1) >= readAmplification {
			r.addFinding(finding{Kind: "INDEX_SCAN_AMPLIFICATION", Relation: n.Relation, Index: n.Index, Rows: read,
				Detail: fmt.Sprintf("%.0f rows read through %s for %.0f kept; cond %s; filter %s", read, n.Index, got, n.IndexCond, firstNonEmpty(n.StorageFilter, n.Filter))})
		}
	}
	for _, child := range n.Plans {
		walk(child, loops, s, r, executed)
	}
}

func innerRelation(n planNode) string {
	if n.Relation != "" {
		return n.Relation
	}
	for _, c := range n.Plans {
		if rel := innerRelation(c); rel != "" {
			return rel
		}
	}
	return ""
}

func (r *result) addFinding(f finding) {
	r.Findings = append(r.Findings, f)
	for _, existing := range r.Flags {
		if existing == f.Kind {
			return
		}
	}
	r.Flags = append(r.Flags, f.Kind)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return "none"
}

func maxf(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}
