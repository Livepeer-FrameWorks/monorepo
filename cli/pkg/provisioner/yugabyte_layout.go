package provisioner

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"frameworks/cli/internal/releases"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
)

// DatabaseLayoutKind selects how YugabyteDB places the tables of one service database.
type DatabaseLayoutKind string

const (
	// DatabaseLayoutColocated creates the database WITH COLOCATION = true. Tables share one parent tablet unless the
	// layout lists them as distributed.
	DatabaseLayoutColocated DatabaseLayoutKind = "colocated"
	// DatabaseLayoutDistributed creates an ordinary database in which every table and index gets its own tablets.
	DatabaseLayoutDistributed DatabaseLayoutKind = "distributed"
)

// TablePlacement is where one table's rows live inside a YugabyteDB database.
type TablePlacement string

const (
	TablePlacementColocated   TablePlacement = "colocated"
	TablePlacementDistributed TablePlacement = "distributed"
)

// yugabyteColocationOptOut is the only placement clause ever emitted. YugabyteDB accepts it in colocated and
// non-colocated databases alike, while COLOCATION = true is rejected outside a colocated database, so the same
// rewritten DDL runs against production databases before and after they move to the colocated layout.
const yugabyteColocationOptOut = " WITH (COLOCATION = false)"

// ErrDatabaseLayoutMissing reports that pkg/database/sql/layout has no file for a database.
var ErrDatabaseLayoutMissing = errors.New("database layout not declared")

var (
	layoutDatabaseNamePattern = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)
	layoutTableNamePattern    = regexp.MustCompile(`^[a-z_][a-z0-9_]*\.[a-z_][a-z0-9_]*$`)
)

// DatabaseLayout is the repository-declared YugabyteDB placement contract for one logical database, loaded from
// pkg/database/sql/layout/<database>.yaml. PostgreSQL targets never read it.
type DatabaseLayout struct {
	Database          string             `yaml:"database"`
	Layout            DatabaseLayoutKind `yaml:"layout"`
	ColocatedTables   []string           `yaml:"colocated_tables"`
	DistributedTables []string           `yaml:"distributed_tables"`
	// WriteRateBenchmark lists colocated tables updated in place at fleet poll cadence. The real-engine benchmark
	// measures them on the shared parent tablet against distributed placement.
	WriteRateBenchmark []string `yaml:"write_rate_benchmark"`
	// RetiredTables are created by shipped migrations and dropped by later ones, so the baseline no longer creates
	// them. Each is also listed with its placement for clusters that still replay those migrations.
	RetiredTables []string `yaml:"retired_tables"`

	placement map[string]TablePlacement
}

// Colocated reports whether the database itself is created colocated.
func (l *DatabaseLayout) Colocated() bool {
	return l != nil && l.Layout == DatabaseLayoutColocated
}

// Placement returns the declared placement of a lowercase schema-qualified table and whether the layout determines it.
// Every table of a distributed database is distributed.
func (l *DatabaseLayout) Placement(table string) (TablePlacement, bool) {
	if l == nil {
		return "", false
	}
	if l.Layout == DatabaseLayoutDistributed {
		return TablePlacementDistributed, true
	}
	placement, ok := l.placement[table]
	return placement, ok
}

// LoadDatabaseLayout reads and validates the embedded layout for a logical database.
func LoadDatabaseLayout(database string) (*DatabaseLayout, error) {
	if !layoutDatabaseNamePattern.MatchString(database) {
		return nil, fmt.Errorf("database layout name %q must be a lowercase identifier", database)
	}
	name := path.Join("layout", database+".yaml")
	data, err := dbsql.Content.ReadFile(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrDatabaseLayoutMissing, name)
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	return parseDatabaseLayout(database, data)
}

func parseDatabaseLayout(database string, data []byte) (*DatabaseLayout, error) {
	var layout DatabaseLayout
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&layout); err != nil {
		return nil, fmt.Errorf("parse %s layout: %w", database, err)
	}
	if layout.Database != database {
		return nil, fmt.Errorf("%s layout declares database %q", database, layout.Database)
	}
	switch layout.Layout {
	case DatabaseLayoutDistributed:
		if len(layout.ColocatedTables)+len(layout.DistributedTables)+len(layout.WriteRateBenchmark)+len(layout.RetiredTables) > 0 {
			return nil, fmt.Errorf("%s layout is distributed, so every table is distributed and no table lists are allowed", database)
		}
		return &layout, nil
	case DatabaseLayoutColocated:
	default:
		return nil, fmt.Errorf("%s layout %q must be %q or %q", database, layout.Layout, DatabaseLayoutColocated, DatabaseLayoutDistributed)
	}

	layout.placement = make(map[string]TablePlacement, len(layout.ColocatedTables)+len(layout.DistributedTables))
	for _, group := range []struct {
		tables    []string
		placement TablePlacement
	}{
		{layout.ColocatedTables, TablePlacementColocated},
		{layout.DistributedTables, TablePlacementDistributed},
	} {
		for _, table := range group.tables {
			if !layoutTableNamePattern.MatchString(table) {
				return nil, fmt.Errorf("%s layout entry %q must be a lowercase schema-qualified table name", database, table)
			}
			if prior, duplicate := layout.placement[table]; duplicate {
				return nil, fmt.Errorf("%s layout lists %s more than once (already %s)", database, table, prior)
			}
			// Outbox and inbox rows arrive with events rather than fleet size, and a colocated parent tablet never
			// splits, so they are distributed regardless of their current write pattern.
			if group.placement == TablePlacementColocated && (strings.HasSuffix(table, "_outbox") || strings.HasSuffix(table, "_inbox")) {
				return nil, fmt.Errorf("%s layout lists %s as colocated; outbox and inbox tables must be distributed", database, table)
			}
			layout.placement[table] = group.placement
		}
	}
	for _, table := range layout.WriteRateBenchmark {
		if layout.placement[table] != TablePlacementColocated {
			return nil, fmt.Errorf("%s layout write_rate_benchmark entry %s must also be listed in colocated_tables", database, table)
		}
	}
	retired := make(map[string]struct{}, len(layout.RetiredTables))
	for _, table := range layout.RetiredTables {
		if _, ok := layout.placement[table]; !ok {
			return nil, fmt.Errorf("%s layout retired_tables entry %s must also be listed with its placement", database, table)
		}
		if _, duplicate := retired[table]; duplicate {
			return nil, fmt.Errorf("%s layout lists retired table %s more than once", database, table)
		}
		retired[table] = struct{}{}
	}
	return &layout, nil
}

// ValidateLayoutAgainstBaseline requires a colocated layout to classify exactly the tables its baseline creates, so a
// new table cannot ship without a placement decision and a removed table cannot leave a stale entry behind.
func ValidateLayoutAgainstBaseline(layout *DatabaseLayout, baselineSQL string) error {
	creations, err := scanTableCreations(baselineSQL)
	if err != nil {
		return err
	}
	if !layout.Colocated() {
		return nil
	}
	created := make(map[string]struct{}, len(creations))
	var unclassified []string
	for _, creation := range creations {
		created[creation.table] = struct{}{}
		if _, ok := layout.placement[creation.table]; !ok {
			unclassified = append(unclassified, creation.table)
		}
	}
	retired := make(map[string]struct{}, len(layout.RetiredTables))
	for _, table := range layout.RetiredTables {
		retired[table] = struct{}{}
	}
	var stale, retiredButCreated []string
	for table := range layout.placement {
		_, inBaseline := created[table]
		_, isRetired := retired[table]
		switch {
		case isRetired && inBaseline:
			retiredButCreated = append(retiredButCreated, table)
		case !isRetired && !inBaseline:
			stale = append(stale, table)
		}
	}
	sort.Strings(unclassified)
	sort.Strings(stale)
	sort.Strings(retiredButCreated)
	if len(unclassified) == 0 && len(stale) == 0 && len(retiredButCreated) == 0 {
		return nil
	}
	return fmt.Errorf("%s layout does not match its baseline: unclassified tables %v, stale entries %v, retired tables the baseline still creates %v",
		layout.Database, unclassified, stale, retiredButCreated)
}

// RewriteDDLForLayout appends the colocation opt-out to every CREATE TABLE the layout declares distributed. In a
// colocated layout an unclassified table is an error, so unplaced DDL never reaches a YugabyteDB engine. SQL that
// uses a relation-creating form the rewriter cannot place is rejected for every layout, including a nil one.
func RewriteDDLForLayout(layout *DatabaseLayout, sql string) (string, error) {
	creations, err := scanTableCreations(sql)
	if err != nil {
		return "", err
	}
	if !layout.Colocated() {
		return sql, nil
	}
	var out strings.Builder
	last := 0
	for _, creation := range creations {
		placement, ok := layout.placement[creation.table]
		if !ok {
			return "", fmt.Errorf("line %d: table %s is not classified in the %s layout; add it to colocated_tables or distributed_tables", creation.line, creation.table, layout.Database)
		}
		if placement != TablePlacementDistributed {
			continue
		}
		out.WriteString(sql[last:creation.optionOffset])
		out.WriteString(yugabyteColocationOptOut)
		last = creation.optionOffset
	}
	out.WriteString(sql[last:])
	return out.String(), nil
}

// YugabyteLayoutForDatabase returns the layout for a logical database, or nil when the platform does not own it.
func YugabyteLayoutForDatabase(source string) (*DatabaseLayout, error) {
	return yugabyteLayoutForSource(source)
}

// YugabyteDatabaseColocated reports whether a logical database is created colocated on YugabyteDB.
func YugabyteDatabaseColocated(source string) (bool, error) {
	layout, err := yugabyteLayoutForSource(source)
	if err != nil {
		return false, err
	}
	return layout.Colocated(), nil
}

// yugabyteLayoutForSource resolves the layout for a logical database. Platform databases named in the release catalog
// must declare one. Databases the platform does not own, such as Chatwoot, have no layout and keep YugabyteDB's
// default placement, whatever their name looks like; TestEveryEmbeddedBaselineBelongsToTheCatalog keeps a platform
// baseline from falling into that group.
func yugabyteLayoutForSource(source string) (*DatabaseLayout, error) {
	if loadErr := releases.LoadError(); loadErr != nil {
		return nil, fmt.Errorf("release catalog: %w", loadErr)
	}
	if !slices.Contains(releases.ServiceDatabaseNames(), source) {
		return nil, nil
	}
	return LoadDatabaseLayout(source)
}

// YugabyteServingTabletsQuery lists the serving tablet replicas of the current database hosted by the tserver that
// answers; tablets and peers are counted by querying every node and taking distinct tablet ids. A split parent stays
// listed as TABLET_DATA_SPLIT_COMPLETED until it is cleaned up, so only TABLET_DATA_READY replicas count.
const YugabyteServingTabletsQuery = "SELECT tablet_id FROM yb_local_tablets WHERE namespace_name = current_database() AND state = 'TABLET_DATA_READY'"

// YugabyteRelationPlacementQuery lists every table and secondary index outside system schemas with its observed
// placement, as pipe-separated rows under psql -tA. Primary-key indexes are stored inside their table in YugabyteDB
// and report no placement, so they are excluded. Vector indexes (ybhnsw, and hnsw where pgvector provides it) report
// is_colocated true even in a non-colocated database because they live with their base table, so they are excluded
// too and follow their table's placement.
const YugabyteRelationPlacementQuery = `
SELECT n.nspname || '.' || c.relname,
       c.relkind,
       COALESCE(tn.nspname || '.' || tc.relname, n.nspname || '.' || c.relname),
       p.is_colocated,
       p.num_tablets
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN pg_index i ON i.indexrelid = c.oid
LEFT JOIN pg_class tc ON tc.oid = i.indrelid
LEFT JOIN pg_namespace tn ON tn.oid = tc.relnamespace
LEFT JOIN pg_am am ON am.oid = c.relam
CROSS JOIN LATERAL yb_table_properties(c.oid) p
WHERE c.relkind IN ('r', 'p', 'i', 'I')
  AND n.nspname NOT IN ('pg_catalog', 'information_schema')
  AND n.nspname NOT LIKE 'pg_toast%'
  AND NOT COALESCE(i.indisprimary, false)
  AND COALESCE(am.amname, '') NOT IN ('ybhnsw', 'hnsw')
ORDER BY 1`

// YugabyteRelationPlacement is one row of YugabyteRelationPlacementQuery.
type YugabyteRelationPlacement struct {
	Relation string
	// Kind is pg_class.relkind: r or p for tables, i or I for indexes.
	Kind string
	// Table is the relation itself for tables and the indexed table for indexes.
	Table      string
	Colocated  bool
	NumTablets int
}

// ParseYugabyteRelationPlacements parses psql -tA output of YugabyteRelationPlacementQuery.
func ParseYugabyteRelationPlacements(output string) ([]YugabyteRelationPlacement, error) {
	var relations []YugabyteRelationPlacement
	for line := range strings.SplitSeq(strings.TrimSpace(output), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		cols := strings.Split(line, "|")
		if len(cols) != 5 {
			return nil, fmt.Errorf("placement row %q has %d columns, want 5", line, len(cols))
		}
		var relation YugabyteRelationPlacement
		relation.Relation, relation.Kind, relation.Table = cols[0], cols[1], cols[2]
		if err := scanPsqlValue(cols[3], &relation.Colocated); err != nil {
			return nil, fmt.Errorf("placement row %q: %w", line, err)
		}
		if err := scanPsqlValue(cols[4], &relation.NumTablets); err != nil {
			return nil, fmt.Errorf("placement row %q: %w", line, err)
		}
		relations = append(relations, relation)
	}
	return relations, nil
}

// YugabytePlacementDrift compares observed placement with a layout. When the database was created with a different
// colocation setting, only that mismatch is reported: every relation then follows the database setting and only a
// relayout can change it. Relations whose table the layout does not list, such as the migration ledger, are skipped.
func YugabytePlacementDrift(layout *DatabaseLayout, databaseColocated bool, relations []YugabyteRelationPlacement) []string {
	if layout.Colocated() != databaseColocated {
		declared := DatabaseLayoutDistributed
		if layout.Colocated() {
			declared = DatabaseLayoutColocated
		}
		return []string{fmt.Sprintf("database is %s, layout declares %s", placementWord(databaseColocated), declared)}
	}
	var drift []string
	for _, relation := range relations {
		declared, ok := layout.Placement(relation.Table)
		if !ok {
			continue
		}
		if want := declared == TablePlacementColocated; relation.Colocated != want {
			subject := relation.Relation
			if relation.Relation != relation.Table {
				subject = fmt.Sprintf("%s (index on %s)", relation.Relation, relation.Table)
			}
			drift = append(drift, fmt.Sprintf("%s is %s, layout declares %s", subject, placementWord(relation.Colocated), declared))
		}
	}
	return drift
}

func placementWord(colocated bool) string {
	if colocated {
		return string(TablePlacementColocated)
	}
	return string(TablePlacementDistributed)
}

// yugabyteSQLForSource returns SQL for a YugabyteDB target with the source database's layout applied.
func yugabyteSQLForSource(source, sql string) (string, error) {
	layout, err := yugabyteLayoutForSource(source)
	if err != nil {
		return "", err
	}
	rewritten, err := RewriteDDLForLayout(layout, sql)
	if err != nil {
		return "", fmt.Errorf("%s: %w", source, err)
	}
	return rewritten, nil
}

type tableCreation struct {
	table string
	line  int
	// optionOffset is the byte offset just after the column list's closing parenthesis.
	optionOffset int
	// open and close are the statement token indexes of the column list's parentheses.
	open, close int
}

// scanTableCreations returns every top-level CREATE TABLE in src in order. Forms whose placement a trailing WITH
// clause cannot express are rejected: temporary and unlogged tables, CREATE TABLE AS/OF/PARTITION OF, any clause after
// the column list, unqualified names, materialized views, top-level SELECT ... INTO, and table creation inside a
// dollar-quoted body. Tables created through dynamic EXECUTE strings are not visible to this scan.
func scanTableCreations(src string) ([]tableCreation, error) {
	statements, err := sqlStatements(src)
	if err != nil {
		return nil, err
	}
	var creations []tableCreation
	for _, stmt := range statements {
		creation, scanErr := scanStatement(src, stmt)
		if scanErr != nil {
			return nil, scanErr
		}
		if creation != nil {
			creations = append(creations, *creation)
		}
	}
	return creations, nil
}

// sqlStatements tokenizes src and splits it into top-level statements on semicolons outside parentheses.
func sqlStatements(src string) ([][]sqlToken, error) {
	tokens, err := tokenizeSQL(src)
	if err != nil {
		return nil, err
	}
	var statements [][]sqlToken
	start, depth := 0, 0
	for i, tok := range tokens {
		if tok.kind != sqlTokenPunct {
			continue
		}
		switch tok.text {
		case "(":
			depth++
		case ")":
			depth--
			if depth < 0 {
				return nil, sqlPositionError(src, tok.start, "unbalanced closing parenthesis")
			}
		case ";":
			if depth == 0 {
				if i > start {
					statements = append(statements, tokens[start:i])
				}
				start = i + 1
			}
		}
	}
	if depth != 0 {
		return nil, sqlPositionError(src, len(src), "unbalanced opening parenthesis")
	}
	if start < len(tokens) {
		statements = append(statements, tokens[start:])
	}
	return statements, nil
}

func scanStatement(src string, stmt []sqlToken) (*tableCreation, error) {
	for _, tok := range stmt {
		if tok.kind == sqlTokenDollarBody {
			if err := rejectTableCreationInBody(src, tok); err != nil {
				return nil, err
			}
		}
	}
	if len(stmt) == 0 {
		return nil, nil
	}
	for _, tok := range stmt {
		if isSQLPunct(tok, "\\") {
			return nil, sqlPositionError(src, tok.start, "psql meta-commands are not supported: their effect on the statements that follow cannot be scanned")
		}
	}
	switch {
	case isSQLWord(stmt[0], "select"), isSQLWord(stmt[0], "with"):
		return nil, rejectSelectInto(src, stmt)
	case isSQLWord(stmt[0], "copy"):
		for _, tok := range stmt {
			if isSQLWord(tok, "stdin") {
				return nil, sqlPositionError(src, stmt[0].start, "COPY ... FROM stdin is not supported: its inline data cannot be scanned")
			}
		}
		return nil, nil
	case isSQLWord(stmt[0], "explain"), isSQLWord(stmt[0], "prepare"):
		if createsTable(stmt[1:]) {
			return nil, sqlPositionError(src, stmt[0].start, "%s of a table-creating statement is not supported", strings.ToUpper(stmt[0].text))
		}
		return nil, nil
	}
	if !isSQLWord(stmt[0], "create") {
		return nil, nil
	}
	i := 1
	if i < len(stmt) && isSQLWord(stmt[i], "materialized") {
		return nil, sqlPositionError(src, stmt[0].start, "CREATE MATERIALIZED VIEW is not supported: layouts place tables only")
	}
	if i < len(stmt) && isSQLWord(stmt[i], "foreign") {
		return nil, sqlPositionError(src, stmt[0].start, "CREATE FOREIGN TABLE is not supported: layouts place tables only")
	}
	if i < len(stmt) && isSQLWord(stmt[i], "schema") && createsTable(stmt[i+1:]) {
		return nil, sqlPositionError(src, stmt[0].start, "CREATE SCHEMA with an embedded CREATE TABLE is not supported: create the table in its own statement")
	}
	modifier := ""
	for i < len(stmt) && isTableModifier(stmt[i]) {
		modifier = stmt[i].text
		i++
	}
	if i >= len(stmt) || !isSQLWord(stmt[i], "table") {
		return nil, nil
	}
	if modifier != "" {
		return nil, sqlPositionError(src, stmt[0].start, "CREATE %s TABLE is not supported", strings.ToUpper(modifier))
	}
	i++
	if i+2 < len(stmt) && isSQLWord(stmt[i], "if") && isSQLWord(stmt[i+1], "not") && isSQLWord(stmt[i+2], "exists") {
		i += 3
	}
	table, next, err := parseQualifiedTableName(src, stmt, i)
	if err != nil {
		return nil, err
	}
	if next >= len(stmt) || !isSQLPunct(stmt[next], "(") {
		return nil, sqlPositionError(src, stmt[0].start, "CREATE TABLE %s must be followed by a column list; CREATE TABLE AS, OF, and PARTITION OF are not supported", table)
	}
	closeIndex := matchingSQLParen(stmt, next)
	if closeIndex < 0 {
		return nil, sqlPositionError(src, stmt[next].start, "CREATE TABLE %s has an unterminated column list", table)
	}
	if closeIndex+1 < len(stmt) {
		tail := stmt[closeIndex+1]
		return nil, sqlPositionError(src, tail.start, "CREATE TABLE %s has a clause after its column list (%s); storage, partitioning, inheritance, and tablespace clauses are not supported", table, src[tail.start:tail.end])
	}
	return &tableCreation{table: table, line: sqlLine(src, stmt[0].start), optionOffset: stmt[closeIndex].end, open: next, close: closeIndex}, nil
}

func isTableModifier(tok sqlToken) bool {
	if tok.kind != sqlTokenWord {
		return false
	}
	switch tok.text {
	case "global", "local", "temp", "temporary", "unlogged":
		return true
	}
	return false
}

func parseQualifiedTableName(src string, stmt []sqlToken, i int) (string, int, error) {
	schema, ok := sqlIdentifierAt(stmt, i)
	if !ok {
		return "", 0, sqlPositionError(src, stmt[0].start, "statement is missing a table name")
	}
	if i+2 >= len(stmt) || !isSQLPunct(stmt[i+1], ".") {
		return "", 0, sqlPositionError(src, stmt[i].start, "table %s must be schema-qualified so its placement can be resolved", schema)
	}
	name, ok := sqlIdentifierAt(stmt, i+2)
	if !ok {
		return "", 0, sqlPositionError(src, stmt[i+1].start, "table name after %s. is not an identifier", schema)
	}
	return schema + "." + name, i + 3, nil
}

func sqlIdentifierAt(stmt []sqlToken, i int) (string, bool) {
	if i >= len(stmt) {
		return "", false
	}
	switch stmt[i].kind {
	case sqlTokenWord, sqlTokenQuotedIdentifier:
		return stmt[i].text, true
	}
	return "", false
}

func matchingSQLParen(stmt []sqlToken, open int) int {
	depth := 0
	for i := open; i < len(stmt); i++ {
		if stmt[i].kind != sqlTokenPunct {
			continue
		}
		switch stmt[i].text {
		case "(":
			depth++
		case ")":
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// rejectSelectInto refuses a top-level SELECT ... INTO, including one after a WITH clause, which creates a table in
// the default placement. INTO after INSERT is not a table creation.
func rejectSelectInto(src string, stmt []sqlToken) error {
	depth := 0
	selecting := false
	for _, tok := range stmt {
		switch {
		case isSQLPunct(tok, "("):
			depth++
		case isSQLPunct(tok, ")"):
			depth--
		case depth != 0:
		case isSQLWord(tok, "select"):
			selecting = true
		case isSQLWord(tok, "from"), isSQLWord(tok, "insert"), isSQLWord(tok, "update"), isSQLWord(tok, "delete"), isSQLWord(tok, "merge"):
			selecting = false
		case selecting && isSQLWord(tok, "into"):
			return sqlPositionError(src, tok.start, "SELECT ... INTO creates a table and is not supported")
		}
	}
	return nil
}

// createsTable reports whether tokens contain CREATE [modifiers] TABLE.
func createsTable(tokens []sqlToken) bool {
	for i, tok := range tokens {
		if !isSQLWord(tok, "create") {
			continue
		}
		j := i + 1
		for j < len(tokens) && isTableModifier(tokens[j]) {
			j++
		}
		if j < len(tokens) && (isSQLWord(tokens[j], "table") || isSQLWord(tokens[j], "foreign")) {
			return true
		}
	}
	return false
}

// rejectTableCreationInBody refuses CREATE TABLE inside a function or DO body, where no layout clause can be added.
func rejectTableCreationInBody(src string, body sqlToken) error {
	tagLength := (body.end - body.start - len(body.text)) / 2
	base := body.start + tagLength
	tokens, err := tokenizeSQL(body.text)
	if err != nil {
		return fmt.Errorf("dollar-quoted body at line %d: %w", sqlLine(src, body.start), err)
	}
	for i, tok := range tokens {
		if tok.kind == sqlTokenDollarBody {
			if nestedErr := rejectTableCreationInBody(body.text, tok); nestedErr != nil {
				return fmt.Errorf("dollar-quoted body at line %d: %w", sqlLine(src, body.start), nestedErr)
			}
			continue
		}
		if !isSQLWord(tok, "create") {
			continue
		}
		j := i + 1
		for j < len(tokens) && isTableModifier(tokens[j]) {
			j++
		}
		if j < len(tokens) && isSQLWord(tokens[j], "table") {
			return sqlPositionError(src, base+tok.start, "CREATE TABLE inside a dollar-quoted body is not supported: its placement cannot be declared")
		}
	}
	return nil
}

type sqlTokenKind uint8

const (
	sqlTokenWord sqlTokenKind = iota + 1
	sqlTokenQuotedIdentifier
	sqlTokenString
	sqlTokenDollarBody
	sqlTokenNumber
	sqlTokenPunct
)

type sqlToken struct {
	kind sqlTokenKind
	// text is lowercased for words, unescaped for quoted identifiers, the body without its tags for dollar quotes,
	// and verbatim for numbers and punctuation. Strings carry no text.
	text  string
	start int
	end   int
}

// tokenizeSQL lexes PostgreSQL SQL into code tokens and drops comments. It recognizes line comments, nested block
// comments, standard and E” strings, double-quoted identifiers, and tagged dollar quotes, so keywords inside any of
// them are never read as DDL.
func tokenizeSQL(src string) ([]sqlToken, error) {
	if strings.HasPrefix(src, "\ufeff") {
		return nil, errors.New("SQL starts with a byte-order mark, which the engine does not accept")
	}
	var tokens []sqlToken
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v':
			i++
		case c == '-' && i+1 < len(src) && src[i+1] == '-':
			newline := strings.IndexByte(src[i:], '\n')
			if newline < 0 {
				i = len(src)
			} else {
				i += newline + 1
			}
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			end, err := skipSQLBlockComment(src, i)
			if err != nil {
				return nil, err
			}
			i = end
		case c == '\'':
			end, err := scanSQLQuoted(src, i, '\'', false)
			if err != nil {
				return nil, err
			}
			tokens = append(tokens, sqlToken{kind: sqlTokenString, start: i, end: end})
			i = end
		case c == '"':
			end, err := scanSQLQuoted(src, i, '"', false)
			if err != nil {
				return nil, err
			}
			tokens = append(tokens, sqlToken{kind: sqlTokenQuotedIdentifier, text: strings.ReplaceAll(src[i+1:end-1], `""`, `"`), start: i, end: end})
			i = end
		case c == '$':
			tag, ok := readDollarTag(src[i:])
			if !ok {
				tokens = append(tokens, sqlToken{kind: sqlTokenPunct, text: "$", start: i, end: i + 1})
				i++
				continue
			}
			bodyStart := i + len(tag)
			bodyLength := strings.Index(src[bodyStart:], tag)
			if bodyLength < 0 {
				return nil, sqlPositionError(src, i, "unterminated dollar-quoted string %s", tag)
			}
			end := bodyStart + bodyLength + len(tag)
			tokens = append(tokens, sqlToken{kind: sqlTokenDollarBody, text: src[bodyStart : bodyStart+bodyLength], start: i, end: end})
			i = end
		case isSQLIdentifierStart(c):
			j := i + 1
			for j < len(src) && isSQLIdentifierPart(src[j]) {
				j++
			}
			if j == i+1 && (c == 'e' || c == 'E') && j < len(src) && src[j] == '\'' {
				end, err := scanSQLQuoted(src, j, '\'', true)
				if err != nil {
					return nil, err
				}
				tokens = append(tokens, sqlToken{kind: sqlTokenString, start: i, end: end})
				i = end
				continue
			}
			tokens = append(tokens, sqlToken{kind: sqlTokenWord, text: strings.ToLower(src[i:j]), start: i, end: j})
			i = j
		case c >= '0' && c <= '9':
			j := i + 1
			for j < len(src) && (src[j] == '.' || isSQLIdentifierPart(src[j])) {
				j++
			}
			tokens = append(tokens, sqlToken{kind: sqlTokenNumber, text: src[i:j], start: i, end: j})
			i = j
		default:
			tokens = append(tokens, sqlToken{kind: sqlTokenPunct, text: src[i : i+1], start: i, end: i + 1})
			i++
		}
	}
	return tokens, nil
}

func skipSQLBlockComment(src string, start int) (int, error) {
	depth := 0
	i := start
	for i+1 < len(src) {
		switch {
		case src[i] == '/' && src[i+1] == '*':
			depth++
			i += 2
		case src[i] == '*' && src[i+1] == '/':
			depth--
			i += 2
			if depth == 0 {
				return i, nil
			}
		default:
			i++
		}
	}
	return 0, sqlPositionError(src, start, "unterminated block comment")
}

// scanSQLQuoted returns the offset after the closing quote. A doubled quote is an escaped quote; E” strings also
// treat a backslash as escaping the next byte.
func scanSQLQuoted(src string, start int, quote byte, backslashEscapes bool) (int, error) {
	i := start + 1
	for i < len(src) {
		c := src[i]
		switch {
		case backslashEscapes && c == '\\':
			i += 2
		case c == quote && i+1 < len(src) && src[i+1] == quote:
			i += 2
		case c == quote:
			return i + 1, nil
		default:
			i++
		}
	}
	kind := "string literal"
	if quote == '"' {
		kind = "quoted identifier"
	}
	return 0, sqlPositionError(src, start, "unterminated %s", kind)
}

func isSQLIdentifierStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

func isSQLIdentifierPart(c byte) bool {
	return isSQLIdentifierStart(c) || c >= '0' && c <= '9' || c == '$'
}

func isSQLWord(tok sqlToken, word string) bool {
	return tok.kind == sqlTokenWord && tok.text == word
}

func isSQLPunct(tok sqlToken, punct string) bool {
	return tok.kind == sqlTokenPunct && tok.text == punct
}

func sqlLine(src string, offset int) int {
	if offset > len(src) {
		offset = len(src)
	}
	return 1 + strings.Count(src[:offset], "\n")
}

func sqlPositionError(src string, offset int, format string, args ...any) error {
	return fmt.Errorf("line %d: %s", sqlLine(src, offset), fmt.Sprintf(format, args...))
}
