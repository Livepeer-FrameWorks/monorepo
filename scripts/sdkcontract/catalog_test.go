package main

import (
	"encoding/json"
	"testing"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

func TestCatalogCarriesVariablePresenceAndResultTypes(t *testing.T) {
	schema, err := gqlparser.LoadSchema(&ast.Source{Input: `type Query { rows(first: Int!, label: String, token: ID!): [String!]! }`})
	if err != nil {
		t.Fatal(err)
	}
	in := emitInput{Version: "0.5.0", Line: "0.5", MinServer: "v0.3.11", Schema: schema, Targets: map[string]string{"Rows": "query.rows"}, Ops: []manifestOperation{{Name: "Rows", Kind: "query", Since: "v0.3.11", Document: `query Rows($first: Int! = 50, $label: String = null, $token: ID!) { rows(first: $first, label: $label, token: $token) }`}}}
	var catalog struct {
		Operations []struct {
			Variables []catalogVariable
			Result    catalogResult
		}
	}
	if err := json.Unmarshal([]byte(in.catalog()), &catalog); err != nil {
		t.Fatal(err)
	}
	variables := catalog.Operations[0].Variables
	if variables[0].Required || !variables[0].HasDefault || string(variables[0].DefaultValue) != "50" || variables[0].TypeString != "Int!" {
		t.Fatalf("defaulted non-null variable: %+v", variables[0])
	}
	if !variables[1].HasDefault || string(variables[1].DefaultValue) != "null" {
		t.Fatalf("explicit null default was lost: %+v", variables[1])
	}
	if !variables[2].Required || variables[2].HasDefault || len(variables[2].DefaultValue) != 0 {
		t.Fatalf("required variable: %+v", variables[2])
	}
	result := catalog.Operations[0].Result
	if result.Field != "rows" || result.ResponseKey != "rows" || result.Type != "[String!]!" {
		t.Fatalf("result metadata: %+v", result)
	}
}
