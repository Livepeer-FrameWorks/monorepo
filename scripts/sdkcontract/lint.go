package main

import (
	"fmt"
	"sort"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

// errorInterface is the schema interface every union error member implements.
const errorInterface = "Error"

// lintOperations validates every operation against schema and applies the
// public-operation rules: each union selection selects __typename and covers
// every error member, and no operation uses a deprecated field, argument, or
// enum value.
func lintOperations(schema *ast.Schema, ops []operation) []string {
	var problems []string
	for _, op := range ops {
		doc, errs := gqlparser.LoadQueryWithRules(schema, op.Document, nil)
		if len(errs) > 0 {
			for _, e := range errs {
				problems = append(problems, fmt.Sprintf("%s (%s): %s", op.Name, op.File, e.Message))
			}
			continue
		}
		fragments := map[string]*ast.FragmentDefinition{}
		for _, f := range doc.Fragments {
			fragments[f.Name] = f
		}
		for _, o := range doc.Operations {
			problems = append(problems, lintSelection(schema, op, o.SelectionSet, fragments)...)
		}
	}
	sort.Strings(problems)
	return problems
}

func lintSelection(schema *ast.Schema, op operation, set ast.SelectionSet, fragments map[string]*ast.FragmentDefinition) []string {
	var problems []string
	for _, sel := range set {
		switch s := sel.(type) {
		case *ast.Field:
			if s.Definition == nil {
				continue
			}
			where := fmt.Sprintf("%s (%s): %s", op.Name, op.File, s.Name)
			if dep := s.Definition.Directives.ForName("deprecated"); dep != nil {
				problems = append(problems, where+" is deprecated")
			}
			for _, arg := range s.Arguments {
				if def := s.Definition.Arguments.ForName(arg.Name); def != nil && def.Directives.ForName("deprecated") != nil {
					problems = append(problems, fmt.Sprintf("%s: argument %s is deprecated", where, arg.Name))
				}
				problems = append(problems, lintValue(schema, where, arg.Value)...)
			}
			named := schema.Types[s.Definition.Type.Name()]
			if named != nil && named.Kind == ast.Union {
				problems = append(problems, lintUnion(schema, where, named, s.SelectionSet, fragments)...)
			}
			problems = append(problems, lintSelection(schema, op, s.SelectionSet, fragments)...)
		case *ast.InlineFragment:
			problems = append(problems, lintSelection(schema, op, s.SelectionSet, fragments)...)
		case *ast.FragmentSpread:
			if frag := fragments[s.Name]; frag != nil {
				problems = append(problems, lintSelection(schema, op, frag.SelectionSet, fragments)...)
			}
		}
	}
	return problems
}

// lintValue reports deprecated enum values written as literals.
func lintValue(schema *ast.Schema, where string, v *ast.Value) []string {
	if v == nil {
		return nil
	}
	var problems []string
	if v.Kind == ast.EnumValue && v.Definition != nil {
		if ev := v.Definition.EnumValues.ForName(v.Raw); ev != nil && ev.Directives.ForName("deprecated") != nil {
			problems = append(problems, fmt.Sprintf("%s: enum value %s is deprecated", where, v.Raw))
		}
	}
	for _, child := range v.Children {
		problems = append(problems, lintValue(schema, where, child.Value)...)
	}
	return problems
}

func lintUnion(schema *ast.Schema, where string, union *ast.Definition, set ast.SelectionSet, fragments map[string]*ast.FragmentDefinition) []string {
	var problems []string
	hasTypename := false
	covered := map[string]bool{}
	for _, sel := range set {
		switch s := sel.(type) {
		case *ast.Field:
			if s.Name == "__typename" && s.Alias == s.Name {
				hasTypename = true
			}
		case *ast.InlineFragment:
			covered[s.TypeCondition] = true
		case *ast.FragmentSpread:
			if frag := fragments[s.Name]; frag != nil {
				covered[frag.TypeCondition] = true
			}
		}
	}
	if !hasTypename {
		problems = append(problems, where+": union "+union.Name+" selection does not select __typename")
	}
	for _, member := range union.Types {
		def := schema.Types[member]
		if def == nil || !implements(def, errorInterface) {
			continue
		}
		if !covered[member] && !covered[errorInterface] {
			problems = append(problems, fmt.Sprintf("%s: union %s selection does not select error member %s", where, union.Name, member))
		}
	}
	return problems
}

func implements(def *ast.Definition, iface string) bool {
	for _, name := range def.Interfaces {
		if name == iface {
			return true
		}
	}
	return false
}
