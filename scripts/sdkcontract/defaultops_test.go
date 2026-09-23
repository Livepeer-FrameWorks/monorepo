package main

import (
	"strings"
	"testing"

	"github.com/vektah/gqlparser/v2/ast"
)

const defaultOpsSchema = `
schema { query: Query mutation: Mutation subscription: Subscription }
interface Error { message: String! code: String }
type ValidationError implements Error { message: String! code: String field: String }
type PlacementError { message: String! code: PlacementCode! }
enum PlacementCode { DENIED }
type PageInfo { startCursor: String endCursor: String hasNextPage: Boolean! hasPreviousPage: Boolean! }
input ConnectionInput { first: Int after: String }
type Query {
  stream(id: ID!): Stream
  streamsConnection(page: ConnectionInput): StreamConnection!
  analytics: Analytics!
  summary: Summary
  old: String @deprecated(reason: "gone")
}
type Mutation { createStream(name: String!): StreamResult! ping: Boolean! }
type Subscription { streamEvents(streamId: ID): Stream }
union StreamResult = Stream | ValidationError | PlacementError
type Stream {
  id: ID!
  name: String!
  legacy: String @deprecated(reason: "gone")
  owner: Owner
  clip(id: ID!, limit: Int! = 10): Clip
  clips(page: ConnectionInput): [Clip!]!
}
type Clip { id: ID! title: String }
type Owner { id: ID! org: Org }
type Org { name: String! parent: Org }
type StreamConnection { edges: [StreamEdge!]! nodes: [Stream!]! pageInfo: PageInfo! totalCount: Int! }
type StreamEdge { cursor: String! node: Stream! }
type Analytics { usage: Usage! }
type Usage { summary(streamId: ID!): Summary }
type Summary { views: Int! }
`

// generateFixture generates the default operations of defaultOpsSchema next
// to handWritten and loads the complete set.
func generateFixture(t *testing.T, handWritten string) (map[string]string, []operation) {
	t.Helper()
	schema := mustSchema(t, defaultOpsSchema)
	hand := []*ast.Source{{Name: "pkg/graphql/public/queries/hand.graphql", Input: handWritten}}
	handOps, err := parseOperations(schema, hand)
	if err != nil {
		t.Fatal(err)
	}
	files, _, err := generateDefaultOps(schema, handOps)
	if err != nil {
		t.Fatal(err)
	}
	all, err := parseOperations(schema, append(hand, generatedSources(files)...))
	if err != nil {
		t.Fatal(err)
	}
	if problems := lintOperations(schema, all); len(problems) > 0 {
		t.Fatalf("generated operations do not validate:\n%s", strings.Join(problems, "\n"))
	}
	return files, all
}

func opNamed(t *testing.T, ops []operation, name string) operation {
	t.Helper()
	for _, op := range ops {
		if op.Name == name {
			return op
		}
	}
	t.Fatalf("no operation %s", name)
	return operation{}
}

func TestDefaultOperationsCoverRootsAndArgumentFields(t *testing.T) {
	_, ops := generateFixture(t, `query Placeholder { summary { views } }`)
	got := map[string]string{}
	for _, op := range ops {
		got[op.Name] = op.Target
	}
	want := map[string]string{
		"Placeholder":              "query.summary",
		"GetStream":                "query.stream",
		"GetStreamsConnection":     "query.streamsConnection",
		"GetClip":                  "query.stream.clip",
		"GetClips":                 "query.stream.clips",
		"GetAnalyticsUsageSummary": "query.analytics.usage.summary",
		"CreateStream":             "mutation.createStream",
		"Ping":                     "mutation.ping",
		"StreamEvents":             "subscription.streamEvents",
	}
	for name, target := range want {
		if got[name] != target {
			t.Errorf("%s targets %q, want %q", name, got[name], target)
		}
	}
	if len(got) != len(want) {
		t.Errorf("operations = %v", got)
	}
	// query.analytics is a namespace and Query.old is deprecated: neither
	// is in want.
}

func TestDefaultOperationsNeverSelectInternalFields(t *testing.T) {
	full := mustSchema(t, `
directive @internal(reason: String!) on FIELD_DEFINITION
type Query { account: Account, secret: String @internal(reason: "service token") }
type Account { id: ID! token: String @internal(reason: "service token") }
`)
	public, err := publicSchema(full)
	if err != nil {
		t.Fatal(err)
	}
	files, _, err := generateDefaultOps(public, nil)
	if err != nil {
		t.Fatal(err)
	}
	for path, text := range files {
		if strings.Contains(text, "secret") || strings.Contains(text, "token") {
			t.Errorf("%s selects an internal field:\n%s", path, text)
		}
	}
	if !strings.Contains(files[generatedOpsDir+"/queries.graphql"], "query GetAccount {") {
		t.Errorf("no GetAccount:\n%s", files[generatedOpsDir+"/queries.graphql"])
	}
}

func TestDefaultOperationPathArgumentsBecomeVariables(t *testing.T) {
	_, ops := generateFixture(t, ``)
	doc := opNamed(t, ops, "GetClip").Document
	for _, want := range []string{
		"query GetClip ($streamId: ID!, $id: ID!, $limit: Int = 10)",
		"stream(id: $streamId)",
		"clip(id: $id, limit: $limit)",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("GetClip lacks %q:\n%s", want, doc)
		}
	}
}

func TestDefaultSelectionShapes(t *testing.T) {
	files, _ := generateFixture(t, ``)
	fragments := files[generatedOpsDir+"/fragments.graphql"]
	queries := files[generatedOpsDir+"/queries.graphql"]
	mutations := files[generatedOpsDir+"/mutations.graphql"]

	// Depth 2: owner and owner.org are selected, org.parent is not;
	// argument fields and deprecated fields are left out.
	stream := section(fragments, "fragment StreamDefaultFields on Stream {")
	for _, want := range []string{"  id\n", "  name\n", "  owner {\n    id\n    org {\n      name\n    }\n  }\n"} {
		if !strings.Contains(stream, want) {
			t.Errorf("StreamDefaultFields lacks %q:\n%s", want, stream)
		}
	}
	for _, unwanted := range []string{"parent", "clip", "legacy"} {
		if strings.Contains(stream, unwanted) {
			t.Errorf("StreamDefaultFields selects %s:\n%s", unwanted, stream)
		}
	}

	// Connections select edges { cursor node } and pageInfo, not nodes.
	conn := section(queries, "query GetStreamsConnection")
	for _, want := range []string{"edges {\n      cursor\n      node {\n        ...StreamDefaultFields\n", "pageInfo {\n      ...PageInfoDefaultFields\n", "totalCount"} {
		if !strings.Contains(conn, want) {
			t.Errorf("GetStreamsConnection lacks %q:\n%s", want, conn)
		}
	}
	if strings.Contains(conn, "nodes") {
		t.Errorf("GetStreamsConnection selects nodes:\n%s", conn)
	}

	// Unions select __typename and each member's fragment.
	create := section(mutations, "mutation CreateStream")
	for _, want := range []string{"__typename\n", "...StreamDefaultFields\n", "...ValidationErrorDefaultFields\n", "...PlacementErrorInStreamResultDefaultFields\n"} {
		if !strings.Contains(create, want) {
			t.Errorf("CreateStream lacks %q:\n%s", want, create)
		}
	}
	// A member declaring a field under a type the other members do not
	// selects it under an alias in that union, so the member fragments
	// merge.
	placement := section(fragments, "fragment PlacementErrorInStreamResultDefaultFields on PlacementError")
	validation := section(fragments, "fragment ValidationErrorDefaultFields")
	if !strings.Contains(placement, "placementErrorCode: code\n") || !strings.Contains(validation, "  code\n") {
		t.Errorf("code is not aliased on PlacementError only:\n%s\n%s", placement, validation)
	}
}

// section returns the block of text starting at the line that begins with
// head, up to the next blank line.
func section(text, head string) string {
	i := strings.Index(text, head)
	if i < 0 {
		return ""
	}
	rest := text[i:]
	if j := strings.Index(rest, "\n\n"); j >= 0 {
		return rest[:j+1]
	}
	return rest
}

func TestHandWrittenOperationReplacesDefault(t *testing.T) {
	_, ops := generateFixture(t, `query MyStream($id: ID!) { stream(id: $id) { id name } }`)
	for _, op := range ops {
		if op.Name == "GetStream" {
			t.Fatal("GetStream generated although MyStream targets query.stream")
		}
	}
	if opNamed(t, ops, "MyStream").Target != "query.stream" {
		t.Fatal("MyStream does not target query.stream")
	}
}

func TestLoaderRejectsDuplicateNamesAndTargets(t *testing.T) {
	cases := map[string]struct{ doc, want string }{
		"name": {`query GetStream($id: ID!) { stream(id: $id) { id } }
query GetStream { summary { views } }`, "operation GetStream is declared twice"},
		"target": {`query A($id: ID!) { stream(id: $id) { id } }
query B($id: ID!) { stream(id: $id) { name } }`, "operations A and B both target query.stream"},
		"generated name": {`query GetStream { summary { views } }`, "operation GetStream is declared twice"},
	}
	schema := mustSchema(t, defaultOpsSchema)
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			hand := []*ast.Source{{Name: "hand.graphql", Input: tc.doc}}
			handOps, err := parseOperations(schema, hand)
			if err == nil {
				files, _, gerr := generateDefaultOps(schema, handOps)
				if gerr != nil {
					t.Fatal(gerr)
				}
				_, err = parseOperations(schema, append(hand, generatedSources(files)...))
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestOperationTargetFollowsSingleFieldPath(t *testing.T) {
	cases := map[string]string{
		`query Q { stream(id: "1") { id name } }`:                                                 "query.stream",
		`query Q { stream(id: "1") { clip(id: "2") { id title } } }`:                              "query.stream.clip",
		`query Q { stream(id: "1") { ...F } } fragment F on Stream { owner { id org { name } } }`: "query.stream.owner",
		`query Q { stream(id: "1") { __typename owner { id } } }`:                                 "query.stream",
		`mutation M { ping }`: "mutation.ping",
	}
	schema := mustSchema(t, defaultOpsSchema)
	for doc, want := range cases {
		ops := mustOps(t, schema, doc)
		if ops[0].Target != want {
			t.Errorf("%s: target %s, want %s", doc, ops[0].Target, want)
		}
	}
	if _, err := parseOperations(schema, []*ast.Source{{Name: "x.graphql", Input: `query Q { stream(id: "1") { id } summary { views } }`}}); err == nil {
		t.Fatal("an operation with two root fields loaded")
	}
}

// TestTargetAnnotationWins: a hand-written operation that selects more than
// its target's path names the target, which then replaces that target's
// default operation instead of the inferred one.
func TestTargetAnnotationWins(t *testing.T) {
	_, ops := generateFixture(t, `
# Reads a clip with its stream's id.
# @target query.stream.clip
query StreamClip($streamId: ID!, $id: ID!) { stream(id: $streamId) { id clip(id: $id) { id title } } }
`)
	if got := opNamed(t, ops, "StreamClip").Target; got != "query.stream.clip" {
		t.Fatalf("StreamClip targets %s, want query.stream.clip", got)
	}
	names := map[string]bool{}
	for _, op := range ops {
		names[op.Name] = true
	}
	if names["GetClip"] || !names["GetStream"] {
		t.Fatalf("GetClip must be replaced and GetStream generated: %v", names)
	}
	// Without the annotation the same document targets query.stream.
	inferred := mustOps(t, mustSchema(t, defaultOpsSchema), `query StreamClip($streamId: ID!, $id: ID!) { stream(id: $streamId) { id clip(id: $id) { id title } } }`)
	if inferred[0].Target != "query.stream" {
		t.Fatalf("inferred target %s, want query.stream", inferred[0].Target)
	}
}

func TestTargetAnnotationMustMatchSchemaAndSelection(t *testing.T) {
	full := mustSchema(t, `
directive @internal(reason: String!) on FIELD_DEFINITION
type Query { stream(id: ID!): Stream }
type Stream { id: ID! owner: Owner clip(id: ID!): Clip secret(id: ID!): Clip @internal(reason: "service token") }
type Owner { id: ID! }
type Clip { id: ID! }
`)
	schema, err := publicSchema(full)
	if err != nil {
		t.Fatal(err)
	}
	const body = `query Q($id: ID!) { stream(id: $id) { id clip(id: $id) { id } } }`
	cases := map[string]struct{ annotation, want string }{
		"unknown field":    {"# @target query.stream.nope", "Stream has no public field nope"},
		"internal field":   {"# @target query.stream.secret", "Stream has no public field secret"},
		"not selected":     {"# @target query.stream.owner", "the operation does not select query.stream.owner"},
		"wrong kind":       {"# @target mutation.stream.clip", "a query operation's target is query.<field>"},
		"two annotations":  {"# @target query.stream\n# @target query.stream.clip", "2 @target annotations"},
		"missing path":     {"# @target", "takes exactly one path"},
		"below the target": {"# @target query.stream.clip.id.x", "ID has no public field x"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := parseOperations(schema, []*ast.Source{{Name: "x.graphql", Input: tc.annotation + "\n" + body}})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

const nodeOpsSchema = `
interface Node { id: ID! }
input ConnectionInput { first: Int after: String }
type Query {
  node(id: ID!): Node
  devices: [Device!]!
  sites: [Site!]!
  stream(id: ID!): Stream
}
type Stream implements Node { id: ID! clip(id: ID!): Clip }
type Clip { id: ID! }
type Device implements Node {
  id: ID!
  name: String!
  metrics(page: ConnectionInput, window: Int = 60): [Metric!]!
}
type Metric { at: String! value: Float! }
type Site { id: ID! usage(day: String!): Int }
`

// TestNodeTypeArgumentFieldsTargetThroughNode: an argument field on a Node
// type the single-object walk does not reach is a target through Query.node;
// one on a type that is not a Node stays list-only.
func TestNodeTypeArgumentFieldsTargetThroughNode(t *testing.T) {
	schema := mustSchema(t, nodeOpsSchema)
	targets, listOnly := opTargets(schema)
	keys := map[string]bool{}
	for _, target := range targets {
		keys[target.key()] = true
	}
	if !keys["query.node.Device.metrics"] || !keys["query.stream.clip"] || keys["query.node.Stream.clip"] {
		t.Fatalf("targets = %v", keys)
	}
	if len(listOnly) != 1 || listOnly[0] != "Site.usage" {
		t.Fatalf("list-only = %v", listOnly)
	}

	files, ops, err := generateDefaultOps(schema, nil)
	if err != nil {
		t.Fatal(err)
	}
	all, err := parseOperations(schema, generatedSources(files))
	if err != nil {
		t.Fatal(err)
	}
	if problems := lintOperations(schema, all); len(problems) > 0 {
		t.Fatalf("generated operations do not validate:\n%s", strings.Join(problems, "\n"))
	}
	for _, op := range ops {
		if got := targetOf(all, op.Name); got != op.Target.key() {
			t.Errorf("%s loads as %s, not its target %s", op.Name, got, op.Target.key())
		}
	}
	metrics := opNamed(t, all, "GetDeviceMetrics")
	if metrics.Target != "query.node.Device.metrics" || opNamed(t, all, "GetNode").Target != "query.node" {
		t.Fatalf("GetDeviceMetrics = %s, GetNode = %s", metrics.Target, opNamed(t, all, "GetNode").Target)
	}
	queries := files[generatedOpsDir+"/queries.graphql"]
	want := `# @target query.node.Device.metrics
query GetDeviceMetrics(
  $id: ID!
  $page: ConnectionInput
  # @genqlient(omitempty: true)
  $window: Int = 60
) {
  node(id: $id) {
    __typename
    ... on Device {
      metrics(page: $page, window: $window) {
        ...MetricDefaultFields
      }
    }
  }
}
`
	if !strings.Contains(queries, want) {
		t.Fatalf("GetDeviceMetrics is not\n%s\nin\n%s", want, queries)
	}
}

// TestRepoDefaultOperationsAreCurrent regenerates the default operations of
// the working tree: they must match the committed files, validate against
// the public schema with the hand-written ones, and leave no target without
// an operation.
func TestRepoDefaultOperationsAreCurrent(t *testing.T) {
	files, ops, all, err := buildDefaultOps("../..")
	if err != nil {
		t.Fatal(err)
	}
	stale, err := writeOrCheck("../..", files, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) > 0 {
		t.Fatalf("stale default operations (run make generate-ops): %v", stale)
	}
	for _, op := range ops {
		if got := targetOf(all, op.Name); got != op.Target.key() {
			t.Errorf("%s selects %s, not its target %s", op.Name, got, op.Target.key())
		}
	}
	schema, err := loadPublicSchema("../..", headRef)
	if err != nil {
		t.Fatal(err)
	}
	if missing := coverage(schema, all).Missing; len(missing) > 0 {
		t.Fatalf("targets without an operation: %v", missing)
	}
	// ListPushTargets selects the stream's id beside its push targets and
	// names its target with @target.
	push := opNamed(t, all, "ListPushTargets")
	if push.Target != "query.stream.pushTargets" || !strings.Contains(push.Document, "stream(id: $streamId) {\n    id\n    pushTargets {") {
		t.Fatalf("ListPushTargets = %s\n%s", push.Target, push.Document)
	}
	for name, target := range map[string]string{
		"GetInfrastructureNodeMetricsConnection":   "query.node.InfrastructureNode.metricsConnection",
		"GetInfrastructureNodeMetrics1hConnection": "query.node.InfrastructureNode.metrics1hConnection",
	} {
		if got := opNamed(t, all, name).Target; got != target {
			t.Errorf("%s targets %s, want %s", name, got, target)
		}
	}
	summary := opNamed(t, all, "GetStreamAnalyticsSummary")
	if summary.Target != "query.analytics.usage.streaming.streamAnalyticsSummary" || !strings.Contains(summary.Document, "$streamId: ID!") {
		t.Fatalf("GetStreamAnalyticsSummary = %s\n%s", summary.Target, summary.Document)
	}
}
