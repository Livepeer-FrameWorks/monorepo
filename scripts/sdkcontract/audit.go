package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
)

// rootCoverage compares schema entry points with the root fields selected by
// public SDK operation documents. It does not infer whether a field is safe
// to publish from its name or description.
func rootCoverage(schema *ast.Schema, ops []operation) (map[string][]string, error) {
	covered := map[string][]string{}
	for _, op := range ops {
		doc, err := parser.ParseQuery(&ast.Source{Name: op.File, Input: op.Document})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", op.File, err)
		}
		for _, definition := range doc.Operations {
			root := string(definition.Operation)
			var rootType *ast.Definition
			switch definition.Operation {
			case ast.Query:
				rootType = schema.Query
			case ast.Mutation:
				rootType = schema.Mutation
			case ast.Subscription:
				rootType = schema.Subscription
			}
			if rootType == nil {
				return nil, fmt.Errorf("%s: schema has no %s root", op.Name, root)
			}
			for _, selection := range definition.SelectionSet {
				field, ok := selection.(*ast.Field)
				if !ok {
					return nil, fmt.Errorf("%s: root selection must be a field", op.Name)
				}
				if rootType.Fields.ForName(field.Name) == nil {
					return nil, fmt.Errorf("%s: %s.%s is not in the schema", op.Name, root, field.Name)
				}
				key := root + "." + field.Name
				covered[key] = append(covered[key], op.Name)
			}
		}
	}
	return covered, nil
}

func runAudit(repo string) error {
	schema, err := loadSchema(repo, headRef)
	if err != nil {
		return err
	}
	ops, err := loadOperations(repo)
	if err != nil {
		return err
	}
	covered, err := rootCoverage(schema, ops)
	if err != nil {
		return err
	}
	for _, root := range []struct {
		name string
		def  *ast.Definition
	}{
		{"query", schema.Query},
		{"mutation", schema.Mutation},
		{"subscription", schema.Subscription},
	} {
		if root.def == nil {
			continue
		}
		var missing []string
		count := 0
		for _, field := range root.def.Fields {
			if strings.HasPrefix(field.Name, "__") {
				continue
			}
			count++
			if len(covered[root.name+"."+field.Name]) == 0 {
				missing = append(missing, field.Name)
			}
		}
		sort.Strings(missing)
		fmt.Printf("%s: %d schema fields, %d covered, %d uncovered\n", root.name, count, count-len(missing), len(missing))
		if len(missing) > 0 {
			fmt.Printf("  uncovered: %s\n", strings.Join(missing, ", "))
		}
	}
	fmt.Printf("SDK operation documents: %d\n", len(ops))
	return nil
}
