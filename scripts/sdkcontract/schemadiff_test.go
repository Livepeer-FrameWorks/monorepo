package main

import (
	"strings"
	"testing"

	"github.com/vektah/gqlparser/v2/ast"
)

// compatBase is the released public schema every fixture changes. Clip is
// reachable only as an implementation of Node, Health only in output, Sort
// only in input, and Mode in both.
const compatBase = `
directive @internal(reason: String!) on FIELD_DEFINITION
directive @experimental(reason: String!, until: String!) on FIELD_DEFINITION
schema { query: Query mutation: Mutation }
interface Node { id: ID! }
interface Error { message: String! }
type Stream implements Node {
  id: ID!
  name: String!
  tags: [String!]!
  mode: Mode!
  health: Health
  owner: Owner
  legacy: String @deprecated(reason: "gone")
  preview: String @experimental(reason: "shape may change", until: "v9.0.0")
}
type Owner { id: ID! }
type Clip implements Node { id: ID! }
type NotFoundError implements Error { message: String! }
type ValidationError implements Error { message: String! field: String }
union StreamResult = Stream | NotFoundError | ValidationError
input CreateStreamInput { name: String! record: Boolean mode: Mode = PUSH region: String! = "eu" }
input LabelInput { key: String! }
enum Mode { PUSH PULL }
enum Health { UP DOWN }
enum Sort { ASC DESC }
type Query {
  stream(id: ID!, verbose: Boolean): Stream
  streams(first: Int = 10, sort: Sort): [Stream!]!
  node(id: ID!): Node
  secret: String @internal(reason: "service token")
}
type Mutation {
  createStream(input: CreateStreamInput!): StreamResult!
  label(id: ID!, labels: [LabelInput!]): Stream
}
`

// publicOf loads sdl and filters it like a release schema.
func publicOf(t *testing.T, sdl string) *ast.Schema {
	t.Helper()
	s, err := publicSchema(mustSchema(t, sdl))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func replaced(t *testing.T, old, new string) string {
	t.Helper()
	if !strings.Contains(compatBase, old) {
		t.Fatalf("fixture does not contain %q", old)
	}
	return strings.Replace(compatBase, old, new, 1)
}

func diffAgainstBase(t *testing.T, newSDL string, minimums ...string) schemaDiff {
	t.Helper()
	var mins []*ast.Schema
	for _, m := range minimums {
		mins = append(mins, publicOf(t, m))
	}
	return diffPublicSchemas(publicOf(t, compatBase), publicOf(t, newSDL), mustSchema(t, newSDL), mins)
}

func TestSchemaDiffBreakingRules(t *testing.T) {
	cases := []struct {
		name, old, new, want string
	}{
		{"output field removed", "  owner: Owner\n", "", "Stream.owner was removed"},
		{"unused argument removed", "stream(id: ID!, verbose: Boolean)", "stream(id: ID!)", "argument Query.stream(verbose:) was removed"},
		{"required argument added", "node(id: ID!)", "node(id: ID!, scope: String!)", "argument Query.node(scope:) was added as required without a default"},
		{"argument nullability tightened", "streams(first: Int = 10,", "streams(first: Int!,", "argument Query.streams(first:) changed type from Int to Int!"},
		{"non-null argument lost its default", "record: Boolean mode: Mode = PUSH region: String! = \"eu\"", "record: Boolean mode: Mode = PUSH region: String!", "input field CreateStreamInput.region became required: its default was removed"},
		{"argument default changed", "streams(first: Int = 10,", "streams(first: Int = 20,", "argument Query.streams(first:) changed its default from 10 to 20; a request that omits it changes meaning"},
		{"input field default changed", "mode: Mode = PUSH", "mode: Mode = PULL", "input field CreateStreamInput.mode changed its default from PUSH to PULL; a request that omits it changes meaning"},
		{"non-null input field default changed", "region: String! = \"eu\"", "region: String! = \"us\"", "input field CreateStreamInput.region changed its default from \"eu\" to \"us\"; a request that omits it changes meaning"},
		{"nullable argument lost its default", "streams(first: Int = 10,", "streams(first: Int,", "argument Query.streams(first:) lost its default 10; a request that omits it no longer gets that value"},
		{"nullable input field lost its default", "mode: Mode = PUSH", "mode: Mode", "input field CreateStreamInput.mode lost its default PUSH; a request that omits it no longer gets that value"},
		{"input type removed", "labels: [LabelInput!]", "labels: [String!]", "type LabelInput was removed"},
		{"input field removed", "record: Boolean ", "", "input field CreateStreamInput.record was removed"},
		{"required input field added", "input LabelInput { key: String! }", "input LabelInput { key: String! value: String! }", "input field LabelInput.value was added as required without a default"},
		{"output nullability loosened", "  name: String!\n", "  name: String\n", "Stream.name changed type from String! to String"},
		{"input nullability tightened", "record: Boolean ", "record: Boolean! ", "input field CreateStreamInput.record changed type from Boolean to Boolean!"},
		{"named type changed", "  owner: Owner\n", "  owner: Clip\n", "Stream.owner changed type from Owner to Clip"},
		{"list nesting changed", "tags: [String!]!", "tags: [[String!]!]!", "Stream.tags changed type from [String!]! to [[String!]!]!"},
		{"input enum value removed", "enum Sort { ASC DESC }", "enum Sort { ASC }", "enum value Sort.DESC, accepted as input, was removed"},
		{"output enum value removed", "enum Health { UP DOWN }", "enum Health { UP }", "enum value Health.DOWN, returned, was removed"},
		{"enum in both positions", "enum Mode { PUSH PULL }", "enum Mode { PUSH }", "enum value Mode.PULL, returned and accepted as input, was removed"},
		{"union member removed", "union StreamResult = Stream | NotFoundError | ValidationError", "union StreamResult = Stream | ValidationError", "union StreamResult lost member NotFoundError; fragments on NotFoundError inside its selections no longer validate"},
		{"interface implementation removed", "type Clip implements Node { id: ID! }", "type Clip { id: ID! }", "interface Node lost implementation Clip; fragments on Clip inside its selections no longer validate"},
		{"deprecated field removed without a tagged minimum", "  legacy: String @deprecated(reason: \"gone\")\n", "", "Stream.legacy was removed"},
		{"stable field became experimental", "  owner: Owner\n", "  owner: Owner @experimental(reason: \"reworked\", until: \"v9.0.0\")\n", "Stream.owner became @experimental; a stable field cannot leave the compatibility contract"},
		{"field became internal", "  owner: Owner\n", "  owner: Owner @internal(reason: \"operator\")\n", "Stream.owner was removed (now @internal)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diff := diffAgainstBase(t, replaced(t, tc.old, tc.new))
			if !contains(diff.Breaking, tc.want) {
				t.Fatalf("breaking = %v, want %q", diff.Breaking, tc.want)
			}
		})
	}
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func TestSchemaDiffRemovedUnionMemberBreaksInlineFragment(t *testing.T) {
	newSDL := replaced(t, "union StreamResult = Stream | NotFoundError | ValidationError", "union StreamResult = Stream | ValidationError")
	doc := `mutation M { createStream(input: {name: "a"}) { __typename ... on NotFoundError { message } } }`
	if err := validates(publicOf(t, compatBase), doc); err != nil {
		t.Fatalf("fixture document must validate on the released schema: %v", err)
	}
	if err := validates(publicOf(t, newSDL), doc); err == nil {
		t.Fatal("the inline fragment still validates; the fixture does not show the break")
	}
	if diff := diffAgainstBase(t, newSDL); !contains(diff.Breaking, "union StreamResult lost member NotFoundError; fragments on NotFoundError inside its selections no longer validate") {
		t.Fatalf("breaking = %v", diff.Breaking)
	}
}

func TestSchemaDiffCompatibleChanges(t *testing.T) {
	cases := []struct {
		name, old, new, info string
	}{
		{"output nullability tightened", "  owner: Owner\n", "  owner: Owner!\n", "Stream.owner changed type from Owner to Owner! (compatible)"},
		{"input nullability loosened", "input LabelInput { key: String! }", "input LabelInput { key: String }", "input field LabelInput.key changed type from String! to String (compatible)"},
		{"optional argument added", "node(id: ID!)", "node(id: ID!, scope: String)", "argument Query.node(scope:) was added"},
		{"required argument with default added", "node(id: ID!)", "node(id: ID!, scope: String! = \"all\")", "argument Query.node(scope:) was added"},
		{"required input field gained a default", "input CreateStreamInput { name: String! ", "input CreateStreamInput { name: String! = \"untitled\" ", "input field CreateStreamInput.name gained the default \"untitled\""},
		{"optional argument gained a default", "stream(id: ID!, verbose: Boolean)", "stream(id: ID!, verbose: Boolean = false)", "argument Query.stream(verbose:) gained the default false"},
		{"default respelled as a block string", "region: String! = \"eu\"", "region: String! = \"\"\"eu\"\"\"", ""},
		{"optional input field added", "input LabelInput { key: String! }", "input LabelInput { key: String! value: String }", "input field LabelInput.value was added"},
		{"field added", "type Owner { id: ID! }", "type Owner { id: ID! name: String }", "Owner.name was added"},
		{"type added", "type Owner { id: ID! }", "type Owner { id: ID! team: Team }\ntype Team { id: ID! }", "type Team was added"},
		{"enum value added", "enum Health { UP DOWN }", "enum Health { UP DOWN DEGRADED }", "enum value Health.DEGRADED was added"},
		{"union member added", "union StreamResult = Stream | NotFoundError | ValidationError", "union StreamResult = Stream | NotFoundError | ValidationError | Clip", "union StreamResult gained member Clip"},
		{"experimental field changed", "preview: String @experimental", "preview: Int! @experimental", "Stream.preview changed type from String to Int! (experimental, not checked)"},
		{"experimental field removed", "  preview: String @experimental(reason: \"shape may change\", until: \"v9.0.0\")\n", "", "Stream.preview was removed (experimental, not checked)"},
		{"new experimental field", "type Owner { id: ID! }", "type Owner { id: ID! team: String @experimental(reason: \"new\", until: \"v9.0.0\") }", "Owner.team was added"},
		{"internal field removed", "  secret: String @internal(reason: \"service token\")\n", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diff := diffAgainstBase(t, replaced(t, tc.old, tc.new))
			if len(diff.Breaking) != 0 {
				t.Fatalf("breaking = %v, want none", diff.Breaking)
			}
			if tc.info != "" && !contains(diff.Info, tc.info) {
				t.Fatalf("info = %v, want %q", diff.Info, tc.info)
			}
		})
	}
}

func TestSchemaDiffDeprecatedAtEveryMinimumMayGo(t *testing.T) {
	withoutLegacy := replaced(t, "  legacy: String @deprecated(reason: \"gone\")\n", "")
	if diff := diffAgainstBase(t, withoutLegacy, compatBase); len(diff.Breaking) != 0 {
		t.Fatalf("removal of a field deprecated at the minimum reported: %v", diff.Breaking)
	}

	// A second live line whose minimum still has the field undeprecated
	// keeps it in the contract.
	undeprecated := replaced(t, "legacy: String @deprecated(reason: \"gone\")", "legacy: String")
	if diff := diffAgainstBase(t, withoutLegacy, compatBase, undeprecated); !contains(diff.Breaking, "Stream.legacy was removed") {
		t.Fatalf("breaking = %v", diff.Breaking)
	}

	depArg := strings.Replace(compatBase, "verbose: Boolean)", "verbose: Boolean @deprecated(reason: \"always verbose\"))", 1)
	depValue := strings.Replace(compatBase, "enum Sort { ASC DESC }", "enum Sort { ASC DESC @deprecated(reason: \"use ASC\") }", 1)
	depInput := strings.Replace(compatBase, "record: Boolean ", "record: Boolean @deprecated(reason: \"always on\") ", 1)
	for _, tc := range []struct{ name, minimum, old, new string }{
		{"argument", depArg, "stream(id: ID!, verbose: Boolean)", "stream(id: ID!)"},
		{"enum value", depValue, "enum Sort { ASC DESC }", "enum Sort { ASC }"},
		{"input field", depInput, "record: Boolean ", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if diff := diffAgainstBase(t, replaced(t, tc.old, tc.new), tc.minimum); len(diff.Breaking) != 0 {
				t.Fatalf("breaking = %v, want none", diff.Breaking)
			}
		})
	}
}

// A type reached only through a deprecated field may change with it.
func TestSchemaDiffDoesNotWalkThroughExemptFields(t *testing.T) {
	minimum := replaced(t, "  owner: Owner\n", "  owner: Owner @deprecated(reason: \"use team\")\n")
	newSDL := replaced(t, "type Owner { id: ID! }", "type Owner { key: ID! }")
	if diff := diffAgainstBase(t, newSDL, minimum); len(diff.Breaking) != 0 {
		t.Fatalf("breaking = %v, want none", diff.Breaking)
	}
	if diff := diffAgainstBase(t, newSDL); !contains(diff.Breaking, "Owner.id was removed") {
		t.Fatalf("breaking = %v", diff.Breaking)
	}
}

func TestSchemaCompatReportOnlyBeforeTheBaseline(t *testing.T) {
	support := &supportFile{Current: "0.2", Lines: []supportLine{
		{Line: "0.2", Status: lineLive, MinServer: "v0.3.12"},
		{Line: "0.1", Status: lineLive, MinServer: "v0.3.11"},
		{Line: "0.0", Status: lineRetired, MinServer: "v0.2.0"},
	}}
	baseline := contractBaseline(support)
	if baseline.String() != "v0.3.11" {
		t.Fatalf("baseline = %s, want the oldest live min_server v0.3.11", baseline)
	}
	diff := diffAgainstBase(t, replaced(t, "stream(id: ID!, verbose: Boolean)", "stream(id: ID!)"))
	if len(diff.Breaking) == 0 {
		t.Fatal("fixture has no breaking change")
	}
	v0310, _ := parseSemver("v0.3.10")
	v0311, _ := parseSemver("v0.3.11")
	v0312, _ := parseSemver("v0.3.12")
	if err := schemaCompatVerdict(diff, v0310, baseline); err != nil {
		t.Fatalf("report-only mode failed: %v", err)
	}
	for _, latest := range []semver{v0311, v0312} {
		if err := schemaCompatVerdict(diff, latest, baseline); err == nil || !strings.Contains(err.Error(), "argument Query.stream(verbose:) was removed") {
			t.Fatalf("enforce mode at %s: err = %v", latest, err)
		}
	}
	if err := schemaCompatVerdict(schemaDiff{Info: []string{"type X was added"}}, v0311, baseline); err != nil {
		t.Fatalf("additions failed the gate: %v", err)
	}
}

func TestNormalizedValueComparesEqualLiterals(t *testing.T) {
	parse := func(sdl string) *ast.Value {
		t.Helper()
		s := mustSchema(t, "input P { a: Int b: String }\ntype Query { f(x: P = "+sdl+"): Int }")
		return s.Query.Fields.ForName("f").Arguments.ForName("x").DefaultValue
	}
	if a, b := normalizedValue(parse(`{a: 1, b: "x"}`)), normalizedValue(parse(`{b: """x""", a: 1}`)); a != b {
		t.Fatalf("equal object literals normalize to %s and %s", a, b)
	}
	if a, b := normalizedValue(parse(`{a: 1}`)), normalizedValue(parse(`{a: 2}`)); a == b {
		t.Fatalf("different object literals both normalize to %s", a)
	}
	if got := normalizedValue(nil); got != "" {
		t.Fatalf("no default normalizes to %q", got)
	}
}
