package provisioner

import "testing"

func TestDataMigrationLedgerTargetsUseOwningSchema(t *testing.T) {
	targets := dataMigrationLedgerTargets([]SchemaDatabase{
		{Name: "quartermaster"},
		{Name: "foghorn_eu", SourceName: "foghorn", Schema: "foghorn"},
		{Name: "foghorn_eu", SourceName: "foghorn", Schema: "foghorn"},
		{Name: "  "},
	})
	want := []dataMigrationLedgerTarget{
		{database: "quartermaster", schema: "quartermaster"},
		{database: "foghorn_eu", schema: "foghorn"},
	}
	if len(targets) != len(want) {
		t.Fatalf("targets = %#v, want %#v", targets, want)
	}
	for i := range want {
		if targets[i] != want[i] {
			t.Fatalf("targets[%d] = %#v, want %#v", i, targets[i], want[i])
		}
	}
}

func TestDataMigrationLedgerRelation(t *testing.T) {
	relation, err := dataMigrationLedgerRelation("quartermaster")
	if err != nil {
		t.Fatal(err)
	}
	if relation != "quartermaster._data_migrations" {
		t.Fatalf("relation = %q", relation)
	}
	if _, err := dataMigrationLedgerRelation("quartermaster; DROP SCHEMA public"); err == nil {
		t.Fatal("expected invalid schema name to fail")
	}
}

func TestUndefinedDataMigrationTableOutputUsesQualifiedRelation(t *testing.T) {
	output := `ERROR: relation "foghorn._data_migrations" does not exist`
	if !isUndefinedDataMigrationTableOutput(output, "foghorn._data_migrations") {
		t.Fatal("expected missing ledger table to be recognized")
	}
	if isUndefinedDataMigrationTableOutput(output, "quartermaster._data_migrations") {
		t.Fatal("must not recognize an unrelated missing table")
	}
}
