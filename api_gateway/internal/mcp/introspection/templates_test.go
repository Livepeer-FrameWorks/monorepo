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

func TestTemplateVariablesRemainAbsent(t *testing.T) {
	loader := NewTemplateLoader()
	template := loader.parseTemplate(`query Q($count: Int = 50, $flag: Boolean = true, $range: String = "default", $id: ID!, $withDefault: Int! = 4) { stream(id: $id) { id } }`, "operations/queries/Q.gql", nil)
	if template == nil {
		t.Fatal("template missing")
	}
	if len(template.Variables) != 0 {
		t.Fatalf("unsupplied variables must be absent: %v", template.Variables)
	}
	if !slices.Equal(template.RequiredVariables, []string{"id"}) {
		t.Fatalf("required variables = %v", template.RequiredVariables)
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
