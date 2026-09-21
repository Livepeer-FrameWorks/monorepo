package configschema

import (
	"slices"
	"testing"
)

func TestEmbeddedSchemaCoversMigratedServices(t *testing.T) {
	schema, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, service := range []string{"bridge", "commodore", "quartermaster", "periscope-query"} {
		section, ok := schema.Server(service)
		if !ok {
			t.Fatalf("schema has no server section for %s", service)
		}
		if len(section.Variables) == 0 || section.Command == "" {
			t.Fatalf("%s section is empty: %+v", service, section)
		}
		if !slices.Contains(schema.RequiredEnv(service), "SERVICE_TOKEN") {
			t.Fatalf("%s must require SERVICE_TOKEN, got %v", service, schema.RequiredEnv(service))
		}
	}
	if !slices.Contains(schema.RequiredEnv("periscope-query"), "CLICKHOUSE_ADDR") {
		t.Fatal("periscope-query must require CLICKHOUSE_ADDR")
	}
	for _, key := range []string{"SERVICE_TOKEN", "LOOKOUT_ALERTMANAGER_TOKEN", "KAFKA_BROKERS"} {
		if !slices.Contains(schema.RequiredEnv("lookout"), key) {
			t.Fatalf("lookout must require %s, got %v", key, schema.RequiredEnv("lookout"))
		}
	}
	if schema.RequiredEnv("not-a-service") != nil {
		t.Fatal("services without typed configuration must report no required list")
	}
}

func TestMissingCoversServerAndVariantsAndTreatsBlankAsUnset(t *testing.T) {
	schema := Schema{Services: []Section{
		{Service: "svc", Variables: []Variable{
			{Key: "A", Required: true},
			{Key: "OPTIONAL"},
			{Key: "B", Required: true},
		}},
		{Service: "svc", Variant: "bootstrap", Variables: []Variable{
			{Key: "B", Required: true},
			{Key: "C", Required: true},
		}},
		{Service: "other", Variables: []Variable{{Key: "D", Required: true}}},
	}}

	got := schema.Missing("svc", map[string]string{"A": "set", "B": " \t"})
	if want := []string{"B", "C"}; !slices.Equal(got, want) {
		t.Fatalf("Missing = %v, want %v", got, want)
	}
	if got := schema.Missing("svc", map[string]string{"A": "a", "B": "b", "C": "c"}); len(got) != 0 {
		t.Fatalf("complete env reported missing %v", got)
	}
	if got := schema.Missing("unknown", nil); got != nil {
		t.Fatalf("unknown service reported missing %v", got)
	}
}

func TestMissingIncludesEmbeddedVariantRequirements(t *testing.T) {
	schema, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	env := map[string]string{}
	for _, key := range schema.RequiredEnv("purser") {
		env[key] = "set"
	}
	delete(env, "DATABASE_URL")
	if got := schema.Missing("purser", env); !slices.Equal(got, []string{"DATABASE_URL"}) {
		t.Fatalf("purser Missing = %v, want [DATABASE_URL]", got)
	}
}
