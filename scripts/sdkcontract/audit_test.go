package main

import (
	"reflect"
	"testing"
)

func TestCoverageRequiresEveryTarget(t *testing.T) {
	schema := mustSchema(t, `
type Query { stream(id: ID!): Stream, analytics: Analytics!, platform: Platform }
type Stream { id: ID! }
type Platform { id: ID! }
type Analytics { usage(streamId: ID!): Usage }
type Usage { views: Int! }
`)
	ops := mustOps(t, `
query GetStream($id: ID!) { stream(id: $id) { id } }
query GetUsage($streamId: ID!) { analytics { usage(streamId: $streamId) { views } } }
`)
	c := coverage(schema, ops)
	if !reflect.DeepEqual(c.Missing, []string{"query.platform"}) {
		t.Fatalf("missing = %v", c.Missing)
	}
	if !reflect.DeepEqual(c.Namespaces, []string{"query.analytics"}) {
		t.Fatalf("namespaces = %v", c.Namespaces)
	}
	if c.Nested != 1 || c.HandWritten["query"] != 2 {
		t.Fatalf("coverage = %+v", c)
	}
}
