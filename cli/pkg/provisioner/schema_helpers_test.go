package provisioner

import (
	"os"
	"strings"
	"testing"
)

func TestBuildSchemaItemsIncludesConfiguredBaselineSchemas(t *testing.T) {
	items, cleanup, err := BuildSchemaItems([]SchemaDatabase{
		{Name: "quartermaster", Owner: "quartermaster"},
		{Name: "missing", Owner: "missing"},
		{Name: "quartermaster", Owner: "quartermaster"},
	})
	defer cleanup()
	if err != nil {
		t.Fatalf("BuildSchemaItems returned error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected one schema item, got %d", len(items))
	}
	if got := items[0]["db"]; got != "quartermaster" {
		t.Fatalf("expected quartermaster schema item, got %v", got)
	}
	if got := items[0]["schema"]; got != "quartermaster" {
		t.Fatalf("expected quartermaster schema name, got %v", got)
	}
	if got := items[0]["owner"]; got != "quartermaster" {
		t.Fatalf("expected quartermaster owner, got %v", got)
	}
	src, ok := items[0]["src"].(string)
	if !ok {
		t.Fatalf("schema src has type %T", items[0]["src"])
	}
	decoded, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read schema src: %v", err)
	}
	sql := string(decoded)
	for _, want := range []string{
		"CREATE SCHEMA IF NOT EXISTS quartermaster",
		"CREATE TABLE IF NOT EXISTS quartermaster.infrastructure_clusters",
		"CREATE TABLE IF NOT EXISTS quartermaster.service_instances",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("quartermaster schema missing %q", want)
		}
	}
}

func TestBuildSchemaItemsSkipsExternallyManagedSchemas(t *testing.T) {
	items, cleanup, err := BuildSchemaItems([]SchemaDatabase{
		{Name: "chatwoot", Owner: "chatwoot"},
		{Name: "listmonk", Owner: "listmonk"},
	})
	defer cleanup()
	if err != nil {
		t.Fatalf("BuildSchemaItems returned error: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected no schema items for externally managed schemas, got %d", len(items))
	}
}

func TestBuildSchemaItemsLeavesPortableSchemasUnchanged(t *testing.T) {
	items, cleanup, err := BuildSchemaItems([]SchemaDatabase{
		{Name: "commodore", Owner: "commodore"},
		{Name: "quartermaster", Owner: "quartermaster"},
		{Name: "skipper", Owner: "skipper"},
	})
	defer cleanup()
	if err != nil {
		t.Fatalf("BuildSchemaItems returned error: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("expected three schema items, got %d", len(items))
	}
	sqlByDB := map[string]string{}
	for _, item := range items {
		db := item["db"].(string)
		src := item["src"].(string)
		data, readErr := os.ReadFile(src)
		if readErr != nil {
			t.Fatalf("read schema src for %s: %v", db, readErr)
		}
		sqlByDB[db] = string(data)
	}
	if !strings.Contains(sqlByDB["commodore"], "CITEXT") {
		t.Fatalf("commodore Yugabyte schema should preserve CITEXT")
	}
	if !strings.Contains(sqlByDB["quartermaster"], "INET") {
		t.Fatalf("quartermaster Yugabyte schema should preserve INET")
	}
	if !strings.Contains(sqlByDB["skipper"], "amname = 'ybhnsw'") {
		t.Fatalf("skipper schema missing Yugabyte access method detection")
	}
	if !strings.Contains(sqlByDB["skipper"], "USING ybhnsw (embedding vector_cosine_ops)") {
		t.Fatalf("skipper schema missing Yugabyte vector index")
	}
	if !strings.Contains(sqlByDB["skipper"], "USING hnsw (embedding vector_cosine_ops)") {
		t.Fatalf("skipper schema missing PostgreSQL vector index")
	}
}
