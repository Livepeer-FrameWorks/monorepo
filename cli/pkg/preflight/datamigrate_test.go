package preflight

import "testing"

func TestDataMigrationLedgerAbsent(t *testing.T) {
	t.Parallel()

	for _, output := range []string{
		`ERROR: relation "_data_migrations" does not exist (SQLSTATE 42P01)`,
		`pq: relation "_data_migrations" does not exist`,
		`{"error":"relation \"_data_migrations\" does not exist"}`,
	} {
		if !dataMigrationLedgerAbsent(output) {
			t.Fatalf("expected absent ledger classification for %q", output)
		}
	}
	for _, output := range []string{
		`ERROR: relation "quartermaster.tenants" does not exist`,
		`connection refused`,
		``,
	} {
		if dataMigrationLedgerAbsent(output) {
			t.Fatalf("unexpected absent ledger classification for %q", output)
		}
	}
}
