package provisioner

import "testing"

func TestBuildCreateDatabaseQueryQuotesIdentifiers(t *testing.T) {
	query := buildCreateDatabaseQuery(`tenant-db"; DROP DATABASE postgres; --`, `owner"; GRANT ALL; --`)
	expected := `CREATE DATABASE "tenant-db""; DROP DATABASE postgres; --" OWNER "owner""; GRANT ALL; --"`
	if query != expected {
		t.Fatalf("unexpected query:\n got: %s\nwant: %s", query, expected)
	}
}

func TestBuildCreateDatabaseQueryWithoutOwner(t *testing.T) {
	query := buildCreateDatabaseQuery("tenant_db", "")
	expected := `CREATE DATABASE "tenant_db"`
	if query != expected {
		t.Fatalf("unexpected query:\n got: %s\nwant: %s", query, expected)
	}
}
