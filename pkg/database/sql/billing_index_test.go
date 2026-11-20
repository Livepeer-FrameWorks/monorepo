package sql

import (
	"strings"
	"testing"
)

func TestBillableStorageIndexBuildDoesNotBlockArtifactWrites(t *testing.T) {
	const release = "migrations/foghorn/v0.3.16/"
	entries, err := Content.ReadDir(release + "expand")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range entries {
		if !strings.Contains(entry.Name(), "billable_storage_usage_index") {
			continue
		}
		data, readErr := Content.ReadFile(release + "expand/" + entry.Name())
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !strings.HasSuffix(entry.Name(), ".notx.sql") || !strings.Contains(string(data), "CREATE INDEX CONCURRENTLY IF NOT EXISTS") {
			t.Fatalf("billing index blocks writes: %s: %s", entry.Name(), data)
		}
		found = true
	}
	if !found {
		t.Fatal("billing index migration missing")
	}
	entries, err = Content.ReadDir(release + "postdeploy")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		data, readErr := Content.ReadFile(release + "postdeploy/" + entry.Name())
		if readErr != nil {
			t.Fatal(readErr)
		}
		s := string(data)
		if strings.Contains(s, "idx_foghorn_artifacts_billable_storage") && strings.Contains(s, "pg_index") && strings.Contains(s, "indisvalid") && strings.Contains(s, "indisready") {
			return
		}
	}
	t.Fatal("billing index has no postdeploy validity check")
}
