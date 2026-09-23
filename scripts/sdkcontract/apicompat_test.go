package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const experimentalSchema = `
directive @experimental(reason: String!, until: String!) on FIELD_DEFINITION
schema { query: Query }
type Stream {
  id: ID!
  name: String!
  score: Int @experimental(reason: "Scoring model is not final", until: "v0.4.0")
  preview(limit: Int): Preview @experimental(reason: "Preview shape is not final", until: "v0.4.0")
}
type Preview { url: String }
type Lab { probe(id: ID!): Preview }
type Query {
  stream(id: ID!): Stream
  lab: Lab @experimental(reason: "Lab endpoints change freely.", until: "v0.4.0")
  streams(first: Int): [Stream!]!
}
`

func TestOperationsOnExperimentalPathsAreExperimental(t *testing.T) {
	schema := mustSchema(t, experimentalSchema)
	ops := mustOps(t, schema, `
query GetStream($id: ID!) { stream(id: $id) { id name } }
query GetStreamPreview($id: ID!) { stream(id: $id) { preview(limit: 3) { url } } }
query GetLabProbe($id: ID!) { lab { probe(id: $id) { url } } }
`)
	want := map[string]string{"GetStream": "", "GetStreamPreview": "v0.4.0", "GetLabProbe": "v0.4.0"}
	for _, op := range ops {
		if op.Experimental.Until != want[op.Name] {
			t.Errorf("%s (target %s): experimental until %q, want %q", op.Name, op.Target, op.Experimental.Until, want[op.Name])
		}
	}

	data, err := renderTargets(ops)
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Experimental map[string]struct{ Until, Reason, Note string } `json:"experimental"`
	}
	if err := json.Unmarshal([]byte(data), &file); err != nil {
		t.Fatal(err)
	}
	if len(file.Experimental) != 2 {
		t.Fatalf("targets.json experimental = %v, want GetStreamPreview and GetLabProbe", file.Experimental)
	}
	note := file.Experimental["GetLabProbe"].Note
	if note != "Experimental until v0.4.0: Lab endpoints change freely. A later SDK release of this line may change or remove this operation." {
		t.Fatalf("GetLabProbe note = %q", note)
	}
	if !strings.HasPrefix(file.Experimental["GetStreamPreview"].Note, "Experimental until v0.4.0: Preview shape is not final. ") {
		t.Fatalf("GetStreamPreview note = %q", file.Experimental["GetStreamPreview"].Note)
	}
}

func TestReleasedLineMayDropOnlyExperimentalOperations(t *testing.T) {
	released := []manifestOperation{
		{Name: "GetStream", Since: "v0.3.11"},
		{Name: "GetLabProbe", Since: "v0.3.11", Experimental: "v0.4.0"},
		{Name: "GetStreamPreview", Since: "v0.3.11", Experimental: "v0.4.0"},
	}
	current := []manifestOperation{
		{Name: "GetStream", Since: "v0.3.11"},
		{Name: "GetStreamPreview", Since: "v0.3.12"},
	}
	if problems := releasedLineProblems(released, current, "sdk-v0.3.0", "0.3"); len(problems) != 0 {
		t.Fatalf("experimental removal and since rise reported: %v", problems)
	}

	current = []manifestOperation{{Name: "GetStreamPreview", Since: "v0.3.11"}}
	problems := releasedLineProblems(released, current, "sdk-v0.3.0", "0.3")
	if len(problems) != 1 || problems[0] != "GetStream: released in sdk-v0.3.0 and removed within line 0.3" {
		t.Fatalf("stable removal: problems = %v", problems)
	}

	current = []manifestOperation{{Name: "GetStream", Since: "v0.3.12"}}
	problems = releasedLineProblems(released, current, "sdk-v0.3.0", "0.3")
	if len(problems) != 1 || !strings.Contains(problems[0], "GetStream: since rose from v0.3.11") {
		t.Fatalf("stable since rise: problems = %v", problems)
	}
}

func TestFrozenLineExemptsExperimentalOperations(t *testing.T) {
	ops := []manifestOperation{
		{Name: "GetStream", Since: "v0.3.11", Document: "stable"},
		{Name: "GetLabProbe", Since: "v0.3.11", Document: "gone", Experimental: "v0.4.0"},
	}
	since := func(document string) (semver, error) {
		if document == "gone" {
			return semver{}, errors.New("does not validate on the working tree")
		}
		return parseSemverMust(t, "v0.3.11"), nil
	}
	if problems := frozenLineProblems("0.2", ops, since); len(problems) != 0 {
		t.Fatalf("experimental operation reported: %v", problems)
	}
	ops[0].Document = "gone"
	problems := frozenLineProblems("0.2", ops, since)
	if len(problems) != 1 || !strings.HasPrefix(problems[0], "line 0.2: GetStream: ") {
		t.Fatalf("stable operation not reported: %v", problems)
	}
}

func TestReferenceMarksExperimentalOperations(t *testing.T) {
	schema := mustSchema(t, experimentalSchema)
	ops := mustOps(t, schema, `
query GetStream($id: ID!) { stream(id: $id) { id name } }
query GetStreamPreview($id: ID!) { stream(id: $id) { preview(limit: 3) { url } } }
`)
	bindings, err := sdkBindings(schema, ops)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	writeSDKBindings(&b, schema.Query.Fields.ForName("stream"), bindings["query.stream"], false)
	table := b.String()
	if !strings.Contains(table, "| GetStreamPreview (experimental until v0.4.0) |") {
		t.Fatalf("experimental operation not marked:\n%s", table)
	}
	if strings.Contains(table, "GetStream (experimental") {
		t.Fatalf("stable operation marked experimental:\n%s", table)
	}
}

func parseSemverMust(t *testing.T, v string) semver {
	t.Helper()
	s, ok := parseSemver(v)
	if !ok {
		t.Fatalf("%s is not a release version", v)
	}
	return s
}

func TestDefaultSelectionsLeaveOutExperimentalFields(t *testing.T) {
	schema := mustSchema(t, experimentalSchema)
	files, _, err := generateDefaultOps(schema, nil)
	if err != nil {
		t.Fatal(err)
	}
	stream := section(files[generatedOpsDir+"/fragments.graphql"], "fragment StreamDefaultFields on Stream {")
	if !strings.Contains(stream, "  name\n") || strings.Contains(stream, "score") {
		t.Fatalf("StreamDefaultFields must select name and not the experimental score:\n%s", stream)
	}
	all, err := parseOperations(schema, generatedSources(files))
	if err != nil {
		t.Fatal(err)
	}
	if problems := lintOperations(schema, all); len(problems) > 0 {
		t.Fatalf("generated operations fail lint:\n%s", strings.Join(problems, "\n"))
	}
	// Experimental fields that are roots or take arguments keep their own,
	// experimental, operations.
	for _, name := range []string{"GetPreview", "GetProbe"} {
		if op := opNamed(t, all, name); op.Experimental.Until != "v0.4.0" {
			t.Errorf("%s is not experimental: %+v", name, op.Experimental)
		}
	}
	if op := opNamed(t, all, "GetStream"); op.Experimental.Until != "" {
		t.Errorf("GetStream is experimental: %+v", op.Experimental)
	}
}

func TestLintRejectsStableOperationSelectingExperimentalField(t *testing.T) {
	schema := mustSchema(t, experimentalSchema)
	stable := mustOps(t, schema, `query GetStream($id: ID!) { stream(id: $id) { id score } }`)
	problems := lintOperations(schema, stable)
	want := "GetStream (test.graphql): score is @experimental until v0.4.0, but the operation's target query.stream is stable; a stable operation cannot select a field that may be removed within the line (target the experimental field or leave it out)"
	if len(problems) != 1 || problems[0] != want {
		t.Fatalf("problems = %v, want %q", problems, want)
	}
	experimental := mustOps(t, schema, `query GetStreamPreview($id: ID!) { stream(id: $id) { preview(limit: 3) { url } } }`)
	if problems := lintOperations(schema, experimental); len(problems) != 0 {
		t.Fatalf("experimental operation rejected: %v", problems)
	}
}
