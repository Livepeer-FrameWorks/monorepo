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
	// Two operations may not target one field, so each is its own set.
	ops := append(mustOps(t, `
mutation Missing($input: CreateStreamInput!) { createStream(input: $input) { ... on Stream { id } ... on ValidationError { message } } }
`), mustOps(t, `
fragment NF on NotFoundError { message resourceId }
mutation Complete($input: CreateStreamInput!) { createStream(input: $input) { __typename ... on Stream { id } ... on ValidationError { message } ...NF } }
`)...)
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
query Two { streams(first: 2) { ...B } }
`)
	if len(ops) != 2 || strings.Contains(ops[0].Document, "fragment B") || !strings.Contains(ops[0].Document, "fragment A") {
		t.Fatalf("documents = %#v", ops)
	}
	if ops[0].Hash == ops[1].Hash {
		t.Fatal("different operations share a hash")
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
