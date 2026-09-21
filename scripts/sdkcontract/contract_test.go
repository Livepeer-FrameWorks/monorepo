package main

import (
	"strings"
	"testing"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

const baseSchema = `
schema { query: Query mutation: Mutation }
interface Error { message: String! code: String }
type ValidationError implements Error { message: String! code: String field: String }
type NotFoundError implements Error { message: String! code: String resourceId: ID! }
type Stream { id: ID! name: String! legacy: String @deprecated(reason: "gone") }
union StreamResult = Stream | ValidationError | NotFoundError
input CreateStreamInput { name: String! record: Boolean mode: Mode }
enum Mode { PUSH PULL }
type Query { stream(id: ID!): Stream streams(first: Int): [Stream!]! }
type Mutation { createStream(input: CreateStreamInput!): StreamResult! }
`

func mustSchema(t *testing.T, text string) *ast.Schema {
	t.Helper()
	s, err := gqlparser.LoadSchema(&ast.Source{Input: text})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func mustOps(t *testing.T, text string) []operation {
	t.Helper()
	ops, err := parseOperations([]*ast.Source{{Name: "test.graphql", Input: text}})
	if err != nil {
		t.Fatal(err)
	}
	return ops
}

func TestLintRequiresTypenameAndEveryErrorMember(t *testing.T) {
	schema := mustSchema(t, baseSchema)
	ops := mustOps(t, `
fragment NF on NotFoundError { message resourceId }
mutation Missing($input: CreateStreamInput!) { createStream(input: $input) { ... on Stream { id } ... on ValidationError { message } } }
mutation Complete($input: CreateStreamInput!) { createStream(input: $input) { __typename ... on Stream { id } ... on ValidationError { message } ...NF } }
`)
	problems := lintOperations(schema, ops)
	joined := strings.Join(problems, "\n")
	if !strings.Contains(joined, "Missing (test.graphql): createStream: union StreamResult selection does not select __typename") {
		t.Fatalf("missing __typename not reported:\n%s", joined)
	}
	if !strings.Contains(joined, "does not select error member NotFoundError") {
		t.Fatalf("missing error member not reported:\n%s", joined)
	}
	if strings.Contains(joined, "Complete") {
		t.Fatalf("complete operation reported:\n%s", joined)
	}
}

func TestLintRejectsDeprecatedFields(t *testing.T) {
	problems := lintOperations(mustSchema(t, baseSchema), mustOps(t, `query Old { stream(id: "1") { legacy } }`))
	if len(problems) != 1 || !strings.Contains(problems[0], "legacy is deprecated") {
		t.Fatalf("problems = %v", problems)
	}
}

func TestOperationDocumentsCarryOnlyTheirFragments(t *testing.T) {
	ops := mustOps(t, `
fragment A on Stream { id }
fragment B on Stream { name }
query One { stream(id: "1") { ...A } }
query Two { stream(id: "2") { ...B } }
`)
	if len(ops) != 2 || strings.Contains(ops[0].Document, "fragment B") || !strings.Contains(ops[0].Document, "fragment A") {
		t.Fatalf("documents = %#v", ops)
	}
	if ops[0].Hash == ops[1].Hash {
		t.Fatal("different operations share a hash")
	}
}

func usageOf(t *testing.T, schema *ast.Schema, doc string) *usage {
	t.Helper()
	u := newUsage()
	if err := collectUsage(schema, doc, u); err != nil {
		t.Fatal(err)
	}
	return u
}

func TestBreakingChangesDetectsRemovalNullabilityAndInputs(t *testing.T) {
	oldS := mustSchema(t, baseSchema)
	doc := `mutation M($input: CreateStreamInput!) { createStream(input: $input) { __typename ... on Stream { id name } } }
query Q { streams(first: 1) { id } }`
	u := usageOf(t, oldS, doc)

	cases := []struct {
		name    string
		schema  string
		want    string
		exempt  bool
		wantNil bool
	}{
		{name: "field removed", schema: strings.Replace(baseSchema, "name: String! legacy", "legacy", 1), want: "Stream.name was removed"},
		{name: "non-null became nullable", schema: strings.Replace(baseSchema, "type Stream { id: ID! name: String!", "type Stream { id: ID! name: String", 1), want: "Stream.name changed type from String! to String"},
		{name: "required argument added", schema: strings.Replace(baseSchema, "streams(first: Int)", "streams(first: Int, tenant: ID!)", 1), want: "Query.streams gained required argument tenant"},
		{name: "argument became required", schema: strings.Replace(baseSchema, "streams(first: Int)", "streams(first: Int!)", 1), want: "argument Query.streams(first) changed type from Int to Int!"},
		{name: "input field became required", schema: strings.Replace(baseSchema, "record: Boolean mode", "record: Boolean! mode", 1), want: "input field CreateStreamInput.record changed type"},
		{name: "input gained required field", schema: strings.Replace(baseSchema, "mode: Mode }", "mode: Mode region: String! }", 1), want: "input CreateStreamInput gained required field region"},
		{name: "input enum value removed", schema: strings.Replace(baseSchema, "enum Mode { PUSH PULL }", "enum Mode { PUSH }", 1), want: "enum value Mode.PULL, accepted as input, was removed"},
		{name: "nullable became non-null is safe", schema: strings.Replace(baseSchema, "stream(id: ID!): Stream ", "stream(id: ID!): Stream! ", 1), wantNil: true},
		{name: "unused field removed is safe", schema: strings.Replace(baseSchema, " legacy: String @deprecated(reason: \"gone\")", "", 1), wantNil: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := breakingChanges(oldS, mustSchema(t, tc.schema), nil, u)
			if tc.wantNil {
				if len(got) != 0 {
					t.Fatalf("got %v, want none", got)
				}
				return
			}
			if !strings.Contains(strings.Join(got, "\n"), tc.want) {
				t.Fatalf("got %v, want %q", got, tc.want)
			}
		})
	}
}

func TestBreakingChangesExemptsFieldsDeprecatedAtTheMinimum(t *testing.T) {
	deprecatedAtMin := strings.Replace(baseSchema, "name: String!", `name: String! @deprecated(reason: "use title")`, 1)
	oldS := mustSchema(t, deprecatedAtMin)
	u := usageOf(t, oldS, `query Q { stream(id: "1") { name } }`)
	removed := mustSchema(t, strings.Replace(baseSchema, "name: String! legacy", "legacy", 1))
	if got := breakingChanges(oldS, removed, oldS, u); len(got) != 0 {
		t.Fatalf("removal of a field deprecated at the minimum reported: %v", got)
	}
	if got := breakingChanges(oldS, removed, nil, u); len(got) == 0 {
		t.Fatal("removal without a tagged minimum must be reported")
	}
}

func TestSemverOrdering(t *testing.T) {
	a, _ := parseSemver("v0.3.9")
	b, _ := parseSemver("v0.3.11")
	if !a.Less(b) || b.Less(a) {
		t.Fatal("v0.3.9 must sort before v0.3.11")
	}
	for _, v := range []string{"v0.3.11-rc1", "dev", "0.3.11"} {
		if _, ok := parseSemver(v); ok {
			t.Fatalf("%s parsed as a stable release", v)
		}
	}
}
