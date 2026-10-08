package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lib/pq"
)

// The seeder fills every table of a baseline with synthetic rows shaped like production: high-volume tables
// (queues, outboxes, events, ledgers and the tables a layout file places distributed) get many rows, queues are
// mostly settled with a small pending head, and a share of settled queue rows is deleted so the scans the audit
// explains walk DocDB tombstones as production's do. Foreign keys, and columns that share a name with another
// table's unique key, draw from the parent's seeded values with a skew so a few parents own most children, the
// shape tenants and clusters have. Triggers and foreign-key enforcement are off while seeding
// (session_replication_role = replica) and CHECK constraints are dropped, so generic values never stop a load,
// while values named by a CHECK's IN list stay inside it. The checks stay dropped for the audit: a NOT VALID check
// is still enforced on every row an explained UPDATE touches, which a generic seeded row would fail, and no plan
// the audit reads depends on a CHECK.

type column struct {
	name        string
	format      string // format_type of the column (domains resolved to their base type)
	notNull     bool
	defaultExpr string
	skip        bool // identity, sequence default or generated: the engine fills it
	enumLabels  []string
	checkValues []string
	maxLen      int
	isArray     bool
	elemFormat  string
}

type foreignKey struct {
	columns       []string
	parent        string
	parentColumns []string
	soft          bool
}

type table struct {
	oid         int64
	qname       string
	name        string
	columns     []*column
	byName      map[string]*column
	uniqueSets  [][]string
	fks         []foreignKey
	checks      []checkConstraint
	rows        int
	queue       bool
	parentVals  map[string][][]string // fk key -> sampled parent tuples
	uniqueCols  map[string]bool
	fkColumns   map[string]int // column -> fk index
	seededRows  int
	deletedRows int
}

type checkConstraint struct {
	name, def string
}

var (
	checkInRe     = regexp.MustCompile(`\(?\(?([a-z_][a-z0-9_]*)\)?(?:::[a-z ]+)?\s*=\s*ANY\s*\(\(?ARRAY\[([^\]]*)\]`)
	checkEqRe     = regexp.MustCompile(`\(([a-z_][a-z0-9_]*)\s*=\s*'([^']*)'`)
	quotedRe      = regexp.MustCompile(`'((?:[^']|'')*)'`)
	lenRe         = regexp.MustCompile(`\((\d+)(?:,(\d+))?\)`)
	queueNameRe   = regexp.MustCompile(`(outbox|_queue|queue_|_jobs?$|_inbox|deliver|attempt|_intents?$|_commands?$|dispatch|retries|pending)`)
	hotNameRe     = regexp.MustCompile(`(event|log|history|usage|ledger|session|sample|metric|audit|replay|record|entr|line_item|transaction|snapshot|_runs?$|messages|artifact|placement|incident|invoice|webhook)`)
	settledColRe  = regexp.MustCompile(`^(completed|published|processed|delivered|sent|dispatched|acked|applied|finished|succeeded|settled|resolved|done|consumed|handled)_at$`)
	dueColRe      = regexp.MustCompile(`^(next_attempt|next_retry|available|run|scheduled|due|not_before|retry|visible|next_run|next_check|poll)_(at|after)$`)
	claimColRe    = regexp.MustCompile(`^(claimed|locked|leased|lease_expires|locked_until|lease_until|claim_expires|lease_expiry|lease_deadline|picked)(_at)?$`)
	recentColRe   = regexp.MustCompile(`^last_.*(heartbeat|seen|check|health|report|ping|observed|alive|contact)`)
	pendingValues = regexp.MustCompile(`^(pending|queued|claimed|running|in_progress|processing|retry|retrying|new|open|requested|waiting|scheduled|starting|leased|active_pending|provisioning|initializing|delivering|sending|dispatching|in_flight|publishing|uploading|verifying|assigned|confirming|leasing|applying|activating|awaiting|blocked)$`)
	rareValues    = regexp.MustCompile(`^(failed|rejected|error|errored|dead|dead_letter|abandoned|quarantined|cancelled|canceled|expired|failed_retryable|failed_permanent|review_required|manual_review|overdue|disputed|refunded|reversed|parked)$`)
)

// smallTables are catalogs that stay small in production; they get a few rows so a Seq Scan over them is not
// reported as a large scan.
var smallTables = map[string]int{}

func loadVolumeOverrides(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return fmt.Errorf("%s:%d: want <schema.table> <rows>", path, i+1)
		}
		n, err := strconv.Atoi(fields[1])
		if err != nil {
			return fmt.Errorf("%s:%d: %w", path, i+1, err)
		}
		smallTables[fields[0]] = n
	}
	return nil
}

// distributedTables reads the distributed_tables list of a layout file: the tables its owner classified as
// growing with traffic.
func distributedTables(repo, database string) map[string]bool {
	out := map[string]bool{}
	data, err := os.ReadFile(filepath.Join(repo, "pkg/database/sql/layout", database+".yaml"))
	if err != nil {
		return out
	}
	section := ""
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasSuffix(trimmed, ":") && !strings.HasPrefix(trimmed, "-") {
			section = strings.TrimSuffix(trimmed, ":")
			continue
		}
		if strings.HasPrefix(trimmed, "- ") && (section == "distributed_tables" || section == "write_rate_benchmark") {
			out[strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))] = true
		}
	}
	return out
}

type seeder struct {
	db      *sql.DB
	tables  map[string]*table
	order   []*table
	pools   map[string][]string
	now     time.Time
	opts    options
	hot     map[string]bool
	samples map[string][]map[string]*string
}

func introspect(ctx context.Context, db *sql.DB) (map[string]*table, error) {
	tables := map[string]*table{}
	byOID := map[int64]*table{}
	rows, err := db.QueryContext(ctx, `SELECT c.oid::bigint, format('%I.%I', n.nspname, c.relname), c.relname
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind IN ('r', 'p') AND NOT c.relispartition
  AND n.nspname NOT IN ('pg_catalog', 'information_schema') AND n.nspname NOT LIKE 'pg\_%'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		t := &table{byName: map[string]*column{}, parentVals: map[string][][]string{}, uniqueCols: map[string]bool{}, fkColumns: map[string]int{}}
		if err = rows.Scan(&t.oid, &t.qname, &t.name); err != nil {
			return nil, err
		}
		tables[t.qname] = t
		byOID[t.oid] = t
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	rows, err = db.QueryContext(ctx, `SELECT a.attrelid::bigint, a.attname,
  CASE WHEN t.typtype = 'd' THEN format_type(t.typbasetype, t.typtypmod) ELSE format_type(a.atttypid, a.atttypmod) END,
  a.attnotnull, COALESCE(pg_get_expr(d.adbin, d.adrelid), ''), a.attidentity <> '' OR a.attgenerated <> '',
  COALESCE((SELECT array_agg(e.enumlabel ORDER BY e.enumsortorder) FROM pg_enum e WHERE e.enumtypid = a.atttypid), '{}')
FROM pg_attribute a
JOIN pg_type t ON t.oid = a.atttypid
LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
WHERE a.attnum > 0 AND NOT a.attisdropped AND a.attrelid = ANY($1)
ORDER BY a.attrelid, a.attnum`, pq.Array(oids(byOID)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var relid int64
		c := &column{}
		var labels []string
		if err = rows.Scan(&relid, &c.name, &c.format, &c.notNull, &c.defaultExpr, &c.skip, pq.Array(&labels)); err != nil {
			return nil, err
		}
		c.enumLabels = labels
		if strings.Contains(c.defaultExpr, "nextval(") {
			c.skip = true
		}
		if strings.HasSuffix(c.format, "[]") {
			c.isArray = true
			c.elemFormat = strings.TrimSuffix(c.format, "[]")
		}
		if m := lenRe.FindStringSubmatch(c.format); m != nil && (strings.HasPrefix(c.format, "character") || strings.HasPrefix(c.format, "bit")) {
			c.maxLen = catalogInteger(m[1])
		}
		t := byOID[relid]
		t.columns = append(t.columns, c)
		t.byName[c.name] = c
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	rows, err = db.QueryContext(ctx, `SELECT con.conrelid::bigint, con.contype::text, con.conname, pg_get_constraintdef(con.oid),
  ARRAY(SELECT a.attname::text FROM unnest(con.conkey) WITH ORDINALITY k(n, o) JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.n ORDER BY k.o),
  COALESCE(con.confrelid::bigint, 0),
  ARRAY(SELECT a.attname::text FROM unnest(COALESCE(con.confkey, '{}')) WITH ORDINALITY k(n, o) JOIN pg_attribute a ON a.attrelid = con.confrelid AND a.attnum = k.n ORDER BY k.o)
FROM pg_constraint con WHERE con.conrelid = ANY($1) AND con.contype IN ('p', 'u', 'f', 'c')
ORDER BY con.conrelid, con.contype <> 'p', con.conname`, pq.Array(oids(byOID)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var relid, parent int64
		var kind, name, def string
		var cols, parentCols []string
		if err = rows.Scan(&relid, &kind, &name, &def, pq.Array(&cols), &parent, pq.Array(&parentCols)); err != nil {
			return nil, err
		}
		t := byOID[relid]
		switch kind {
		case "p", "u":
			t.uniqueSets = append(t.uniqueSets, cols)
		case "f":
			if p, ok := byOID[parent]; ok {
				t.fks = append(t.fks, foreignKey{columns: cols, parent: p.qname, parentColumns: parentCols})
			}
		case "c":
			t.checks = append(t.checks, checkConstraint{name: name, def: def})
			for _, m := range checkInRe.FindAllStringSubmatch(def, -1) {
				if c, ok := t.byName[m[1]]; ok && c.checkValues == nil {
					for _, q := range quotedRe.FindAllStringSubmatch(m[2], -1) {
						c.checkValues = append(c.checkValues, strings.ReplaceAll(q[1], "''", "'"))
					}
				}
			}
			for _, m := range checkEqRe.FindAllStringSubmatch(def, -1) {
				if c, ok := t.byName[m[1]]; ok && c.checkValues == nil && !strings.Contains(def, " OR ") {
					c.checkValues = []string{m[2]}
				}
			}
		}
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	rows, err = db.QueryContext(ctx, `SELECT i.indrelid::bigint,
  ARRAY(SELECT a.attname::text FROM unnest(i.indkey::int2[]) WITH ORDINALITY k(n, o) JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.n ORDER BY k.o),
  0 = ANY(i.indkey::int2[])
FROM pg_index i WHERE i.indisunique AND NOT i.indisprimary AND i.indrelid = ANY($1)
ORDER BY i.indrelid, i.indexrelid::regclass::text`, pq.Array(oids(byOID)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var relid int64
		var cols []string
		var expression bool
		if err = rows.Scan(&relid, pq.Array(&cols), &expression); err != nil {
			return nil, err
		}
		if !expression {
			byOID[relid].uniqueSets = append(byOID[relid].uniqueSets, cols)
		}
	}
	return tables, rows.Err()
}

func oids(byOID map[int64]*table) []int64 {
	out := make([]int64, 0, len(byOID))
	for oid := range byOID {
		out = append(out, oid)
	}
	return out
}

// plan decides volumes, soft references and the seeding order.
func (s *seeder) plan(database string) {
	dimension := map[string][]string{} // column name -> tables where it alone is unique
	for _, t := range s.tables {
		for _, set := range t.uniqueSets {
			if len(set) == 1 {
				t.uniqueCols[set[0]] = true
				dimension[set[0]] = append(dimension[set[0]], t.qname)
			}
		}
	}
	for _, list := range dimension {
		sort.Strings(list)
	}
	schema := strings.SplitN(sortedKeys(s.tables)[0], ".", 2)[0]
	for _, t := range s.tables {
		for i, fk := range t.fks {
			for _, c := range fk.columns {
				t.fkColumns[c] = i
			}
		}
		for _, c := range t.columns {
			if c.skip {
				continue
			}
			if _, ok := t.fkColumns[c.name]; ok {
				continue
			}
			if t.uniqueCols[c.name] {
				continue
			}
			// A column named like another table's sole unique key references it (stream_id, tenant_id...), or
			// <entity>_id references <entity>s.id.
			var parent, parentCol string
			for _, cand := range dimension[c.name] {
				if cand != t.qname {
					parent, parentCol = cand, c.name
					break
				}
			}
			if parent == "" && strings.HasSuffix(c.name, "_id") {
				entity := strings.TrimSuffix(c.name, "_id")
				for _, suffix := range []string{"s", "es", ""} {
					cand := schema + "." + entity + suffix
					if p, ok := s.tables[cand]; ok && p.qname != t.qname && p.uniqueCols["id"] {
						parent, parentCol = cand, "id"
						break
					}
				}
			}
			if parent == "" {
				continue
			}
			pc := s.tables[parent].byName[parentCol]
			if pc == nil || !compatibleTypes(c.format, pc.format) {
				continue
			}
			t.fks = append(t.fks, foreignKey{columns: []string{c.name}, parent: parent, parentColumns: []string{parentCol}, soft: true})
			t.fkColumns[c.name] = len(t.fks) - 1
		}
		t.queue = queueNameRe.MatchString(t.name)
		for _, c := range t.columns {
			if dueColRe.MatchString(c.name) || claimColRe.MatchString(c.name) {
				t.queue = true
			}
		}
		switch {
		case strings.HasPrefix(t.qname, "public."):
			// The baseline's own bookkeeping (public._schema_baseline) keeps the rows the baseline wrote.
			t.rows = 0
			t.queue = false
		case smallTables[t.qname] > 0:
			t.rows = smallTables[t.qname]
		case t.queue || s.hot[t.qname] || hotNameRe.MatchString(t.name):
			t.rows = s.opts.hotRows
		default:
			t.rows = s.opts.defaultRows
		}
	}
	// Parents before children; a cycle is broken in name order and its late parent falls back to pooled values.
	indegree := map[string]int{}
	children := map[string][]string{}
	for _, name := range sortedKeys(s.tables) {
		t := s.tables[name]
		parents := map[string]bool{}
		for _, fk := range t.fks {
			if fk.parent != t.qname {
				parents[fk.parent] = true
			}
		}
		indegree[name] = len(parents)
		for p := range parents {
			children[p] = append(children[p], name)
		}
	}
	done := map[string]bool{}
	for len(done) < len(s.tables) {
		progressed := false
		for _, name := range sortedKeys(s.tables) {
			if done[name] || indegree[name] > 0 {
				continue
			}
			done[name] = true
			progressed = true
			s.order = append(s.order, s.tables[name])
			for _, child := range children[name] {
				indegree[child]--
			}
		}
		if !progressed {
			for _, name := range sortedKeys(s.tables) {
				if !done[name] {
					indegree[name] = 0
					break
				}
			}
		}
	}
	_ = database
}

func compatibleTypes(child, parent string) bool {
	norm := func(f string) string {
		switch {
		case strings.HasPrefix(f, "character varying"), f == "text", f == "citext", strings.HasPrefix(f, "character("):
			return "text"
		case f == "integer", f == "bigint", f == "smallint":
			return "int"
		}
		return f
	}
	c, p := norm(child), norm(parent)
	return c == p || (c == "text" && p == "uuid")
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// seedAll loads every table, deletes most settled queue rows and leaves the database analyzed.
func (s *seeder) seedAll(ctx context.Context) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "SET session_replication_role = replica"); err != nil {
		return fmt.Errorf("disable triggers: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "SET statement_timeout = 0"); err != nil {
		return err
	}
	for _, t := range s.order {
		for _, check := range t.checks {
			if _, err := conn.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %s", t.qname, pq.QuoteIdentifier(check.name))); err != nil {
				return fmt.Errorf("drop check %s on %s: %w", check.name, t.qname, err)
			}
		}
	}
	for _, t := range s.order {
		if err := s.seedTable(ctx, conn, t); err != nil {
			return fmt.Errorf("seed %s: %w", t.qname, err)
		}
	}
	for _, t := range s.order {
		if !t.queue || t.seededRows == 0 {
			continue
		}
		// Rows are chosen by a hash of their key, not random(), so every run deletes the same rows.
		pick := "random()"
		if len(t.uniqueSets) > 0 {
			keys := make([]string, len(t.uniqueSets[0]))
			for i, k := range t.uniqueSets[0] {
				keys[i] = pq.QuoteIdentifier(k) + "::text"
			}
			pick = fmt.Sprintf("(abs(hashtext(concat_ws('|', %s))::bigint) %% 1000) / 1000.0", strings.Join(keys, ", "))
		}
		predicate := pick + " < 0.5"
		for _, c := range t.columns {
			if settledColRe.MatchString(c.name) {
				predicate = fmt.Sprintf("%s IS NOT NULL AND %s < 0.6", pq.QuoteIdentifier(c.name), pick)
				break
			}
		}
		res, err := conn.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE %s", t.qname, predicate))
		if err != nil {
			return fmt.Errorf("delete settled rows of %s: %w", t.qname, err)
		}
		n, affectedErr := res.RowsAffected()
		if affectedErr != nil {
			return affectedErr
		}
		t.deletedRows = int(n)
	}
	if _, err := conn.ExecContext(ctx, "SET session_replication_role = DEFAULT"); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "ANALYZE"); err != nil {
		return fmt.Errorf("analyze: %w", err)
	}
	return s.sample(ctx, conn)
}

func (s *seeder) seedTable(ctx context.Context, conn *sql.Conn, t *table) error {
	if t.rows == 0 {
		return nil
	}
	var cols []*column
	for _, c := range t.columns {
		if !c.skip {
			cols = append(cols, c)
		}
	}
	if len(cols) == 0 {
		return nil
	}
	// Parent tuples for each foreign key, loaded from the parent's seeded rows.
	for i, fk := range t.fks {
		key := fmt.Sprint(i)
		parent := s.tables[fk.parent]
		if parent == nil || parent.seededRows == 0 && parent != t {
			continue
		}
		if parent == t {
			continue
		}
		var exprs []string
		for _, pc := range fk.parentColumns {
			exprs = append(exprs, pq.QuoteIdentifier(pc)+"::text")
		}
		// Ordered, so the skewed picks below choose the same parents on every run and the budget compares like
		// with like.
		rows, err := conn.QueryContext(ctx, fmt.Sprintf("SELECT %s FROM %s ORDER BY 1 LIMIT 50000", strings.Join(exprs, ", "), fk.parent))
		if err != nil {
			return fmt.Errorf("load parent %s: %w", fk.parent, err)
		}
		for rows.Next() {
			vals := make([]sql.NullString, len(exprs))
			ptrs := make([]any, len(exprs))
			for j := range vals {
				ptrs[j] = &vals[j]
			}
			if err := rows.Scan(ptrs...); err != nil {
				_ = rows.Close() //nolint:sqlclosecheck // Close this iteration before returning its scan error.
				return err
			}
			tuple := make([]string, len(vals))
			ok := true
			for j, v := range vals {
				if !v.Valid {
					ok = false
				}
				tuple[j] = v.String
			}
			if ok {
				t.parentVals[key] = append(t.parentVals[key], tuple)
			}
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	quoted := make([]string, len(cols))
	selects := make([]string, len(cols))
	unnests := make([]string, len(cols))
	for i, c := range cols {
		quoted[i] = pq.QuoteIdentifier(c.name)
		selects[i] = fmt.Sprintf("u.c%d::%s", i, c.format)
		unnests[i] = fmt.Sprintf("$%d::text[]", i+1)
	}
	aliases := make([]string, len(cols))
	for i := range cols {
		aliases[i] = fmt.Sprintf("c%d", i)
	}
	insert := fmt.Sprintf("INSERT INTO %s (%s) SELECT %s FROM unnest(%s) AS u(%s) ON CONFLICT DO NOTHING",
		t.qname, strings.Join(quoted, ", "), strings.Join(selects, ", "), strings.Join(unnests, ", "), strings.Join(aliases, ", "))
	batch := 1000
	if t.byName["embedding"] != nil {
		batch = 50
	}
	rng := rand.New(rand.NewSource(int64(hashString(t.qname))))
	for start := 0; start < t.rows; start += batch {
		end := min(start+batch, t.rows)
		columns := make([][]*string, len(cols))
		for i := start; i < end; i++ {
			fkTuples := map[int][]string{}
			for ci, c := range cols {
				var v *string
				if fi, ok := t.fkColumns[c.name]; ok {
					tuple, has := fkTuples[fi]
					if !has {
						vals := t.parentVals[fmt.Sprint(fi)]
						if len(vals) > 0 {
							tuple = vals[skewedIndex(rng, len(vals))]
						}
						fkTuples[fi] = tuple
					}
					if tuple != nil {
						fk := t.fks[fi]
						for k, name := range fk.columns {
							if name == c.name {
								val := tuple[k]
								v = &val
							}
						}
					}
					if v == nil {
						v = s.generate(rng, t, c, i)
					}
				} else {
					v = s.generate(rng, t, c, i)
				}
				columns[ci] = append(columns[ci], v)
			}
		}
		args := make([]any, len(cols))
		for ci := range cols {
			args[ci] = arrayLiteral(columns[ci])
		}
		res, err := conn.ExecContext(ctx, insert, args...)
		if err != nil {
			return fmt.Errorf("insert batch: %w", err)
		}
		n, affectedErr := res.RowsAffected()
		if affectedErr != nil {
			return affectedErr
		}
		t.seededRows += int(n)
	}
	return nil
}

func skewedIndex(rng *rand.Rand, n int) int {
	return min(n-1, int(float64(n)*math.Pow(rng.Float64(), 2.5)))
}

func hashString(s string) uint64 {
	sum := sha256.Sum256([]byte(s))
	return binary.BigEndian.Uint64(sum[:8])
}

func arrayLiteral(values []*string) string {
	var b strings.Builder
	b.WriteByte('{')
	for i, v := range values {
		if i > 0 {
			b.WriteByte(',')
		}
		if v == nil {
			b.WriteString("NULL")
			continue
		}
		b.WriteByte('"')
		b.WriteString(strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(*v))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}

func strp(s string) *string { return &s }

func (s *seeder) pool(c *column, size int) []string {
	key := c.name
	if values, ok := s.pools[key]; ok {
		return values
	}
	rng := rand.New(rand.NewSource(int64(hashString("pool:" + key))))
	values := make([]string, size)
	for i := range values {
		values[i] = s.textValue(rng, c, i, true)
	}
	s.pools[key] = values
	return values
}

func poolSize(name string, rows int) int {
	switch name {
	case "tenant_id", "owner_tenant_id", "billing_tenant_id":
		return 50
	case "cluster_id", "origin_cluster_id", "cluster_name", "region", "cell_id", "control_cell_id":
		return 8
	case "node_id", "origin_node_id", "node_name", "hostname", "host":
		return 64
	}
	return max(8, rows/25)
}

func uuidFrom(rng *rand.Rand) string {
	b := make([]byte, 16)
	rng.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func (s *seeder) textValue(rng *rand.Rand, c *column, i int, pooled bool) string {
	name := c.name
	var v string
	switch {
	case strings.HasSuffix(name, "_id") || name == "id" || c.format == "uuid" || strings.HasSuffix(name, "_uuid"):
		v = uuidFrom(rng)
	case strings.Contains(name, "email"):
		v = fmt.Sprintf("user%d-%d@example.test", i, rng.Intn(1e6))
	case strings.Contains(name, "url") || strings.Contains(name, "endpoint") || strings.Contains(name, "uri"):
		v = fmt.Sprintf("https://h%d.example.test/p/%d", rng.Intn(500), i)
	case strings.Contains(name, "hash") || strings.Contains(name, "digest") || strings.Contains(name, "sha") || strings.Contains(name, "fingerprint") || strings.Contains(name, "signature"):
		b := make([]byte, 32)
		rng.Read(b)
		v = hex.EncodeToString(b)
	case strings.Contains(name, "currency"):
		v = []string{"EUR", "USD", "GBP"}[rng.Intn(3)]
	case strings.Contains(name, "country"):
		v = []string{"NL", "DE", "US", "GB"}[rng.Intn(4)]
	case name == "status" || name == "state" || strings.HasSuffix(name, "_status") || strings.HasSuffix(name, "_state"):
		v = []string{"active", "completed", "completed", "completed", "failed", "pending", "running", "deleted"}[rng.Intn(8)]
	case strings.Contains(name, "ip") && (strings.HasSuffix(name, "_ip") || name == "ip" || strings.Contains(name, "address")):
		v = fmt.Sprintf("10.%d.%d.%d", rng.Intn(256), rng.Intn(256), 1+rng.Intn(254))
	default:
		v = fmt.Sprintf("%s-%d-%04x", name, i, rng.Intn(65536))
	}
	if c.maxLen > 0 && len(v) > c.maxLen {
		if strings.HasPrefix(c.format, "character(") || strings.HasPrefix(c.format, "character varying") {
			b := make([]byte, c.maxLen)
			for j := range b {
				b[j] = "0123456789abcdef"[rng.Intn(16)]
			}
			if c.maxLen == 3 {
				return []string{"EUR", "USD", "GBP"}[rng.Intn(3)]
			}
			v = string(b)
		} else {
			v = v[:c.maxLen]
		}
	}
	return v
}

// generate returns one value of column c for row i, or nil for NULL.
func (s *seeder) generate(rng *rand.Rand, t *table, c *column, i int) *string {
	name := c.name
	nullable := !c.notNull
	if len(c.checkValues) > 0 {
		return strp(pickSkewed(rng, c.checkValues, t.queue))
	}
	if len(c.enumLabels) > 0 {
		return strp(pickSkewed(rng, c.enumLabels, t.queue))
	}
	if c.isArray {
		if rng.Intn(3) == 0 {
			return strp("{}")
		}
		elem := &column{name: strings.TrimSuffix(name, "s"), format: c.elemFormat}
		n := 1 + rng.Intn(3)
		parts := make([]string, n)
		for k := range parts {
			parts[k] = `"` + *s.scalar(rng, t, elem, i, true) + `"`
		}
		return strp("{" + strings.Join(parts, ",") + "}")
	}
	f := c.format
	if strings.Contains(f, "timestamp") || f == "date" {
		switch {
		case settledColRe.MatchString(name):
			if nullable && rng.Float64() < 0.05 {
				return nil
			}
		case claimColRe.MatchString(name):
			if nullable && rng.Float64() < 0.9 {
				return nil
			}
		case strings.HasPrefix(name, "deleted") || strings.HasPrefix(name, "revoked") || strings.HasPrefix(name, "archived") || strings.HasPrefix(name, "disabled") || strings.HasPrefix(name, "suspended") || strings.HasPrefix(name, "purged") || strings.HasPrefix(name, "canceled") || strings.HasPrefix(name, "cancelled") || strings.HasPrefix(name, "failed") || strings.HasPrefix(name, "ended") || strings.HasPrefix(name, "superseded") || strings.HasPrefix(name, "retired"):
			if nullable && rng.Float64() < 0.9 {
				return nil
			}
		default:
			if nullable && rng.Float64() < 0.2 {
				return nil
			}
		}
	} else if nullable && rng.Float64() < 0.15 && !strings.HasSuffix(name, "_id") {
		return nil
	}
	return s.scalar(rng, t, c, i, false)
}

// pickSkewed spreads a status column over its allowed values the way a live system does: in-flight states
// (pending, delivering...) are a small head, failure and rejection states rarer still, and settled states the rest.
func pickSkewed(rng *rand.Rand, values []string, queue bool) string {
	var pending, rare, settled []string
	for _, v := range values {
		switch {
		case pendingValues.MatchString(v):
			pending = append(pending, v)
		case rareValues.MatchString(v):
			rare = append(rare, v)
		default:
			settled = append(settled, v)
		}
	}
	if len(settled) == 0 {
		return values[rng.Intn(len(values))]
	}
	pendingShare := 0.3
	if queue {
		pendingShare = 0.05
	}
	roll := rng.Float64()
	if len(pending) > 0 && roll < pendingShare {
		return pending[rng.Intn(len(pending))]
	}
	if len(rare) > 0 && roll < pendingShare+0.03 {
		return rare[rng.Intn(len(rare))]
	}
	return settled[rng.Intn(len(settled))]
}

func (s *seeder) scalar(rng *rand.Rand, t *table, c *column, i int, element bool) *string {
	name := c.name
	f := c.format
	switch {
	case f == "uuid" || f == "text" || f == "citext" || strings.HasPrefix(f, "character"):
		if !element && !t.uniqueCols[name] && (strings.HasSuffix(name, "_id") || name == "region" || name == "hostname" || name == "host" || name == "node_name" || name == "cluster_name" || name == "internal_name") {
			values := s.pool(c, poolSize(name, t.rows))
			return strp(values[skewedIndex(rng, len(values))])
		}
		return strp(s.textValue(rng, c, i, false))
	case f == "boolean":
		if strings.Contains(name, "deleted") || strings.Contains(name, "revoked") || strings.Contains(name, "disabled") || strings.Contains(name, "suspended") || strings.Contains(name, "archived") || strings.Contains(name, "test") {
			return strp(strconv.FormatBool(rng.Float64() < 0.1))
		}
		return strp(strconv.FormatBool(rng.Intn(2) == 0))
	case f == "smallint" || f == "integer" || f == "bigint":
		limit := int64(math.MaxInt32)
		if f == "smallint" {
			limit = 32767
		}
		var v int64
		switch {
		case strings.Contains(name, "attempt") || strings.Contains(name, "retry") || strings.Contains(name, "retries") || strings.Contains(name, "failures"):
			v = int64(rng.Intn(4))
		case strings.Contains(name, "version") || strings.Contains(name, "revision") || strings.Contains(name, "generation") || strings.Contains(name, "seq") || strings.Contains(name, "epoch") || strings.Contains(name, "sequence") || strings.Contains(name, "ordinal") || strings.Contains(name, "position"):
			v = int64(i + 1)
		case name == "port" || strings.HasSuffix(name, "_port"):
			v = int64(1024 + rng.Intn(60000))
		case strings.Contains(name, "bytes") || strings.Contains(name, "size"):
			v = int64(rng.Intn(1 << 30))
		case strings.Contains(name, "priority") || strings.Contains(name, "weight") || strings.Contains(name, "percent"):
			v = int64(rng.Intn(100))
		default:
			v = int64(rng.Intn(1000))
		}
		return strp(strconv.FormatInt(min(v, limit), 10))
	case strings.HasPrefix(f, "numeric") || f == "double precision" || f == "real" || f == "money":
		maxv := 1000.0
		if m := lenRe.FindStringSubmatch(f); m != nil {
			p := catalogInteger(m[1])
			sc := 0
			if m[2] != "" {
				sc = catalogInteger(m[2])
			}
			maxv = math.Min(maxv, math.Pow(10, float64(p-sc))-1)
		}
		return strp(strconv.FormatFloat(rng.Float64()*maxv, 'f', 2, 64))
	case strings.Contains(f, "timestamp") || f == "date":
		var ts time.Time
		switch {
		case dueColRe.MatchString(name):
			if rng.Float64() < 0.7 {
				ts = s.now.Add(-time.Duration(rng.Int63n(int64(48 * time.Hour))))
			} else {
				ts = s.now.Add(time.Duration(rng.Int63n(int64(time.Hour))))
			}
		case recentColRe.MatchString(name) && rng.Float64() < 0.9:
			// Heartbeats and health checks of live things are seconds old, which freshness filters rely on.
			ts = s.now.Add(-time.Duration(rng.Int63n(int64(2 * time.Minute))))
		case strings.HasPrefix(name, "expires") || strings.HasSuffix(name, "expires_at") || strings.Contains(name, "valid_until") || strings.Contains(name, "deadline"):
			ts = s.now.Add(time.Duration(rng.Int63n(int64(60*24*time.Hour))) - 30*24*time.Hour)
		default:
			ts = s.now.Add(-time.Duration(rng.Int63n(int64(30 * 24 * time.Hour))))
		}
		if f == "date" {
			return strp(ts.Format("2006-01-02"))
		}
		return strp(ts.Format(time.RFC3339Nano))
	case f == "jsonb" || f == "json":
		if strings.Contains(c.defaultExpr, "'[]'") || strings.HasSuffix(name, "s") && !strings.HasSuffix(name, "ss") && !strings.Contains(c.defaultExpr, "'{}'") && (strings.Contains(name, "names") || strings.Contains(name, "ids") || strings.Contains(name, "targets") || strings.Contains(name, "items") || strings.Contains(name, "list")) {
			return strp(fmt.Sprintf(`["v%d"]`, rng.Intn(100)))
		}
		return strp(fmt.Sprintf(`{"k": %d, "s": "v%d"}`, i, rng.Intn(100)))
	case f == "bytea":
		b := make([]byte, 32)
		rng.Read(b)
		return strp(`\x` + hex.EncodeToString(b))
	case f == "inet":
		return strp(fmt.Sprintf("10.%d.%d.%d", rng.Intn(256), rng.Intn(256), 1+rng.Intn(254)))
	case f == "cidr":
		return strp(fmt.Sprintf("10.%d.%d.0/24", rng.Intn(256), rng.Intn(256)))
	case f == "interval":
		return strp(fmt.Sprintf("%d seconds", 60+rng.Intn(86400)))
	case strings.HasPrefix(f, "time"):
		return strp(fmt.Sprintf("%02d:%02d:00", rng.Intn(24), rng.Intn(60)))
	case strings.HasPrefix(f, "vector"):
		dims := 3
		if m := lenRe.FindStringSubmatch(f); m != nil {
			dims = catalogInteger(m[1])
		}
		parts := make([]string, dims)
		for k := range parts {
			parts[k] = strconv.FormatFloat(rng.Float64()-0.5, 'f', 3, 64)
		}
		return strp("[" + strings.Join(parts, ",") + "]")
	case f == "tsvector":
		return strp("audit seed")
	case f == "point":
		return strp(fmt.Sprintf("(%f,%f)", rng.Float64()*180-90, rng.Float64()*360-180))
	}
	if c.notNull {
		return strp(s.textValue(rng, c, i, false))
	}
	return nil
}

// sample keeps 20 live rows per table, as text by column, for binding parameters.
func (s *seeder) sample(ctx context.Context, conn *sql.Conn) error {
	s.samples = map[string][]map[string]*string{}
	for _, t := range s.order {
		if _, err := conn.ExecContext(ctx, "SELECT setseed(0.42)"); err != nil {
			return err
		}
		names := make([]string, len(t.columns))
		exprs := make([]string, len(t.columns))
		for i, c := range t.columns {
			names[i] = c.name
			exprs[i] = pq.QuoteIdentifier(c.name) + "::text"
		}
		rows, err := conn.QueryContext(ctx, fmt.Sprintf("SELECT %s FROM %s ORDER BY random() LIMIT 20", strings.Join(exprs, ", "), t.qname))
		if err != nil {
			return fmt.Errorf("sample %s: %w", t.qname, err)
		}
		for rows.Next() {
			vals := make([]sql.NullString, len(names))
			ptrs := make([]any, len(names))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				_ = rows.Close() //nolint:sqlclosecheck // Close this iteration before returning its scan error.
				return fmt.Errorf("scan sample of %s: %w", t.qname, err)
			}
			row := map[string]*string{}
			for i, n := range names {
				if vals[i].Valid {
					v := vals[i].String
					row[n] = &v
				}
			}
			s.samples[t.qname] = append(s.samples[t.qname], row)
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	return nil
}

// Catalog type sizes and SQL parameter numbers must fit a machine integer.
func catalogInteger(value string) int {
	n, err := strconv.Atoi(value)
	if err != nil {
		panic(fmt.Sprintf("invalid catalog integer %q: %v", value, err))
	}
	return n
}
