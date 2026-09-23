package graph

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"frameworks/api_gateway/internal/appconfig"

	"github.com/99designs/gqlgen/complexity"
	"github.com/99designs/gqlgen/graphql"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/validator"
)

// publicOperationsDir holds every operation the SDKs ship: the generated
// defaults under generated/ and the hand-written documents beside them.
const publicOperationsDir = "../../pkg/graphql/public"

// defaultComplexityLimit reads the GRAPHQL_COMPLEXITY_LIMIT default from the
// typed configuration, so the test follows the value Bridge starts with.
func defaultComplexityLimit(t *testing.T) int {
	t.Helper()
	field, ok := reflect.TypeFor[appconfig.Bridge]().FieldByName("GraphQLComplexityLimit")
	if !ok {
		t.Fatal("appconfig.Bridge has no GraphQLComplexityLimit field")
	}
	limit, err := strconv.Atoi(field.Tag.Get("default"))
	if err != nil || limit <= 0 {
		t.Fatalf("GraphQLComplexityLimit default %q is not a positive limit", field.Tag.Get("default"))
	}
	return limit
}

// loadPublicOperations parses every .graphql document under pkg/graphql/public
// except the schema artifact as one query document against the gateway schema.
func loadPublicOperations(t *testing.T, schema *ast.Schema) *ast.QueryDocument {
	t.Helper()
	var docs []string
	err := filepath.WalkDir(publicOperationsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".graphql" || d.Name() == "schema.public.graphql" {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		docs = append(docs, string(data))
		return nil
	})
	if err != nil {
		t.Fatalf("read public operations: %v", err)
	}
	doc, gqlErr := gqlparser.LoadQueryWithRules(schema, strings.Join(docs, "\n"), nil)
	if gqlErr != nil {
		t.Fatalf("public operations do not validate against the gateway schema: %v", gqlErr)
	}
	if len(doc.Operations) == 0 {
		t.Fatal("no public operations found")
	}
	return doc
}

// placeholderValue builds the smallest valid value for a required variable so
// argument coercion succeeds and each field's complexity function runs.
func placeholderValue(schema *ast.Schema, typ *ast.Type) any {
	if typ.Elem != nil {
		return []any{}
	}
	def := schema.Types[typ.NamedType]
	switch def.Kind {
	case ast.Enum:
		return def.EnumValues[0].Name
	case ast.InputObject:
		obj := map[string]any{}
		for _, f := range def.Fields {
			if f.Type.NonNull && f.DefaultValue == nil {
				obj[f.Name] = placeholderValue(schema, f.Type)
			}
		}
		return obj
	}
	switch typ.NamedType {
	case "Int":
		return 1
	case "Float", "Money":
		return 1.0
	case "Boolean":
		return false
	case "Time":
		return "2026-01-01T00:00:00Z"
	case "JSON":
		return map[string]any{}
	default:
		return "placeholder"
	}
}

// pageArgRecorder records connection fields whose complexity function did not
// run, either because SetupComplexity has none or because argument coercion
// failed. Either way the walker would fall back to an unpaginated cost.
type pageArgRecorder struct {
	graphql.ExecutableSchema
	missed map[string]bool
}

func (r *pageArgRecorder) Complexity(ctx context.Context, typeName, fieldName string, childComplexity int, args map[string]any) (int, bool) {
	cost, ok := r.ExecutableSchema.Complexity(ctx, typeName, fieldName, childComplexity, args)
	if !ok {
		if field := r.Schema().Types[typeName].Fields.ForName(fieldName); field != nil && field.Arguments.ForName("page") != nil {
			r.missed[typeName+"."+fieldName] = true
		}
	}
	return cost, ok
}

type operationScore struct {
	name  string
	score int
}

// TestPublicOperationsUnderComplexityLimit scores every SDK operation with the
// gateway's complexity calculator, variables coerced as the executor does
// (omitted optional variables stay unset, declared defaults apply), and fails
// when one exceeds the default GRAPHQL_COMPLEXITY_LIMIT. An SDK call made with
// default pagination must never be rejected by the gateway it ships against.
func TestPublicOperationsUnderComplexityLimit(t *testing.T) {
	limit := defaultComplexityLimit(t)
	es := &pageArgRecorder{ExecutableSchema: buildComplexitySchema(), missed: map[string]bool{}}
	schema := es.Schema()
	doc := loadPublicOperations(t, schema)

	scores := make([]operationScore, 0, len(doc.Operations))
	for _, op := range doc.Operations {
		provided := map[string]any{}
		for _, v := range op.VariableDefinitions {
			if v.Type.NonNull && v.DefaultValue == nil {
				provided[v.Variable] = placeholderValue(schema, v.Type)
			}
		}
		vars, err := validator.VariableValues(schema, op, provided)
		if err != nil {
			t.Fatalf("%s: coerce variables: %v", op.Name, err)
		}
		score := complexity.Calculate(context.Background(), es, op, vars, complexity.WithFixedScalarValue(0))
		scores = append(scores, operationScore{name: op.Name, score: score})
		if score > limit {
			t.Errorf("%s: complexity %d exceeds the default limit %d", op.Name, score, limit)
		}
	}
	for field := range es.missed {
		t.Errorf("%s takes a page argument but its complexity function did not run; add it to SetupComplexity", field)
	}

	sort.Slice(scores, func(i, j int) bool {
		if scores[i].score != scores[j].score {
			return scores[i].score > scores[j].score
		}
		return scores[i].name < scores[j].name
	})
	for _, s := range scores[:min(10, len(scores))] {
		t.Logf("%6d  %s", s.score, s.name)
	}
}
