package main

import (
	"reflect"
	"testing"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

func TestRootCoverageCountsRootFieldOnly(t *testing.T) {
	schema, err := gqlparser.LoadSchema(&ast.Source{Input: `
type Query { stream: Stream, platform: Platform }
type Stream { id: ID! }
type Platform { id: ID! }
`})
	if err != nil {
		t.Fatal(err)
	}
	got, err := rootCoverage(schema, []operation{{Name: "GetStream", Document: "query GetStream { stream { id } }"}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, map[string][]string{"query.stream": {"GetStream"}}) {
		t.Fatalf("root coverage = %#v", got)
	}
}
