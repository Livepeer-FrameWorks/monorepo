package introspection

import (
	"slices"
	"strings"
	"testing"
)

func TestExtractFieldName(t *testing.T) {
	tests := []struct {
		query string
		want  string
	}{
		{"query Foo { streams { id } }", "streams"},
		{"{ me }", "me"},
		{"no-braces-here", ""},
	}
	for _, tt := range tests {
		if got := extractFieldName(tt.query); got != tt.want {
			t.Fatalf("extractFieldName(%q) = %q, want %q", tt.query, got, tt.want)
		}
	}
}

func TestExtractFieldPaths(t *testing.T) {
	// A leaf nested field (no sub-selection) is NOT recorded as its own path;
	// only top-level fields and fields that themselves have sub-selections are.
	q := `query Q { streams { id status profile { name } } me }`
	paths := extractFieldPaths(q)
	got := strings.Join(paths, ",")
	for _, want := range []string{"me", "streams", "streams.profile"} {
		if !slices.Contains(paths, want) {
			t.Fatalf("paths %q missing %q", got, want)
		}
	}
	// Leaf nested fields are intentionally absent.
	if slices.Contains(paths, "streams.id") {
		t.Fatalf("leaf field streams.id should not be a recorded path: %q", got)
	}

	// Malformed query parses to nil rather than panicking.
	if extractFieldPaths("query { {{{ ") != nil {
		t.Fatal("expected nil for unparseable query")
	}
}

func TestExtractFieldPaths_SkipsTypename(t *testing.T) {
	paths := extractFieldPaths(`{ stream { __typename id } }`)
	for _, p := range paths {
		if strings.Contains(p, "__typename") {
			t.Fatalf("__typename should be skipped, got %v", paths)
		}
	}
}

func TestExtractDescription(t *testing.T) {
	// Each "#" prefix is stripped but the leading space in the comment body is
	// kept, so two comment lines join with a double space.
	content := "# First line\n# Second line\nquery Foo { x }"
	if got := extractDescription(content); got != "First line  Second line" {
		t.Fatalf("description = %q", got)
	}
	if got := extractDescription("query NoComment { x }"); got != "" {
		t.Fatalf("expected empty description, got %q", got)
	}
}

func TestExtractDefaultVariables(t *testing.T) {
	content := `query Q($page: Pagination, $streamId: ID, $name: String, $count: Int, $flag: Boolean, $id: ID, $filter: StreamFilterInput, $tr: timeRange) {}`
	// Note the parser keys on variable NAME first (page/streamId/nodeId/timeRange),
	// then falls back to TYPE. Verify the name-keyed special cases.
	vars := extractDefaultVariables(`query Q($page: Foo, $streamId: ID, $nodeId: ID, $timeRange: TR) {}`)
	if _, ok := vars["page"].(map[string]any); !ok {
		t.Fatalf("page should default to a pagination map, got %T", vars["page"])
	}
	if vars["streamId"] != "stream_global_id" {
		t.Fatalf("streamId default = %v", vars["streamId"])
	}
	if vars["nodeId"] != "node_id" {
		t.Fatalf("nodeId default = %v", vars["nodeId"])
	}
	if vars["timeRange"] != nil {
		t.Fatalf("timeRange default should be nil, got %v", vars["timeRange"])
	}

	// Type-keyed fallbacks.
	vars = extractDefaultVariables(content)
	if vars["name"] != "" {
		t.Fatalf("String → empty string, got %v", vars["name"])
	}
	if vars["count"] != 0 {
		t.Fatalf("Int → 0, got %v", vars["count"])
	}
	if vars["flag"] != false {
		t.Fatalf("Boolean → false, got %v", vars["flag"])
	}
	if vars["id"] != "id_placeholder" {
		t.Fatalf("ID → placeholder, got %v", vars["id"])
	}
	if _, ok := vars["filter"].(map[string]any); !ok {
		t.Fatalf("*Input → empty map, got %T", vars["filter"])
	}
}

func TestResolveFragments(t *testing.T) {
	frags := map[string]string{
		"StreamFields": "fragment StreamFields on Stream { id ...NestedFields }",
		"NestedFields": "fragment NestedFields on Stream { name }",
		"Unused":       "fragment Unused on X { y }",
	}
	out := resolveFragments("query Q { stream { ...StreamFields } }", frags)
	// Spread fragment and its transitive dependency are inlined; unused is not.
	if !strings.Contains(out, "fragment StreamFields") {
		t.Fatal("expected StreamFields appended")
	}
	if !strings.Contains(out, "fragment NestedFields") {
		t.Fatal("expected transitively-required NestedFields appended")
	}
	if strings.Contains(out, "fragment Unused") {
		t.Fatal("unused fragment should not be appended")
	}
}

func TestCleanPath(t *testing.T) {
	if got := cleanPath("a/b/operations/queries/x.gql"); got != "operations/queries/x.gql" {
		t.Fatalf("cleanPath = %q", got)
	}
	if got := cleanPath("nofolder.gql"); got != "nofolder.gql" {
		t.Fatalf("cleanPath passthrough = %q", got)
	}
}

// Load() runs against the real embedded operations FS — this is a regression net
// that every shipped .gql operation parses and indexes without error.
func TestTemplateLoader_LoadRealOperations(t *testing.T) {
	tl := NewTemplateLoader()
	if err := tl.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Idempotent: second Load is a no-op.
	if err := tl.Load(); err != nil {
		t.Fatalf("second Load: %v", err)
	}

	all := tl.GetAll()
	if len(all) == 0 {
		t.Fatal("expected at least one template loaded from embedded operations")
	}
	for _, tmpl := range all {
		if tmpl.Name == "" {
			t.Errorf("template %s has empty operation name", tmpl.FilePath)
		}
		if tmpl.OperationType == "" {
			t.Errorf("template %s has empty operation type", tmpl.FilePath)
		}
		// Every indexed template must be retrievable by at least one of its paths.
		found := slices.ContainsFunc(tmpl.FieldPaths, func(fp string) bool {
			return fp != "" && tl.FindByField(tmpl.OperationType, fp) != nil
		})
		if len(tmpl.FieldPaths) > 0 && !found {
			t.Errorf("template %s not retrievable via FindByField", tmpl.Name)
		}
	}

	// Unknown lookup returns nil, not panic.
	if tl.FindByField("query", "definitely_not_a_field_xyz") != nil {
		t.Fatal("expected nil for unknown field")
	}
}
