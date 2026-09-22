package provisioner

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestDatabaseACLStatementsRoundTripRecordedAccess(t *testing.T) {
	acl := "{=Tc/quartermaster,quartermaster=CTc/quartermaster,quartermaster_runtime=c/quartermaster,auditor=C*c/quartermaster}"

	// aclexplode output for the ACL above: auditor may grant CREATE onward but not CONNECT.
	rows, err := parseRelayoutACLGrants(`PUBLIC|TEMPORARY|f
PUBLIC|CONNECT|f
quartermaster|CREATE|f
quartermaster|TEMPORARY|f
quartermaster|CONNECT|f
quartermaster_runtime|CONNECT|f
auditor|CREATE|t
auditor|CONNECT|f`)
	if err != nil {
		t.Fatalf("parseRelayoutACLGrants: %v", err)
	}
	grants := databaseACLGrantStatements("quartermaster", "quartermaster", false, rows)
	want := []string{
		`REVOKE ALL ON DATABASE "quartermaster" FROM PUBLIC`,
		`GRANT TEMPORARY, CONNECT ON DATABASE "quartermaster" TO PUBLIC`,
		`GRANT CREATE, TEMPORARY, CONNECT ON DATABASE "quartermaster" TO quartermaster`,
		`GRANT CONNECT ON DATABASE "quartermaster" TO quartermaster_runtime`,
		`GRANT CONNECT ON DATABASE "quartermaster" TO auditor`,
		`GRANT CREATE ON DATABASE "quartermaster" TO auditor WITH GRANT OPTION`,
	}
	if !slices.Equal(grants, want) {
		t.Fatalf("grant statements:\n%s\nwant:\n%s", strings.Join(grants, "\n"), strings.Join(want, "\n"))
	}
	for _, bad := range []string{"auditor|CREATE", "auditor|SUPERUSER|t", "auditor|CONNECT|yes"} {
		if _, badErr := parseRelayoutACLGrants(bad); badErr == nil {
			t.Errorf("parseRelayoutACLGrants(%q) accepted a malformed row", bad)
		}
	}

	revokes, err := databaseACLRevokeStatements("quartermaster", acl, []string{"quartermaster_runtime", "extra_role"})
	if err != nil {
		t.Fatalf("databaseACLRevokeStatements: %v", err)
	}
	wantRevokes := []string{
		`REVOKE ALL ON DATABASE "quartermaster" FROM PUBLIC`,
		`REVOKE ALL ON DATABASE "quartermaster" FROM "auditor"`,
		`REVOKE ALL ON DATABASE "quartermaster" FROM "extra_role"`,
		`REVOKE ALL ON DATABASE "quartermaster" FROM "quartermaster"`,
		`REVOKE ALL ON DATABASE "quartermaster" FROM "quartermaster_runtime"`,
	}
	if !slices.Equal(revokes, wantRevokes) {
		t.Fatalf("revoke statements:\n%s", strings.Join(revokes, "\n"))
	}

	grantees, err := databaseACLConnectGrantees(acl)
	if err != nil || !slices.Equal(grantees, []string{"", "quartermaster", "quartermaster_runtime", "auditor"}) {
		t.Fatalf("connect grantees = %v, %v", grantees, err)
	}
}

func TestDatabaseACLDefaultAndEmpty(t *testing.T) {
	// A default ACL means owner-holds-everything plus PUBLIC CONNECT/TEMPORARY, and fencing revoked both.
	grants := databaseACLGrantStatements("navigator", "navigator", true, nil)
	if !slices.Equal(grants, []string{
		`REVOKE ALL ON DATABASE "navigator" FROM PUBLIC`,
		`GRANT ALL ON DATABASE "navigator" TO "navigator"`,
		`GRANT CONNECT, TEMPORARY ON DATABASE "navigator" TO PUBLIC`,
	}) {
		t.Fatalf("default ACL grants = %v", grants)
	}
	if grantees, err := databaseACLConnectGrantees(relayoutDefaultACL); err != nil || !slices.Equal(grantees, []string{""}) {
		t.Fatalf("default ACL grantees = %v, %v; want PUBLIC", grantees, err)
	}
	if grantees, err := databaseACLConnectGrantees("{}"); err != nil || len(grantees) != 0 {
		t.Fatalf("empty ACL grantees = %v, %v; want none", grantees, err)
	}
}

func TestDatabaseACLRejectsUnsupportedItems(t *testing.T) {
	for _, acl := range []string{
		`{"odd role"=c/owner}`,
		"{runtime=c}",
		"{runtime/owner}",
		"{runtime=cX/owner}",
		"runtime=c/owner",
	} {
		if _, _, err := parseDatabaseACL(acl); err == nil {
			t.Errorf("parseDatabaseACL(%q) accepted an unsupported ACL", acl)
		}
	}
}

func TestCompareRelayoutEvidenceReportsEveryDifference(t *testing.T) {
	source := &RelayoutEvidence{
		SchemaDigest: "a",
		Tables:       map[string]string{"app.t1": "10|1:2", "app.t2": "5|3:4"},
		Sequences:    map[string]string{"app.s": "7|true"},
		Triggers:     map[string]string{"app.t1.enqueue": "O"},
		Constraints:  map[string]string{"app.t1.fk": "t"},
		Attributes:   map[string]string{"schema:app": "app", "privilege:schema:app.app:app_runtime:USAGE": "false", "privilege:column:app.t1.secret:app:SELECT": "false"},
	}
	identical := &RelayoutEvidence{
		SchemaDigest: "a", Colocated: true,
		Tables:      map[string]string{"app.t1": "10|1:2", "app.t2": "5|3:4"},
		Sequences:   map[string]string{"app.s": "7|true"},
		Triggers:    map[string]string{"app.t1.enqueue": "O"},
		Constraints: map[string]string{"app.t1.fk": "t"},
		Attributes:  map[string]string{"schema:app": "app", "privilege:schema:app.app:app_runtime:USAGE": "false", "privilege:column:app.t1.secret:app:SELECT": "false"},
	}
	if differences := CompareRelayoutEvidence(source, identical); len(differences) != 0 {
		t.Fatalf("identical evidence differs: %v", differences)
	}
	shadow := &RelayoutEvidence{
		SchemaDigest:             "b",
		Tables:                   map[string]string{"app.t1": "10|9:9", "app.extra": "0|0:0"},
		Sequences:                map[string]string{"app.s": "7|false"},
		Triggers:                 map[string]string{"app.t1.enqueue": "D"},
		Constraints:              map[string]string{"app.t1.fk": "f"},
		Attributes:               map[string]string{"schema:app": "app", "privilege:schema:app.app:app_runtime:USAGE": "true"},
		InternalTriggersDisabled: 2,
	}
	want := []string{
		"2 internal triggers are disabled, source has 0",
		"attribute privilege:column:app.t1.secret:app:SELECT is missing",
		"attribute privilege:schema:app.app:app_runtime:USAGE is true, source is false",
		"constraint app.t1.fk is f, source is t",
		"schema digest b differs from a",
		"sequence app.s is 7|false, source is 7|true",
		"table app.extra is not in the source",
		"table app.t1 is 10|9:9, source is 10|1:2",
		"table app.t2 is missing",
		"trigger app.t1.enqueue is D, source is O",
	}
	if differences := CompareRelayoutEvidence(source, shadow); !slices.Equal(differences, want) {
		t.Fatalf("differences:\n%s", strings.Join(differences, "\n"))
	}
}

func TestParseRelayoutEvidenceRejectsUnknownRows(t *testing.T) {
	evidence, err := parseRelayoutEvidence("demo", strings.Join([]string{
		"colocated|t",
		"internal_triggers_disabled|0",
		"trigger|app.t.enqueue|O",
		"constraint|app.t.fk|t",
		"attribute|sequence_owner:app.s|a app.t.id",
		"sequence|app.s|42|true",
		"table|app.t|3|11:12",
	}, "\n"))
	if err != nil {
		t.Fatalf("parseRelayoutEvidence: %v", err)
	}
	if !evidence.Colocated || evidence.Tables["app.t"] != "3|11:12" || evidence.Sequences["app.s"] != "42|true" || evidence.Attributes["sequence_owner:app.s"] != "a app.t.id" {
		t.Fatalf("parsed evidence = %+v", evidence)
	}
	for _, row := range []string{"table|app.t|3", "mystery|x"} {
		if _, err := parseRelayoutEvidence("demo", row); err == nil {
			t.Errorf("parseRelayoutEvidence accepted %q", row)
		}
	}
}

func TestRelayoutReceiptDifferencesIncludePlacementAndProbes(t *testing.T) {
	receipt := &RelayoutReceipt{
		Source:         RelayoutEvidence{SchemaDigest: "a"},
		ShadowEvidence: RelayoutEvidence{SchemaDigest: "a"},
		PlacementDrift: []string{"database is distributed, layout declares colocated"},
		ProbeFailures:  []string{"probe 1 as purser_runtime: permission denied"},
	}
	want := []string{
		"placement: database is distributed, layout declares colocated",
		"capability: probe 1 as purser_runtime: permission denied",
	}
	if differences := receipt.Differences(); !slices.Equal(differences, want) {
		t.Fatalf("receipt differences = %v", differences)
	}
}

func TestYugabyteRelayoutValidatesTarget(t *testing.T) {
	base := YugabyteRelayout{Primary: stubYugabyteNode{}, YSQLHost: "127.0.0.1", YSQLPort: 5433, Database: "quartermaster", DumpDir: "/var/lib/frameworks/relayout", LeaseOwner: "operator"}
	if err := base.validate(); err != nil {
		t.Fatalf("valid relayout rejected: %v", err)
	}
	for name, mutate := range map[string]func(*YugabyteRelayout){
		"no primary":      func(r *YugabyteRelayout) { r.Primary = nil },
		"quoted name":     func(r *YugabyteRelayout) { r.Database = `quarter"master` },
		"shadow name":     func(r *YugabyteRelayout) { r.Database = "quartermaster__relayout" },
		"pre name":        func(r *YugabyteRelayout) { r.Database = "quartermaster__pre_relayout" },
		"too long":        func(r *YugabyteRelayout) { r.Database = strings.Repeat("a", 50) },
		"no lease owner":  func(r *YugabyteRelayout) { r.LeaseOwner = " " },
		"relative dump":   func(r *YugabyteRelayout) { r.DumpDir = "relayout" },
		"no ysql address": func(r *YugabyteRelayout) { r.YSQLPort = 0 },
	} {
		candidate := base
		mutate(&candidate)
		if err := candidate.validate(); err == nil {
			t.Errorf("%s: validate accepted an invalid relayout", name)
		}
	}
}

func TestNormalizeYugabyteIndexKeyOrderIgnoresPlacementOnly(t *testing.T) {
	distributed := "idx|navigator|idx_acme_accounts_tenant_email_ca|CREATE UNIQUE INDEX idx_acme_accounts_tenant_email_ca ON navigator.acme_accounts USING lsm (tenant_id HASH, email ASC, ca ASC)"
	colocated := "idx|navigator|idx_acme_accounts_tenant_email_ca|CREATE UNIQUE INDEX idx_acme_accounts_tenant_email_ca ON navigator.acme_accounts USING lsm (tenant_id ASC, email ASC, ca ASC)"
	if got, want := normalizeYugabyteIndexKeyOrder(distributed), normalizeYugabyteIndexKeyOrder(colocated); got != want {
		t.Fatalf("placement-only index difference survived normalization:\n%s\n%s", got, want)
	}
	for _, pair := range [][2]string{
		{"idx|s|i|CREATE INDEX i ON s.t USING lsm ((tenant_id, stream_id) HASH, created_at DESC)", "idx|s|i|CREATE INDEX i ON s.t USING lsm (tenant_id, stream_id, created_at DESC)"},
		{"idx|s|i|CREATE INDEX i ON s.t USING lsm (state ASC) WHERE ((status)::text = 'ASC)'::text)", "idx|s|i|CREATE INDEX i ON s.t USING lsm (state) WHERE ((status)::text = 'ASC)'::text)"},
		{"con|s|t|p|t_pkey|PRIMARY KEY (tenant_id, cluster_id)", "con|s|t|p|t_pkey|PRIMARY KEY (tenant_id, cluster_id)"},
	} {
		if got := normalizeYugabyteIndexKeyOrder(pair[0]); got != pair[1] {
			t.Errorf("normalizeYugabyteIndexKeyOrder(%q) = %q, want %q", pair[0], got, pair[1])
		}
	}
	expressionDistributed := "idx|purser|idx_default|CREATE UNIQUE INDEX idx_default ON purser.billing_tiers USING lsm ((1) HASH) WHERE (is_default = true)"
	expressionColocated := "idx|purser|idx_default|CREATE UNIQUE INDEX idx_default ON purser.billing_tiers USING lsm ((1) ASC) WHERE (is_default = true)"
	if got, want := normalizeYugabyteIndexKeyOrder(expressionDistributed), normalizeYugabyteIndexKeyOrder(expressionColocated); got != want {
		t.Fatalf("expression key placement difference survived normalization:\n%s\n%s", got, want)
	}
	if normalizeYugabyteIndexKeyOrder("idx|s|i|CREATE INDEX i ON s.t USING lsm (created_at DESC)") == normalizeYugabyteIndexKeyOrder("idx|s|i|CREATE INDEX i ON s.t USING lsm (created_at ASC)") {
		t.Fatal("normalization erased a meaningful DESC ordering")
	}
}

func TestNormalizeArrayCastsMatchesDumpRoundTrip(t *testing.T) {
	source := "con|quartermaster|bootstrap_tokens|c|chk_kind|CHECK (((kind)::text = ANY ((ARRAY['edge_node'::character varying, 'service'::character varying, 'infrastructure_node'::character varying])::text[])))"
	restored := "con|quartermaster|bootstrap_tokens|c|chk_kind|CHECK (((kind)::text = ANY (ARRAY[('edge_node'::character varying)::text, ('service'::character varying)::text, ('infrastructure_node'::character varying)::text])))"
	if got := normalizeArrayCasts(source); got != restored {
		t.Fatalf("normalizeArrayCasts(source) =\n%s\nwant\n%s", got, restored)
	}
	if got := normalizeArrayCasts(restored); got != restored {
		t.Fatalf("normalizeArrayCasts changed the restored form:\n%s", got)
	}
	nullable := "con|purser|t|c|chk|CHECK (((quote_source IS NULL) OR ((quote_source)::text = ANY ((ARRAY['it''s, ]quoted'::character varying, 'b'::character varying])::text[]))))"
	want := "con|purser|t|c|chk|CHECK (((quote_source IS NULL) OR ((quote_source)::text = ANY (ARRAY[('it''s, ]quoted'::character varying)::text, ('b'::character varying)::text]))))"
	if got := normalizeArrayCasts(nullable); got != want {
		t.Fatalf("normalizeArrayCasts(quoted) =\n%s\nwant\n%s", got, want)
	}
	if normalizeArrayCasts(source) == normalizeArrayCasts(strings.Replace(source, "'service'", "'services'", 1)) {
		t.Fatal("normalization erased a changed literal")
	}
	uncast := "con|s|t|c|chk|CHECK ((kind = ANY (ARRAY['a'::text, 'b'::text])))"
	if got := normalizeArrayCasts(uncast); got != uncast {
		t.Fatalf("normalizeArrayCasts changed an array without a cast: %s", got)
	}
}

func TestNormalizeRelayoutViewLineMatchesDumpRoundTrip(t *testing.T) {
	if !strings.Contains(relayoutIntrospectionQuery, "replace(pg_get_viewdef(c.oid, false)") || strings.Contains(relayoutIntrospectionQuery, "md5(pg_get_viewdef") {
		t.Fatal("relayout introspection query does not read unprettied view definitions")
	}
	// Unprettied definitions of one view before and after ysql_dump and restore on 2025.2.3.0-b149.
	source := "view|purser|payment_report_provider_objects_without_local_rows|v| SELECT ppo.id    FROM ((purser.provider_payment_objects ppo      LEFT JOIN purser.billing_payments bp ON ((((ppo.local_reference_type)::text = 'payment'::text) AND (ppo.local_reference_id = bp.id))))      LEFT JOIN purser.payment_provider_intents ppi ON ((((ppo.local_reference_type)::text = 'intent'::text) AND (ppo.local_reference_id = ppi.id))))   WHERE ((ppo.local_reference_id IS NOT NULL) AND ((((ppo.local_reference_type)::text = 'payment'::text) AND (bp.id IS NULL)) OR ((ppo.local_reference_type)::text <> ALL ((ARRAY['payment'::character varying, 'sca_required'::character varying, 'intent'::character varying])::text[]))));|yugabyte"
	restored := "view|purser|payment_report_provider_objects_without_local_rows|v| SELECT ppo.id    FROM ((purser.provider_payment_objects ppo      LEFT JOIN purser.billing_payments bp ON ((((ppo.local_reference_type)::text = 'payment'::text) AND (ppo.local_reference_id = bp.id))))      LEFT JOIN purser.payment_provider_intents ppi ON ((((ppo.local_reference_type)::text = 'intent'::text) AND (ppo.local_reference_id = ppi.id))))   WHERE ((ppo.local_reference_id IS NOT NULL) AND ((((ppo.local_reference_type)::text = 'payment'::text) AND (bp.id IS NULL)) OR ((ppo.local_reference_type)::text <> ALL (ARRAY[('payment'::character varying)::text, ('sca_required'::character varying)::text, ('intent'::character varying)::text]))));|yugabyte"
	got, want := normalizeRelayoutViewLine(normalizeArrayCasts(source)), normalizeRelayoutViewLine(normalizeArrayCasts(restored))
	if got != want {
		t.Fatalf("view round trip differs after normalization:\n%s\n%s", got, want)
	}
	if !strings.HasPrefix(got, "view|purser|payment_report_provider_objects_without_local_rows|v|SELECT ppo.id FROM ((") || !strings.HasSuffix(got, "|yugabyte") {
		t.Fatalf("normalized view line lost its identity or owner: %s", got)
	}
	changed := strings.Replace(restored, "'sca_required'", "'expired'", 1)
	if normalizeRelayoutViewLine(changed) == want {
		t.Fatal("normalization erased a changed view predicate")
	}
	concat := "view|s|v|v| SELECT (a || b) AS ab\n   FROM s.t;|owner"
	if got, want := normalizeRelayoutViewLine(concat), "view|s|v|v|SELECT (a || b) AS ab FROM s.t;|owner"; got != want {
		t.Fatalf("normalizeRelayoutViewLine(%q) = %q, want %q", concat, got, want)
	}
}

func TestDiffSortedLinesReportsBothDirections(t *testing.T) {
	got := diffSortedLines([]string{"a", "b", "b", "c"}, []string{"b", "c", "d"})
	if want := []string{"+ d", "- a", "- b"}; !slices.Equal(got, want) {
		t.Fatalf("diffSortedLines = %v, want %v", got, want)
	}
}

func TestYugabyteMigrationLedgerDDLMatchesMigrateRole(t *testing.T) {
	normalize := func(sql string) string { return strings.Join(strings.Fields(sql), " ") }
	content := normalize(readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/yugabyte/tasks/migrate.yml"))
	if !strings.Contains(content, normalize(YugabyteMigrationLedgerDDL)) {
		t.Fatal("relayout ledger DDL differs from the yugabyte role's _migrations definition")
	}
}

type stubYugabyteNode struct{}

func (stubYugabyteNode) Name() string { return "stub" }

func (stubYugabyteNode) Query(context.Context, string, string) (string, error) { return "", nil }

func (stubYugabyteNode) Shell(context.Context, string) (string, error) { return "", nil }

func TestRelayoutPreflightEstimateCountsOnlyTheWindow(t *testing.T) {
	report := &RelayoutPreflight{
		Timings: RelayoutCopyTimings{
			SchemaDump: time.Hour, PreData: time.Hour, PostData: time.Hour,
			Compare: 3 * time.Second, DataDump: 4 * time.Second, Data: 10 * time.Second,
		},
		ChecksumDuration: 6 * time.Second,
		WindowChecks:     25 * time.Second,
	}
	if got, want := report.EstimatedDowntime(), 54*time.Second; got != want {
		t.Fatalf("EstimatedDowntime = %s, want %s: compare, data dump, load, two checksums, and window checks, but no schema phase", got, want)
	}
}

func TestIsRelayoutRuntimeTableMatchesLedgersInAnySchema(t *testing.T) {
	for table, want := range map[string]bool{
		"public._migrations":                  true,
		"public._data_migrations":             true,
		"quartermaster._data_migrations":      true,
		"commodore._data_migration_runs":      true,
		"_data_migrations":                    true,
		"quartermaster.infrastructure_nodes":  false,
		"public._schema_baseline":             false,
		"quartermaster._data_migrations_copy": false,
	} {
		if got := isRelayoutRuntimeTable(table); got != want {
			t.Errorf("isRelayoutRuntimeTable(%q) = %v, want %v", table, got, want)
		}
	}
}
