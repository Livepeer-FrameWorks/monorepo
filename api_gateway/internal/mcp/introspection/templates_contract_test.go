package introspection

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	graphqlpkg "github.com/Livepeer-FrameWorks/monorepo/pkg/graphql"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/validator"
)

func TestPrimaryFieldPath(t *testing.T) {
	for _, tc := range []struct{ query, want string }{
		{`query { stream { id name metrics { status } } }`, "stream"},
		{`query { s: stream { __typename id targets: pushTargets { id name } } }`, "stream.pushTargets"},
		{`query { tenant { id } clustersConnection { totalCount } }`, ""},
		{`query { streamsConnection { nodes { id } pageInfo { hasNextPage } } }`, "streamsConnection"},
		{`query { stream { ...Wrapper } } fragment Wrapper on Stream { id ...on Stream { pushTargets { id name } } }`, "stream.pushTargets"},
		{`query { stream { ...Missing } }`, "stream"},
	} {
		if got := primaryFieldPath(tc.query); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.query, got, tc.want)
		}
	}
}

func TestTemplateLoadFailureDoesNotPublishPartialState(t *testing.T) {
	files := fstest.MapFS{
		"operations/queries/A.gql": {Data: []byte(`query A { tenant { id } }`)},
		"operations/queries/Z.gql": {Data: []byte(`query {`)},
	}
	loader := NewTemplateLoader()
	if err := loader.loadFrom(files, []byte(`{"operations":[]}`)); err == nil {
		t.Fatal("invalid template accepted")
	}
	if len(loader.GetAll()) != 0 || loader.FindByField("query", "tenant") != nil {
		t.Fatal("partial template state published")
	}
	delete(files, "operations/queries/Z.gql")
	for range 2 {
		if err := loader.loadFrom(files, []byte(`{"operations":[]}`)); err != nil {
			t.Fatal(err)
		}
		if len(loader.GetAll()) != 1 {
			t.Fatal("retry duplicated templates")
		}
	}
}

func TestSDKLabelsIdentifyTheReturnedDocument(t *testing.T) {
	var catalog struct {
		Operations []struct{ Name, Document string }
	}
	if err := json.Unmarshal(graphqlpkg.PublicCatalog, &catalog); err != nil {
		t.Fatal(err)
	}
	documents := map[string]string{}
	for _, op := range catalog.Operations {
		documents[op.Name] = resolveFragments(op.Document, nil)
	}
	loader := NewTemplateLoader()
	if err := loader.Load(); err != nil {
		t.Fatal(err)
	}
	for _, template := range loader.GetAll() {
		for _, name := range template.SDKOperations {
			if resolveFragments(template.Query, nil) != documents[name] {
				t.Errorf("%s is incorrectly labelled as SDK operation %s", template.Name, name)
			}
		}
	}
	for _, field := range []string{"stream", "tenant"} {
		template := loader.FindByField("query", field)
		if template.Source != "sdk" && len(template.SDKOperations) != 0 {
			t.Errorf("application %s has unrelated SDK labels: %v", field, template.SDKOperations)
		}
	}
}

func TestFieldLookupPrefersSDKOverIncidentalApplicationSelection(t *testing.T) {
	loader := NewTemplateLoader()
	if err := loader.Load(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ field, name string }{{"stream", "GetStream"}, {"tenant", "GetTenant"}} {
		t.Run(tc.field, func(t *testing.T) {
			got := loader.FindByField("query", tc.field)
			if got == nil || got.Name != tc.name || got.Source != "sdk" {
				t.Fatalf("%s returned %+v, want exact SDK %s", tc.field, got, tc.name)
			}
		})
	}
	if got := loader.FindByField("query", "stream.pushTargets"); got.Name != "GetPushTargets" {
		t.Fatalf("push-target template replaced: %s", got.Name)
	}
	if got := loader.FindByField("query", "streamsConnection"); got.Name != "GetStreamsConnection" {
		t.Fatalf("connection template replaced: %s", got.Name)
	}
}

func TestStreamsTemplateDoesNotOverrideDefaults(t *testing.T) {
	loader := NewTemplateLoader()
	if err := loader.Load(); err != nil {
		t.Fatal(err)
	}
	template := loader.FindByField("query", "streamsConnection")
	if template == nil {
		t.Fatal("streamsConnection template missing")
	}
	if got, present := template.Variables["first"]; present {
		t.Fatalf("GetStreamsConnection declares first=50; generated variables override it with first=%v", got)
	}
}

func TestClientDirectivesAreRemovedFromFragmentClosure(t *testing.T) {
	query := `query Q($skip: Boolean = false) { node(id: "@paginate") { ...on Stream { ...Fields @mask_disable name @skip(if: $skip) } } }`
	fragments := map[string]string{"Fields": `fragment Fields on Stream { id @required }`}
	out := resolveFragments(query, fragments)
	if strings.Contains(out, "@mask_disable") || strings.Contains(out, "@required") || !strings.Contains(out, "@skip") || !strings.Contains(out, `"@paginate"`) {
		t.Fatalf("normalization changed server directives or literals: %s", out)
	}
}

func TestTemplateVariablesPreserveGraphQLCoercion(t *testing.T) {
	schema, err := gqlparser.LoadSchema(&ast.Source{Input: `type Query { value(n: Int, enabled: Boolean, text: String): String }`})
	if err != nil {
		t.Fatal(err)
	}
	doc, queryErrors := gqlparser.LoadQueryWithRules(schema, `query Q($n: Int! = 50, $enabled: Boolean = true, $text: String = "default") { value(n: $n, enabled: $enabled, text: $text) }`, nil)
	if len(queryErrors) > 0 {
		t.Fatal(queryErrors)
	}
	for _, tc := range []struct {
		name        string
		supplied    map[string]any
		wantN       any
		wantEnabled any
		wantText    any
		invalid     bool
	}{
		{"absent", map[string]any{}, int64(50), true, "default", false},
		{"explicit", map[string]any{"n": 0, "enabled": false, "text": nil}, 0, false, nil, false},
		{"nonnull-null", map[string]any{"n": nil}, nil, nil, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values, err := validator.VariableValues(schema, doc.Operations[0], tc.supplied)
			if tc.invalid {
				if err == nil {
					t.Fatal("null must not use the non-null variable default")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(values["n"], tc.wantN) || !reflect.DeepEqual(values["enabled"], tc.wantEnabled) || !reflect.DeepEqual(values["text"], tc.wantText) {
				t.Fatalf("coerced variables: %#v", values)
			}
		})
	}
}

func TestAllTemplatesValidateOnBridge(t *testing.T) {
	loader := NewTemplateLoader()
	if err := loader.Load(); err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile("../../../../pkg/graphql/schema.graphql")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "schema.graphql", Input: string(source)})
	if err != nil {
		t.Fatal(err)
	}
	for _, template := range loader.GetAll() {
		t.Run(template.Name, func(t *testing.T) {
			if _, errs := gqlparser.LoadQueryWithRules(schema, template.Query, nil); len(errs) > 0 {
				t.Fatalf("generate_query returned a document Bridge rejects: %v", errs)
			}
		})
	}
}
