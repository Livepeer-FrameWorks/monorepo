package main

import (
	"strings"
	"testing"
)

func TestDefaultOperationsPreserveNestedInputDefaults(t *testing.T) {
	schema := mustSchema(t, `
enum Mode { NEAR FAR }
input Limits {
  threshold: Int! = 500
  mode: Mode! = NEAR
  labels: [String!]! = []
  next: Limits
}
input Envelope { limits: [Limits!]! }
type Query { inspect(input: Envelope!): String! }
`)
	files, _, err := generateDefaultOps(schema, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	query := files[generatedOpsDir+"/queries.graphql"]
	previous := -1
	for _, field := range []struct{ name, annotation string }{
		{"Limits.labels", `# @genqlient(for: "Limits.labels", omitempty: true)`},
		{"Limits.mode", `# @genqlient(for: "Limits.mode", omitempty: true, pointer: true)`},
		{"Limits.threshold", `# @genqlient(for: "Limits.threshold", omitempty: true, pointer: true)`},
	} {
		annotation := field.annotation
		index := strings.Index(query, annotation)
		if strings.Count(query, annotation) != 1 || index <= previous {
			t.Fatalf("nested defaults must be annotated once in sorted order; missing or misplaced %s in:\n%s", field.name, query)
		}
		previous = index
	}
	if strings.Contains(query, `for: "Envelope.limits"`) || strings.Contains(query, `for: "Limits.next"`) {
		t.Fatalf("fields without defaults received default annotations:\n%s", query)
	}
}
