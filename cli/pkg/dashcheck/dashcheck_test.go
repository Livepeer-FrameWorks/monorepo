package dashcheck

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"frameworks/cli/pkg/metabase"
)

func TestGrafanaDashboardQueriesReferenceKnownMetrics(t *testing.T) {
	repoRoot := findRepoRoot(t)
	knownMetrics := loadDeclaredMetrics(t, repoRoot)
	dashboardPath := filepath.Join(repoRoot, "infrastructure/grafana/dashboards/frameworks-ops.json")

	expressions := loadGrafanaExpressions(t, dashboardPath)
	var failures []string
	for _, expr := range expressions {
		for _, unknown := range unknownPromMetrics(expr, knownMetrics) {
			failures = append(failures, fmt.Sprintf("%s\n  %s", unknown, expr))
		}
	}
	if len(failures) > 0 {
		t.Fatalf("Grafana dashboard references metrics that are not declared or allowlisted:\n%s", strings.Join(failures, "\n\n"))
	}
}

func TestMetabaseCardQueriesMatchPeriscopeSchema(t *testing.T) {
	repoRoot := findRepoRoot(t)
	schema := loadClickHouseSchema(t, repoRoot)
	specPath := filepath.Join(repoRoot, "infrastructure/metabase/periscope_cards.yaml")

	spec := loadMetabaseSpec(t, specPath)
	var failures []string
	for _, card := range spec.Cards {
		if strings.TrimSpace(card.Query) == "" {
			failures = append(failures, fmt.Sprintf("%s has an empty query", card.Slug))
			continue
		}
		for _, err := range validateClickHouseQuery(card.Query, schema) {
			failures = append(failures, fmt.Sprintf("%s: %s", card.Slug, err))
		}
	}
	if len(failures) > 0 {
		t.Fatalf("Metabase card SQL diverges from the Periscope ClickHouse schema:\n%s", strings.Join(failures, "\n"))
	}
}

type tableSchema map[string]map[string]bool

func findRepoRoot(t *testing.T) string {
	t.Helper()

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for dir := wd; ; dir = filepath.Dir(dir) {
		if fileExists(filepath.Join(dir, "infrastructure/grafana/dashboards/frameworks-ops.json")) &&
			fileExists(filepath.Join(dir, "pkg/database/sql/clickhouse/periscope.sql")) {
			return dir
		}
		if dir == filepath.Dir(dir) {
			t.Fatal("could not find repo root")
		}
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func loadGrafanaExpressions(t *testing.T, path string) []string {
	t.Helper()

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	if err := json.Unmarshal(content, &doc); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	var expressions []string
	walkJSON(doc, func(obj map[string]any) {
		if expr, ok := obj["expr"].(string); ok && strings.TrimSpace(expr) != "" {
			expressions = append(expressions, expr)
		}
	})
	sort.Strings(expressions)
	return expressions
}

func walkJSON(v any, visit func(map[string]any)) {
	switch typed := v.(type) {
	case map[string]any:
		visit(typed)
		for _, child := range typed {
			walkJSON(child, visit)
		}
	case []any:
		for _, child := range typed {
			walkJSON(child, visit)
		}
	}
}

func loadDeclaredMetrics(t *testing.T, repoRoot string) map[string]bool {
	t.Helper()

	metrics := map[string]bool{
		"up":                                     true,
		"vm_rows_inserted_total":                 true,
		"vmagent_remotewrite_pending_data_bytes": true,
		"vmagent_remotewrite_packets_dropped_total": true,
		"vm_promscrape_scrapes_failed_total":        true,
		"process_resident_memory_bytes":             true,
		"go_goroutines":                             true,
		"go_memstats_heap_alloc_bytes":              true,
		"go_threads":                                true,
		"circuit_breaker_state":                     true,
		"circuit_breaker_state_transitions_total":   true,
	}

	err := filepath.WalkDir(repoRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "dist", "build", ".svelte-kit":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		contentBytes, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		content := string(contentBytes)
		prefixes := collectorPrefixes(content)
		for _, prefix := range prefixes {
			addMetric(metrics, prefix+"_http_requests_total")
			addMetric(metrics, prefix+"_http_request_duration_seconds")
			addMetric(metrics, prefix+"_active_connections")
			addMetric(metrics, prefix+"_service_info")
		}
		for _, name := range metricCollectorNames(content) {
			for _, prefix := range prefixes {
				addMetric(metrics, prefix+"_"+name)
			}
		}
		if strings.Contains(content, ".CreateKafkaMetrics(") {
			for _, prefix := range prefixes {
				addMetric(metrics, prefix+"_kafka_messages_total")
				addMetric(metrics, prefix+"_kafka_operation_duration_seconds")
				addMetric(metrics, prefix+"_kafka_consumer_lag")
			}
		}
		if strings.Contains(content, ".RegisterDBStats(") {
			for _, prefix := range prefixes {
				addMetric(metrics, prefix+"_db_open_connections")
				addMetric(metrics, prefix+"_db_in_use_connections")
				addMetric(metrics, prefix+"_db_idle_connections")
				addMetric(metrics, prefix+"_db_wait_count_total")
				addMetric(metrics, prefix+"_db_wait_duration_seconds_total")
			}
		}
		for _, name := range prometheusOptionNames(content) {
			addMetric(metrics, name)
			for _, namespace := range prometheusNamespaces(content) {
				addMetric(metrics, namespace+"_"+name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return metrics
}

var collectorPrefixRe = regexp.MustCompile(`NewMetricsCollector\(\s*"([^"]+)"`)
var metricCollectorNameRe = regexp.MustCompile(`(?s)\.New(?:Counter|Gauge|Histogram|Summary)\(\s*"([^"]+)"`)
var prometheusNameRe = regexp.MustCompile(`Name:\s*"([^"]+)"`)
var prometheusNamespaceRe = regexp.MustCompile(`Namespace:\s*"([^"]+)"`)

func collectorPrefixes(content string) []string {
	var prefixes []string
	for _, match := range collectorPrefixRe.FindAllStringSubmatch(content, -1) {
		prefixes = append(prefixes, sanitizeMetricPart(match[1]))
	}
	return dedupe(prefixes)
}

func metricCollectorNames(content string) []string {
	var names []string
	for _, match := range metricCollectorNameRe.FindAllStringSubmatch(content, -1) {
		names = append(names, match[1])
	}
	return dedupe(names)
}

func prometheusOptionNames(content string) []string {
	var names []string
	for _, match := range prometheusNameRe.FindAllStringSubmatch(content, -1) {
		if strings.HasPrefix(match[1], "test_") {
			continue
		}
		names = append(names, match[1])
	}
	return dedupe(names)
}

func prometheusNamespaces(content string) []string {
	var namespaces []string
	for _, match := range prometheusNamespaceRe.FindAllStringSubmatch(content, -1) {
		namespaces = append(namespaces, sanitizeMetricPart(match[1]))
	}
	return dedupe(namespaces)
}

func sanitizeMetricPart(value string) string {
	return strings.ReplaceAll(value, "-", "_")
}

func addMetric(metrics map[string]bool, name string) {
	metrics[name] = true
}

func unknownPromMetrics(expr string, known map[string]bool) []string {
	var unknown []string
	for _, pattern := range promNameMatcherRe.FindAllStringSubmatch(expr, -1) {
		if !matchesKnownMetric(pattern[1], known) {
			unknown = append(unknown, "__name__=~"+pattern[1])
		}
	}

	expr = stripQuotedStrings(expr)
	seen := map[string]bool{}
	for _, token := range promTokenRe.FindAllString(expr, -1) {
		if seen[token] || !looksLikeMetric(token) || promIgnoreToken(token) {
			continue
		}
		seen[token] = true
		if !knownMetric(token, known) {
			unknown = append(unknown, token)
		}
	}
	sort.Strings(unknown)
	return unknown
}

var promNameMatcherRe = regexp.MustCompile(`__name__\s*=~\s*"([^"]+)"`)
var promTokenRe = regexp.MustCompile(`[A-Za-z_:][A-Za-z0-9_:]*`)

func matchesKnownMetric(pattern string, known map[string]bool) bool {
	re, err := regexp.Compile("^" + pattern + "$")
	if err != nil {
		return false
	}
	for metric := range known {
		if re.MatchString(metric) || re.MatchString(metric+"_bucket") || re.MatchString(metric+"_sum") || re.MatchString(metric+"_count") {
			return true
		}
	}
	return false
}

func looksLikeMetric(token string) bool {
	if token == "up" {
		return true
	}
	prefixes := []string{
		"bridge_", "commodore_", "deckhand_", "decklog_", "foghorn_",
		"helmsman_", "navigator_", "periscope_", "privateer_", "purser_",
		"quartermaster_", "signalman_", "skipper_", "steward_", "stream_",
		"victoriametrics_", "vm_", "vmagent_", "vm_promscrape_",
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(token, prefix) {
			return true
		}
	}
	return strings.HasSuffix(token, "_total") ||
		strings.HasSuffix(token, "_seconds") ||
		strings.HasSuffix(token, "_bucket") ||
		strings.HasSuffix(token, "_bytes") ||
		strings.HasSuffix(token, "_connections")
}

func promIgnoreToken(token string) bool {
	if strings.HasPrefix(token, "__") {
		return true
	}
	_, ok := promIgnoredTokens[token]
	return ok
}

var promIgnoredTokens = set(
	"absent", "avg", "bottomk", "bool", "by", "clamp_min", "count", "count_values",
	"group_left", "group_right", "histogram_quantile", "ignoring", "increase", "irate",
	"label_values", "max", "min", "on", "quantile", "rate", "scalar", "sort_desc", "sum",
	"vector", "without",
	"algorithm", "blocking", "channel", "class", "client", "direction", "endpoint",
	"error_type", "event_type", "frameworks_service", "host", "instance", "job", "layer",
	"le", "method", "node_id", "operation", "partition", "port", "provider", "query",
	"query_type", "reason", "result", "selected_node", "service", "status", "stream",
	"table", "tenant_id", "topic", "trigger_type", "type", "url",
	"node", "service", "rate_interval",
)

func knownMetric(metric string, known map[string]bool) bool {
	if known[metric] {
		return true
	}
	for _, suffix := range []string{"_bucket", "_sum", "_count"} {
		if strings.HasSuffix(metric, suffix) && known[strings.TrimSuffix(metric, suffix)] {
			return true
		}
	}
	return false
}

func loadMetabaseSpec(t *testing.T, path string) metabase.Spec {
	t.Helper()

	spec, err := metabase.LoadSpec(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Cards) == 0 {
		t.Fatalf("%s has no cards", path)
	}
	return spec
}

func loadClickHouseSchema(t *testing.T, repoRoot string) tableSchema {
	t.Helper()

	schema := tableSchema{}
	paths := []string{filepath.Join(repoRoot, "pkg/database/sql/clickhouse/periscope.sql")}
	migrationRoot := filepath.Join(repoRoot, "pkg/database/sql/clickhouse/migrations/periscope")
	err := filepath.WalkDir(migrationRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".sql") {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)

	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		parseClickHouseDDL(string(content), schema)
	}
	return schema
}

func parseClickHouseDDL(content string, schema tableSchema) {
	content = stripSQLComments(content)
	for _, match := range createTableRe.FindAllStringSubmatchIndex(content, -1) {
		tableName := normalizeTableName(content[match[2]:match[3]])
		openParen := match[1] - 1
		closeParen := findMatchingParen(content, openParen)
		if closeParen == -1 {
			continue
		}
		ensureTable(schema, tableName)
		for _, column := range parseColumns(content[openParen+1 : closeParen]) {
			schema[tableName][column] = true
		}
	}
	for _, stmt := range splitSQLStatements(content) {
		match := alterTableRe.FindStringSubmatch(stmt)
		if match == nil {
			continue
		}
		tableName := normalizeTableName(match[1])
		ensureTable(schema, tableName)
		for _, add := range addColumnRe.FindAllStringSubmatch(stmt, -1) {
			schema[tableName][add[1]] = true
		}
	}
	for _, stmt := range splitSQLStatements(content) {
		match := createViewRe.FindStringSubmatch(stmt)
		if match == nil {
			continue
		}
		tableName := normalizeTableName(match[1])
		ensureTable(schema, tableName)
		for _, column := range parseSelectColumns(stmt) {
			schema[tableName][column] = true
		}
	}
}

var createTableRe = regexp.MustCompile(`(?i)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([A-Za-z_][A-Za-z0-9_.]*)\s*\(`)
var createViewRe = regexp.MustCompile(`(?i)\bCREATE\s+(?:MATERIALIZED\s+)?VIEW\s+(?:IF\s+NOT\s+EXISTS\s+)?([A-Za-z_][A-Za-z0-9_.]*)\s+`)
var alterTableRe = regexp.MustCompile(`(?i)ALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?([A-Za-z_][A-Za-z0-9_.]*)\b`)
var addColumnRe = regexp.MustCompile(`(?i)ADD\s+COLUMN\s+(?:IF\s+NOT\s+EXISTS\s+)?([A-Za-z_][A-Za-z0-9_]*)\b`)

func ensureTable(schema tableSchema, table string) {
	if schema[table] == nil {
		schema[table] = map[string]bool{}
	}
}

func parseColumns(body string) []string {
	var columns []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, ","))
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		column := strings.Trim(fields[0], "`")
		if ddlNonColumnToken(column) {
			continue
		}
		columns = append(columns, column)
	}
	return columns
}

func parseSelectColumns(stmt string) []string {
	selectIdx := indexSQLKeyword(stmt, "select")
	fromIdx := indexSQLKeyword(stmt, "from")
	if selectIdx < 0 || fromIdx <= selectIdx {
		return nil
	}
	var columns []string
	for _, expr := range splitTopLevelComma(stmt[selectIdx+len("select") : fromIdx]) {
		expr = strings.TrimSpace(expr)
		if expr == "" {
			continue
		}
		if match := sqlAliasRe.FindStringSubmatch(expr); match != nil {
			columns = append(columns, match[1])
			continue
		}
		if column := trailingIdentifier(expr); column != "" && !sqlKeywordOrFunction(column) {
			columns = append(columns, column)
		}
	}
	return dedupe(columns)
}

func indexSQLKeyword(stmt, keyword string) int {
	re := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(keyword) + `\b`)
	loc := re.FindStringIndex(stmt)
	if loc == nil {
		return -1
	}
	return loc[0]
}

func splitTopLevelComma(input string) []string {
	var parts []string
	start := 0
	depth := 0
	for i, r := range input {
		switch r {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				parts = append(parts, input[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, input[start:])
	return parts
}

func trailingIdentifier(expr string) string {
	matches := sqlTokenRe.FindAllString(expr, -1)
	if len(matches) == 0 {
		return ""
	}
	return matches[len(matches)-1]
}

func ddlNonColumnToken(token string) bool {
	switch strings.ToUpper(token) {
	case "INDEX", "PROJECTION", "CONSTRAINT", "PRIMARY", "ORDER", "PARTITION", "TTL", "SETTINGS", "ENGINE":
		return true
	default:
		return false
	}
}

func validateClickHouseQuery(query string, schema tableSchema) []string {
	var failures []string
	tables := queryTables(query)
	if len(tables) == 0 {
		return []string{"query does not reference a table"}
	}
	for _, table := range tables {
		if schema[table] == nil {
			failures = append(failures, fmt.Sprintf("unknown table %q", table))
		}
	}
	if len(failures) > 0 {
		return failures
	}

	allowed := set("periscope")
	for _, table := range tables {
		allowed[table] = true
		for column := range schema[table] {
			allowed[column] = true
		}
	}
	for _, alias := range sqlAliases(query) {
		allowed[alias] = true
	}

	scrubbed := stripQuotedStrings(stripSQLComments(query))
	for _, token := range sqlTokenRe.FindAllString(scrubbed, -1) {
		if sqlKeywordOrFunction(token) || allowed[token] {
			continue
		}
		failures = append(failures, fmt.Sprintf("unknown identifier %q", token))
	}
	return dedupe(failures)
}

func queryTables(query string) []string {
	var tables []string
	for _, match := range sqlTableRefRe.FindAllStringSubmatch(query, -1) {
		tables = append(tables, normalizeTableName(match[1]))
	}
	return dedupe(tables)
}

func sqlAliases(query string) []string {
	var aliases []string
	for _, match := range sqlAliasRe.FindAllStringSubmatch(query, -1) {
		aliases = append(aliases, match[1])
	}
	return dedupe(aliases)
}

var sqlTableRefRe = regexp.MustCompile(`(?i)\b(?:FROM|JOIN)\s+([A-Za-z_][A-Za-z0-9_.]*)`)
var sqlAliasRe = regexp.MustCompile(`(?i)\bAS\s+([A-Za-z_][A-Za-z0-9_]*)\b`)
var sqlTokenRe = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

func normalizeTableName(value string) string {
	value = strings.Trim(value, "`")
	if dot := strings.LastIndex(value, "."); dot >= 0 {
		value = value[dot+1:]
	}
	return value
}

func sqlKeywordOrFunction(token string) bool {
	_, ok := sqlIgnoredTokens[strings.ToLower(token)]
	return ok
}

var sqlIgnoredTokens = lowerSet(
	"select", "from", "where", "group", "by", "order", "limit", "as", "and", "or",
	"is", "not", "null", "interval", "hour", "day", "final", "desc", "asc",
	"count", "countif", "round", "avgif", "quantileif", "nullif", "coalesce",
	"max", "min", "sum", "ifnull", "pow", "argmax", "tostartofhour", "now",
	"today", "todate", "todatetime", "uniqexact", "quantile",
)

func stripSQLComments(content string) string {
	content = regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(content, " ")
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if idx := strings.Index(line, "--"); idx >= 0 {
			lines[i] = line[:idx]
		}
	}
	return strings.Join(lines, "\n")
}

func stripQuotedStrings(content string) string {
	content = regexp.MustCompile(`'([^']|'')*'`).ReplaceAllString(content, " ")
	content = regexp.MustCompile(`"([^"\\]|\\.)*"`).ReplaceAllString(content, " ")
	return content
}

func splitSQLStatements(content string) []string {
	var statements []string
	for _, stmt := range strings.Split(content, ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt != "" {
			statements = append(statements, stmt)
		}
	}
	return statements
}

func findMatchingParen(content string, open int) int {
	depth := 0
	for i := open; i < len(content); i++ {
		switch content[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func set(values ...string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}

func lowerSet(values ...string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[strings.ToLower(value)] = true
	}
	return result
}

func dedupe(values []string) []string {
	seen := map[string]bool{}
	var result []string
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
