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
	schema, err := loadSchema([]string{"../../../pkg/graphql/public/schema.public.graphql"})
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

func TestPathOperationTakesTheTargetDescription(t *testing.T) {
	t.Parallel()
	schema := gqlparser.MustLoadSchema(&gqlast.Source{Input: `
type Query {
  """Analytics namespace."""
  analytics: Analytics!
}
type Analytics {
  """Views of one stream."""
  views(streamId: ID!): Views
}
type Views { total: Int! unique: Int! }
`})
	description, err := operationDescription(schema,
		`query GetViews($streamId: ID!) { analytics { views(streamId: $streamId) { total unique } } }`, "GetViews")
	if err != nil {
		t.Fatal(err)
	}
	if description != "Views of one stream." {
		t.Fatalf("description = %q", description)
	}
}

func TestOperationCallsCoverEveryFunction(t *testing.T) {
	t.Parallel()
	generated := []byte(`package frameworks

func GetViews(ctx_ context.Context, client_ graphql.Client, streamId string, since *time.Time) (data_ *GetViewsResponse, err_ error) { return nil, nil }

func SubscribeViews(ctx context.Context, sc *SubscriptionClient, streamId *string) iter.Seq2[*ViewsResponse, error] { return nil }

func SubscribeFirehose(ctx context.Context, sc *SubscriptionClient) iter.Seq2[*FirehoseResponse, error] { return nil }

func SubscribeToCluster(ctx_ context.Context, client_ graphql.Client, clusterId string) (data_ *SubscribeToClusterResponse, err_ error) { return nil, nil }

func helper(v int) {}
`)
	out, err := operationCalls(generated)
	if err != nil {
		t.Fatal(err)
	}
	src := string(out)
	for _, want := range []string{
		`"time"`,
		`return GetViews(ctx, c, arg[string](v, "streamId"), arg[*time.Time](v, "since"))`,
		`return SubscribeToCluster(ctx, c, arg[string](v, "clusterId"))`,
		`"Views": func(ctx context.Context, sc *SubscriptionClient, v vars) iter.Seq2[any, error] {`,
		`return anyEvents(SubscribeViews(ctx, sc, arg[*string](v, "streamId")))`,
		`"Firehose": func(ctx context.Context, sc *SubscriptionClient, _ vars) iter.Seq2[any, error] {`,
		`return anyEvents(SubscribeFirehose(ctx, sc))`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("calls lack %q:\n%s", want, src)
		}
	}
	if strings.Contains(src, "helper") || strings.Contains(src, `"encoding/json"`) {
		t.Errorf("calls include a helper or an unused import:\n%s", src)
	}
}
