package graph

import (
	"context"
	"testing"

	"github.com/99designs/gqlgen/graphql"
	"github.com/99designs/gqlgen/graphql/handler/extension"
	"github.com/vektah/gqlparser/v2"
)

func TestScalarFreeComplexityLimitRecordsComplexityStats(t *testing.T) {
	es := buildComplexitySchema()
	vars := map[string]any{"first": 50}
	want := calcComplexity(t, es, getStreamsConnectionQuery, "GetStreamsConnection", vars)

	for _, limit := range []int{10_000, 1} {
		ext := &ScalarFreeComplexityLimit{Func: func(context.Context, *graphql.OperationContext) int { return limit }}
		if err := ext.Validate(es); err != nil {
			t.Fatal(err)
		}
		doc, errs := gqlparser.LoadQueryWithRules(es.Schema(), getStreamsConnectionQuery, nil)
		if errs != nil {
			t.Fatal(errs)
		}
		opCtx := &graphql.OperationContext{Doc: doc, OperationName: "GetStreamsConnection", Variables: vars}
		gqlErr := ext.MutateOperationContext(context.Background(), opCtx)
		if (gqlErr != nil) != (want > limit) {
			t.Fatalf("limit %d: error = %v, want error only when cost %d exceeds the limit", limit, gqlErr, want)
		}

		ctx := graphql.WithOperationContext(context.Background(), opCtx)
		stats := extension.GetComplexityStats(ctx)
		if stats == nil {
			t.Fatalf("limit %d: GetComplexityStats = nil, want the computed cost recorded", limit)
		}
		if stats.Complexity != want || stats.ComplexityLimit != limit {
			t.Fatalf("limit %d: stats = %+v, want Complexity=%d ComplexityLimit=%d", limit, stats, want, limit)
		}
	}
}
