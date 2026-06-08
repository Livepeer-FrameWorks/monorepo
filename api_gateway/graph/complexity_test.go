package graph

import (
	"context"
	"testing"

	"frameworks/api_gateway/graph/generated"
	"frameworks/api_gateway/graph/model"

	"github.com/99designs/gqlgen/complexity"
	"github.com/99designs/gqlgen/graphql"
	"github.com/vektah/gqlparser/v2"
)

func intPtr(v int) *int { return &v }

// buildComplexitySchema wires SetupComplexity into an executable schema for walker-based
// complexity tests. Resolvers are unused: complexity.Calculate only touches Complexity and
// Schema, never executes a query.
func buildComplexitySchema() graphql.ExecutableSchema {
	var c generated.ComplexityRoot
	SetupComplexity(&c)
	return generated.NewExecutableSchema(generated.Config{Complexity: c})
}

// calcComplexity parses query against the real schema and runs the gqlgen complexity walker
// with the same scalar-free option the production guard uses (see ScalarFreeComplexityLimit).
func calcComplexity(t *testing.T, es graphql.ExecutableSchema, query, opName string, vars map[string]any) int {
	t.Helper()
	doc, errs := gqlparser.LoadQueryWithRules(es.Schema(), query, nil)
	if errs != nil {
		t.Fatalf("LoadQuery(%s): %v", opName, errs)
	}
	op := doc.Operations.ForName(opName)
	if op == nil {
		t.Fatalf("operation %q not found in test query", opName)
	}
	return complexity.Calculate(context.Background(), es, op, vars, complexity.WithFixedScalarValue(0))
}

// The exact operation the webapp sends for the Streams page. Selecting ~40 cheap scalars per
// row used to evaluate to 2160 because every scalar was multiplied by the page size.
const getStreamsConnectionQuery = `
query GetStreamsConnection($first: Int = 50, $after: String) {
  streamsConnection(page: {first: $first, after: $after}) {
    edges {
      cursor
      node {
        id
        streamId
        name
        description
        streamKey
        playbackId
        record
        ingestMode
        pullSource { sourceUriRedacted enabled class allowedClusterIds }
        thumbnailAssets { posterUrl spriteVttUrl spriteJpgUrl assetKey }
        dvrChapterMode
        dvrChapterIntervalSeconds
        retentionOverrides { streamId dvrRetentionDaysOverride clipRetentionDaysOverride }
        createdAt
        updatedAt
        metrics {
          status isLive currentViewers bufferState qualityTier
          primaryWidth primaryHeight primaryFps primaryCodec primaryBitrate startedAt
        }
      }
    }
    pageInfo { hasNextPage hasPreviousPage startCursor endCursor }
    totalCount
  }
}`

// Same page, plus the genuine per-item-I/O fields that each fan out to one round-trip per row.
const getStreamsConnectionHeavyQuery = `
query GetStreamsConnectionHeavy($first: Int = 50, $after: String) {
  streamsConnection(page: {first: $first, after: $after}) {
    edges {
      cursor
      node {
        id
        name
        metrics { status isLive }
        pushTargets { __typename }
        playbackPolicy { __typename }
        recentPullSourceEvents { __typename }
      }
    }
    pageInfo { hasNextPage endCursor }
    totalCount
  }
}`

// TestStreamsConnectionComplexityUnderLimit is the regression guard for the original bug: with
// scalars/enums valued at zero, the streams page must land well under the limit. The earlier
// unit tests hand-feed childComplexity and so never exercised the scalar inflation that broke
// production — this runs the real walker over the real schema.
func TestStreamsConnectionComplexityUnderLimit(t *testing.T) {
	es := buildComplexitySchema()
	got := calcComplexity(t, es, getStreamsConnectionQuery, "GetStreamsConnection", map[string]any{"first": 50})
	if got <= 0 || got >= 1000 {
		t.Fatalf("GetStreamsConnection(first:50) complexity = %d, want >0 and well under the 2000/3000 limit", got)
	}
}

// TestPerItemFetchFieldsRaiseComplexity proves the model is field-cost-aware: selecting fields
// whose resolvers do per-row I/O costs measurably more than selecting only projected scalars.
func TestPerItemFetchFieldsRaiseComplexity(t *testing.T) {
	es := buildComplexitySchema()
	base := calcComplexity(t, es, getStreamsConnectionQuery, "GetStreamsConnection", map[string]any{"first": 50})
	heavy := calcComplexity(t, es, getStreamsConnectionHeavyQuery, "GetStreamsConnectionHeavy", map[string]any{"first": 50})
	if heavy <= base {
		t.Fatalf("per-item fetch fields should raise complexity: base=%d heavy=%d", base, heavy)
	}
}

func TestGetPageMultiplier(t *testing.T) {
	tests := []struct {
		name string
		page *model.ConnectionInput
		want int
	}{
		{"nil input", nil, DefaultPageSize},
		{"empty input", &model.ConnectionInput{}, DefaultPageSize},
		{"first=10", &model.ConnectionInput{First: intPtr(10)}, 10},
		{"first=0 falls back to default", &model.ConnectionInput{First: intPtr(0)}, DefaultPageSize},
		{"last=25", &model.ConnectionInput{Last: intPtr(25)}, 25},
		{"first exceeds max", &model.ConnectionInput{First: intPtr(999)}, MaxPageSize},
		{"last exceeds max", &model.ConnectionInput{Last: intPtr(600)}, MaxPageSize},
		{"first takes precedence over last", &model.ConnectionInput{First: intPtr(5), Last: intPtr(20)}, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := getPageMultiplier(tt.page); got != tt.want {
				t.Errorf("getPageMultiplier() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestConnectionComplexity(t *testing.T) {
	tests := []struct {
		name            string
		childComplexity int
		page            *model.ConnectionInput
		want            int
	}{
		{
			name:            "subtracts meta overhead before multiplying",
			childComplexity: 46,
			page:            &model.ConnectionInput{First: intPtr(50)},
			// perItemCost = 46 - 2 = 44; result = 2 + 2 + (50 * 44) = 2204
			want: ConnectionBaseCost + ConnectionMetaOverhead + (50 * (46 - ConnectionMetaOverhead)),
		},
		{
			name:            "small child complexity floors perItemCost to 1",
			childComplexity: 3,
			page:            &model.ConnectionInput{First: intPtr(10)},
			// perItemCost = max(3 - 2, 1) = 1; result = 2 + 2 + (10 * 1) = 14
			want: ConnectionBaseCost + ConnectionMetaOverhead + 10,
		},
		{
			name:            "nil page uses default page size",
			childComplexity: 20,
			page:            nil,
			// perItemCost = 20 - 2 = 18; result = 2 + 2 + (50 * 18) = 904
			want: ConnectionBaseCost + ConnectionMetaOverhead + (DefaultPageSize * (20 - ConnectionMetaOverhead)),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := connectionComplexity(tt.childComplexity, tt.page); got != tt.want {
				t.Errorf("connectionComplexity(%d, page) = %d, want %d", tt.childComplexity, got, tt.want)
			}
		})
	}
}
