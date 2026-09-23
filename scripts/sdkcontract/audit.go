package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"
)

// targetCoverage is the SDK operation coverage of the public schema's
// targets (opTargets).
type targetCoverage struct {
	// Missing lists targets no operation addresses.
	Missing []string
	// Namespaces lists targets whose default selection is empty; their
	// argument fields are targets of their own.
	Namespaces []string
	// Extra lists operations whose target is not a default target, such as
	// ListPushTargets on query.stream.pushTargets.
	Extra []string
	// Targets and HandWritten count, per root kind, the targets with an
	// operation and those whose operation is hand-written.
	Targets, HandWritten map[ast.Operation]int
	// Nested counts the argument-field targets with an operation, ViaNode
	// those of them reached through Query.node.
	Nested, ViaNode int
	ListOnly        []string
}

// coverage checks that every target of schema has an operation. The loader
// already guarantees at most one.
func coverage(schema *ast.Schema, ops []operation) targetCoverage {
	byTarget := map[string]operation{}
	for _, op := range ops {
		byTarget[op.Target] = op
	}
	targets, listOnly := opTargets(schema)
	b := newSelectionBuilder(schema)
	c := targetCoverage{Targets: map[ast.Operation]int{}, HandWritten: map[ast.Operation]int{}, ListOnly: listOnly}
	known := map[string]bool{}
	for _, t := range targets {
		key := t.key()
		known[key] = true
		op, ok := byTarget[key]
		def := schema.Types[t.field().Type.Name()]
		if !ok && b.isNamespace(def) {
			c.Namespaces = append(c.Namespaces, key)
			continue
		}
		c.Targets[t.Kind]++
		if len(t.Path) > 1 {
			c.Nested++
		}
		if t.On != "" {
			c.ViaNode++
		}
		switch {
		case !ok:
			c.Missing = append(c.Missing, key)
		case !strings.HasPrefix(op.File, generatedOpsDir+"/"):
			c.HandWritten[t.Kind]++
		}
	}
	for _, op := range ops {
		if !known[op.Target] {
			c.Extra = append(c.Extra, op.Name+" ("+op.Target+")")
		}
	}
	sort.Strings(c.Missing)
	sort.Strings(c.Namespaces)
	sort.Strings(c.Extra)
	return c
}

// runAudit reports the SDK coverage of the public schema and fails when a
// root field or argument field has no operation.
func runAudit(repo string) error {
	full, err := loadFullSchema(repo, headRef)
	if err != nil {
		return err
	}
	schema, err := publicSchema(full)
	if err != nil {
		return err
	}
	ops, err := loadOperations(repo)
	if err != nil {
		return err
	}
	c := coverage(schema, ops)
	for _, root := range []struct {
		kind     ast.Operation
		def      *ast.Definition
		fullRoot *ast.Definition
	}{
		{ast.Query, schema.Query, full.Query},
		{ast.Mutation, schema.Mutation, full.Mutation},
		{ast.Subscription, schema.Subscription, full.Subscription},
	} {
		if root.def == nil {
			continue
		}
		internal, deprecated := 0, 0
		for _, field := range root.fullRoot.Fields {
			if _, ok := internalReason(field); ok {
				internal++
			}
		}
		for _, field := range root.def.Fields {
			if isDeprecated(field.Directives) {
				deprecated++
			}
		}
		fmt.Printf("%s: %d targets with an operation (%d hand-written); %d deprecated and %d internal root fields excluded\n",
			root.kind, c.Targets[root.kind], c.HandWritten[root.kind], deprecated, internal)
	}
	listOnly := "none"
	if len(c.ListOnly) > 0 {
		listOnly = strings.Join(c.ListOnly, ", ")
	}
	fmt.Printf("argument fields below Query: %d with an operation (%d of them through Query.node); %d reachable only through lists, unions, or interfaces (custom selection): %s\n",
		c.Nested, c.ViaNode, len(c.ListOnly), listOnly)
	if len(c.Namespaces) > 0 {
		fmt.Printf("namespaces without an operation of their own: %s\n", strings.Join(c.Namespaces, ", "))
	}
	if len(c.Extra) > 0 {
		fmt.Printf("hand-written operations beyond the default targets: %s\n", strings.Join(c.Extra, ", "))
	}
	fmt.Printf("SDK operation documents: %d\n", len(ops))
	if len(c.Missing) > 0 {
		return failList("public fields without an SDK operation; run make generate-ops", c.Missing)
	}
	return nil
}
