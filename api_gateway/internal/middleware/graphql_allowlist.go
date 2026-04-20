package middleware

import (
	"encoding/json"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
)

var allowlistedOperations = []string{"serviceinstanceshealth", "resolveviewerendpoint", "resolveingestendpoint", "networkstatus"}

// allowlistedMutationOperations is the set of write fields that may be
// invoked without a JWT. Each entry needs its own per-call credential —
// e.g. bootstrapEdge accepts a one-time bootstrap token in the input
// itself, validated server-side via Quartermaster.
var allowlistedMutationOperations = []string{"bootstrapedge"}

var allowlistedOperationSet = toLowerSet(allowlistedOperations)
var allowlistedMutationSet = toLowerSet(allowlistedMutationOperations)

func toLowerSet(items []string) map[string]struct{} {
	out := make(map[string]struct{}, len(items))
	for _, op := range items {
		out[strings.ToLower(op)] = struct{}{}
	}
	return out
}

// isAllowlistedQuery returns true when the GraphQL payload selects only
// allowlisted top-level fields for its operation type (read-only queries
// or the explicitly public-by-design mutations).
func isAllowlistedQuery(body []byte) bool {
	var req struct {
		Query         string `json:"query"`
		OperationName string `json:"operationName"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return false
	}
	return isAllowlistedOperation(req.Query, req.OperationName)
}

func isAllowlistedOperation(query, operationName string) bool {
	query = strings.TrimSpace(query)
	if query == "" {
		return false
	}

	doc, err := parser.ParseQuery(&ast.Source{Input: query})
	if err != nil {
		return false
	}

	op := selectGraphQLOperation(doc, operationName)
	if op == nil {
		return false
	}
	var allowed map[string]struct{}
	switch op.Operation {
	case ast.Query:
		allowed = allowlistedOperationSet
	case ast.Mutation:
		allowed = allowlistedMutationSet
	default:
		return false
	}

	fragments := make(map[string]*ast.FragmentDefinition, len(doc.Fragments))
	for _, fragment := range doc.Fragments {
		fragments[fragment.Name] = fragment
	}

	return selectionSetAllowlisted(op.SelectionSet, fragments, allowed, map[string]bool{})
}

func selectGraphQLOperation(doc *ast.QueryDocument, operationName string) *ast.OperationDefinition {
	if len(doc.Operations) == 0 {
		return nil
	}
	if operationName != "" {
		for _, op := range doc.Operations {
			if op.Name == operationName {
				return op
			}
		}
		if len(doc.Operations) == 1 {
			return doc.Operations[0]
		}
		return nil
	}
	if len(doc.Operations) == 1 {
		return doc.Operations[0]
	}
	return nil
}

func selectionSetAllowlisted(
	selectionSet ast.SelectionSet,
	fragments map[string]*ast.FragmentDefinition,
	allowed map[string]struct{},
	visited map[string]bool,
) bool {
	if len(selectionSet) == 0 {
		return false
	}
	for _, selection := range selectionSet {
		switch node := selection.(type) {
		case *ast.Field:
			fieldName := strings.ToLower(node.Name)
			if fieldName == "__typename" {
				continue
			}
			if _, ok := allowed[fieldName]; !ok {
				return false
			}
		case *ast.FragmentSpread:
			fragment := fragments[node.Name]
			if fragment == nil || visited[node.Name] {
				return false
			}
			visited[node.Name] = true
			ok := selectionSetAllowlisted(fragment.SelectionSet, fragments, allowed, visited)
			delete(visited, node.Name)
			if !ok {
				return false
			}
		case *ast.InlineFragment:
			if !selectionSetAllowlisted(node.SelectionSet, fragments, allowed, visited) {
				return false
			}
		default:
			return false
		}
	}
	return true
}
