package provisioner

import (
	"strings"
	"testing"
)

const dumpFixtureLayout = "database: demo\nlayout: colocated\ncolocated_tables: [demo.tiers, demo.balances]\ndistributed_tables: [demo.usage]\n"

func TestRewriteDumpForLayoutConvertsColocatedKeysAndPlacesDistributedTables(t *testing.T) {
	layout := mustParseLayout(t, dumpFixtureLayout)
	src := `SET statement_timeout = 0;
SELECT pg_catalog.set_config('search_path', '', false);
CREATE TABLE demo.tiers (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name character varying(50) NOT NULL,
    CONSTRAINT tiers_pkey PRIMARY KEY((id) HASH)
);
CREATE TABLE demo.balances (
    tenant_id uuid NOT NULL,
    currency text NOT NULL,
    CONSTRAINT balances_pkey PRIMARY KEY((tenant_id) HASH, currency ASC),
    CONSTRAINT balances_hash_check CHECK ((currency <> 'HASH'::text))
);
CREATE TABLE demo.usage (
    id uuid NOT NULL,
    CONSTRAINT usage_pkey PRIMARY KEY((id) HASH)
);
CREATE TABLE public._migrations (
    version text NOT NULL,
    phase text NOT NULL,
    seq integer NOT NULL,
    CONSTRAINT _migrations_pkey PRIMARY KEY((version) HASH, phase ASC, seq ASC)
);
CREATE FUNCTION demo.touch() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$;
CREATE UNIQUE INDEX NONCONCURRENTLY tiers_name_key ON demo.tiers USING lsm (name HASH);
CREATE UNIQUE INDEX idx_tiers_default ON demo.tiers USING lsm ((1) HASH) WHERE (name = 'x HASH'::text);
CREATE INDEX idx_tiers_cast ON demo.tiers USING lsm (((name)::text) HASH, id DESC);
CREATE INDEX idx_tiers_lower ON ONLY demo.tiers USING lsm (lower((name)::text) HASH);
CREATE INDEX idx_usage_id ON demo.usage USING lsm (id HASH, tenant ASC);
ALTER TABLE ONLY demo.balances ADD CONSTRAINT balances_currency_key UNIQUE (currency);
ALTER TABLE ONLY demo.tiers ADD CONSTRAINT tiers_alt_key UNIQUE NULLS NOT DISTINCT ((name) HASH);
ALTER TABLE ONLY demo.usage ADD CONSTRAINT usage_tiers_fk FOREIGN KEY (id) REFERENCES demo.tiers(id);
CREATE TRIGGER tiers_touch BEFORE UPDATE ON demo.tiers FOR EACH ROW EXECUTE FUNCTION demo.touch();
`
	got, err := RewriteDumpForLayout(layout, src, isRelayoutRuntimeTable)
	if err != nil {
		t.Fatalf("RewriteDumpForLayout: %v", err)
	}
	for _, want := range []string{
		"CONSTRAINT tiers_pkey PRIMARY KEY(id)\n);",
		"CONSTRAINT balances_pkey PRIMARY KEY(tenant_id, currency ASC),",
		"CHECK ((currency <> 'HASH'::text))",
		"CONSTRAINT usage_pkey PRIMARY KEY((id) HASH)\n) WITH (COLOCATION = false);",
		"CONSTRAINT _migrations_pkey PRIMARY KEY(version, phase ASC, seq ASC)",
		"ON demo.tiers USING lsm (name);",
		"USING lsm ((1)) WHERE (name = 'x HASH'::text);",
		"USING lsm (((name)::text), id DESC);",
		"USING lsm (lower((name)::text));",
		"ON demo.usage USING lsm (id HASH, tenant ASC);",
		"ADD CONSTRAINT balances_currency_key UNIQUE (currency);",
		"ADD CONSTRAINT tiers_alt_key UNIQUE NULLS NOT DISTINCT (name);",
		"FOREIGN KEY (id) REFERENCES demo.tiers(id);",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rewritten dump missing %q:\n%s", want, got)
		}
	}
	if count := strings.Count(got, "HASH"); count != 4 {
		t.Fatalf("rewritten dump keeps %d HASH occurrences, want the distributed table's two keys and two string literals:\n%s", count, got)
	}
}

func TestRewriteDumpForLayoutRejectsUnplacedRelations(t *testing.T) {
	layout := mustParseLayout(t, dumpFixtureLayout)
	for name, sql := range map[string]string{
		"unclassified table":      "CREATE TABLE demo.unknown (id uuid NOT NULL, CONSTRAINT unknown_pkey PRIMARY KEY((id) HASH));",
		"index on unknown table":  "CREATE INDEX idx_unknown ON demo.unknown USING lsm (id HASH);",
		"unqualified index table": "CREATE INDEX idx_unknown ON unknown USING lsm (id HASH);",
		"unique on unknown table": "ALTER TABLE ONLY demo.unknown ADD CONSTRAINT unknown_key UNIQUE (id);",
		"index without key list":  "CREATE INDEX idx_tiers ON demo.tiers USING lsm;",
		"table clause after list": "CREATE TABLE demo.tiers (id uuid) WITH (fillfactor = 70);",
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := RewriteDumpForLayout(layout, sql, nil); err == nil {
				t.Fatalf("expected rejection, got:\n%s", got)
			}
		})
	}
	distributed := &DatabaseLayout{Database: "demo", Layout: DatabaseLayoutDistributed}
	if _, err := RewriteDumpForLayout(distributed, "CREATE TABLE demo.tiers (id uuid);", nil); err == nil {
		t.Fatal("RewriteDumpForLayout accepted a distributed layout")
	}
}
