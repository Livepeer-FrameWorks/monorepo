package main

import (
	"os"
	"strings"
	"testing"

	"github.com/vektah/gqlparser/v2"
	gqlast "github.com/vektah/gqlparser/v2/ast"
)

func TestDocumentOperations(t *testing.T) {
	t.Parallel()
	schema := gqlparser.MustLoadSchema(&gqlast.Source{Input: `
type Query {
  """Return the current viewer count."""
  viewerCount: Int!
}
`})
	generated := []byte("package example\n\nconst ViewerCount_Operation = `query ViewerCount { viewerCount }`\n\nfunc ViewerCount() {}\n")

	documented, err := documentOperations(generated, schema)
	if err != nil {
		t.Fatal(err)
	}
	output := string(documented)
	for _, expected := range []string{
		"// ViewerCount executes the corresponding GraphQL operation.",
		"// Return the current viewer count.",
		"func ViewerCount()",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("generated output is missing %q:\n%s", expected, output)
		}
	}
}

func TestDocumentGeneratedOperations(t *testing.T) {
	t.Parallel()
	schema, err := loadSchema([]string{"../../../pkg/graphql/schema.graphql"})
	if err != nil {
		t.Fatal(err)
	}
	description, err := operationDescription(
		schema,
		`mutation CreateStream($input: CreateStreamInput!) { createStream(input: $input) { __typename } }`,
		"CreateStream",
	)
	if err != nil {
		t.Fatal(err)
	}
	if description == "" {
		t.Fatal("createStream has no schema description")
	}
	generated, err := os.ReadFile("../../generated.go")
	if err != nil {
		t.Fatal(err)
	}
	if !operationPattern.Match(generated) {
		t.Fatal("generated operations were not found")
	}
	foundCreateStream := false
	for _, match := range operationPattern.FindAllSubmatch(generated, -1) {
		foundCreateStream = foundCreateStream || string(match[1]) == "CreateStream"
	}
	if !foundCreateStream {
		t.Fatal("CreateStream operation was not extracted")
	}

	documented, err := documentOperations(generated, schema)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(documented), "// CreateStream executes the corresponding GraphQL operation.") {
		t.Fatal("CreateStream schema documentation was not generated")
	}
}
