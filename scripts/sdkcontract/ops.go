package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/formatter"
	"github.com/vektah/gqlparser/v2/parser"
)

const publicDir = "pkg/graphql/public"

// operation is one public operation with the fragments it uses, printed as a
// self-contained document.
type operation struct {
	Name     string
	Kind     string
	Document string
	Hash     string
	File     string
	// Target is the field the operation addresses (resolveTarget), as
	// kind.field.field, with a member type name after a field of union or
	// interface type (query.node.InfrastructureNode.metricsConnection).
	Target string
	// Experimental is the @experimental mark of the first field on the
	// target path that carries one (targetExperimental); Until is empty for
	// an operation on stable fields only.
	Experimental experimentalTarget
}

// experimentalTarget is the @experimental mark an operation inherits from
// its target path.
type experimentalTarget struct {
	Until  string `json:"until"`
	Reason string `json:"reason"`
}

// targetExperimental returns the @experimental mark of the first field on
// target (kind.field.field, a member type name after a field of union or
// interface type) that carries one. An operation whose root or any field on
// its path is experimental is itself experimental: removing that field
// removes the operation.
func targetExperimental(schema *ast.Schema, target string) (experimentalTarget, bool) {
	segments := strings.Split(target, ".")
	if len(segments) < 2 {
		return experimentalTarget{}, false
	}
	parent := rootDefinition(schema, ast.Operation(segments[0]))
	for _, seg := range segments[1:] {
		if parent == nil {
			return experimentalTarget{}, false
		}
		if member := abstractMember(schema, parent, seg); member != nil {
			parent = member
			continue
		}
		field := parent.Fields.ForName(seg)
		if field == nil {
			return experimentalTarget{}, false
		}
		if reason, until, ok := experimentalMark(field); ok {
			return experimentalTarget{Until: until, Reason: reason}, true
		}
		parent = schema.Types[field.Type.Name()]
	}
	return experimentalTarget{}, false
}

// loadOperations parses every operation document under pkg/graphql/public,
// hand-written and generated, and returns one self-contained document per
// operation, sorted by name.
func loadOperations(repo string) ([]operation, error) {
	schema, err := loadPublicSchema(repo, headRef)
	if err != nil {
		return nil, err
	}
	sources, err := operationSources(repo, true)
	if err != nil {
		return nil, err
	}
	return parseOperations(schema, sources)
}

// operationSources reads the .graphql files under pkg/graphql/public except
// the public schema, and except the generated default operations unless
// withGenerated is set.
func operationSources(repo string, withGenerated bool) ([]*ast.Source, error) {
	root := filepath.Join(repo, publicDir)
	var sources []*ast.Source
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(repo, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			if !withGenerated && filepath.ToSlash(rel) == generatedOpsDir {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".graphql" || filepath.ToSlash(rel) == publicSchemaPath {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sources = append(sources, &ast.Source{Name: filepath.ToSlash(rel), Input: string(data)})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].Name < sources[j].Name })
	return sources, nil
}

// parseOperations returns the operations of sources against the public
// schema. Operation and fragment names are unique, every operation selects
// one root field, and no two operations address the same target.
func parseOperations(schema *ast.Schema, sources []*ast.Source) ([]operation, error) {
	fragments := map[string]*ast.FragmentDefinition{}
	type located struct {
		op   *ast.OperationDefinition
		file string
	}
	var ops []located
	for _, src := range sources {
		doc, err := parser.ParseQuery(src)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", src.Name, err)
		}
		for _, frag := range doc.Fragments {
			if _, dup := fragments[frag.Name]; dup {
				return nil, fmt.Errorf("%s: fragment %s is declared twice", src.Name, frag.Name)
			}
			fragments[frag.Name] = frag
		}
		for _, op := range doc.Operations {
			if op.Name == "" {
				return nil, fmt.Errorf("%s: every public operation needs a name", src.Name)
			}
			ops = append(ops, located{op: op, file: src.Name})
		}
	}

	seen := map[string]bool{}
	targets := map[string]string{}
	out := make([]operation, 0, len(ops))
	for _, item := range ops {
		if seen[item.op.Name] {
			return nil, fmt.Errorf("%s: operation %s is declared twice", item.file, item.op.Name)
		}
		seen[item.op.Name] = true
		used, err := fragmentClosure(item.op.SelectionSet, fragments)
		if err != nil {
			return nil, fmt.Errorf("%s: %s: %w", item.file, item.op.Name, err)
		}
		target, err := resolveTarget(schema, item.op, fragments)
		if err != nil {
			return nil, fmt.Errorf("%s: %s: %w", item.file, item.op.Name, err)
		}
		if other, dup := targets[target]; dup {
			return nil, fmt.Errorf("%s: operations %s and %s both target %s", item.file, other, item.op.Name, target)
		}
		targets[target] = item.op.Name
		doc := &ast.QueryDocument{Operations: ast.OperationList{item.op}}
		for _, name := range used {
			doc.Fragments = append(doc.Fragments, fragments[name])
		}
		var buf bytes.Buffer
		formatter.NewFormatter(&buf, formatter.WithIndent("  "), formatter.WithoutDescription()).FormatQueryDocument(doc)
		text := strings.TrimSpace(buf.String()) + "\n"
		sum := sha256.Sum256([]byte(text))
		experimental, _ := targetExperimental(schema, target)
		out = append(out, operation{
			Name:         item.op.Name,
			Kind:         string(item.op.Operation),
			Document:     text,
			Hash:         "sha256:" + hex.EncodeToString(sum[:]),
			File:         item.file,
			Target:       target,
			Experimental: experimental,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// targetAnnotation marks a comment line directly above an operation that
// names its target explicitly: "# @target query.stream.pushTargets". The
// three SDK code generators ignore it: genqlient reads only "# @genqlient"
// comment directives (sdk_go/tools/genclient drops the line from the Go doc
// comment genqlient copies it into), and graphql-codegen and ariadne-codegen
// drop comments when they print a document.
const targetAnnotation = "@target"

// resolveTarget returns the target of op: the path its @target annotation
// names, or, without one, the path operationTarget infers. An annotated path
// must name public fields of schema, a union or interface member after a
// field of that abstract type, and must be selected by op itself.
func resolveTarget(schema *ast.Schema, op *ast.OperationDefinition, fragments map[string]*ast.FragmentDefinition) (string, error) {
	inferred, err := operationTarget(op, fragments)
	if err != nil {
		return "", err
	}
	annotated, err := annotatedTarget(op)
	if err != nil || annotated == "" {
		return inferred, err
	}
	if err := checkTargetPath(schema, op, fragments, annotated); err != nil {
		return "", fmt.Errorf("%s %s: %w", targetAnnotation, annotated, err)
	}
	return annotated, nil
}

// annotatedTarget returns the path of the @target line in the comments
// directly above op, or "" when there is none.
func annotatedTarget(op *ast.OperationDefinition) (string, error) {
	if op.Comment == nil {
		return "", nil
	}
	var found []string
	for _, c := range op.Comment.List {
		words := strings.Fields(c.Text())
		if len(words) == 0 || words[0] != targetAnnotation {
			continue
		}
		if len(words) != 2 {
			return "", fmt.Errorf("%q: %s takes exactly one path", strings.TrimSpace(c.Text()), targetAnnotation)
		}
		found = append(found, words[1])
	}
	switch len(found) {
	case 0:
		return "", nil
	case 1:
		return found[0], nil
	}
	return "", fmt.Errorf("%d %s annotations; an operation has one target", len(found), targetAnnotation)
}

// checkTargetPath checks that target (kind.field.field, a member type name
// after a field of union or interface type) exists in schema and that op
// selects every step of it.
func checkTargetPath(schema *ast.Schema, op *ast.OperationDefinition, fragments map[string]*ast.FragmentDefinition, target string) error {
	segments := strings.Split(target, ".")
	if len(segments) < 2 || segments[0] != string(op.Operation) {
		return fmt.Errorf("a %s operation's target is %s.<field>[.<field>...]", op.Operation, op.Operation)
	}
	parent := rootDefinition(schema, op.Operation)
	if parent == nil {
		return fmt.Errorf("the public schema has no %s root", op.Operation)
	}
	sets := []ast.SelectionSet{op.SelectionSet}
	for i, seg := range segments[1:] {
		last := i == len(segments)-2
		if member := abstractMember(schema, parent, seg); member != nil {
			if last {
				return fmt.Errorf("ends at type %s; a target is a field", seg)
			}
			var narrowed []ast.SelectionSet
			for _, set := range sets {
				narrowed = append(narrowed, typedSelections(set, fragments, seg)...)
			}
			if len(narrowed) == 0 {
				return fmt.Errorf("the operation has no ... on %s below %s", seg, strings.Join(segments[:i+1], "."))
			}
			parent, sets = member, narrowed
			continue
		}
		field := parent.Fields.ForName(seg)
		if field == nil || strings.HasPrefix(seg, "__") {
			return fmt.Errorf("%s has no public field %s", parent.Name, seg)
		}
		var next []ast.SelectionSet
		selected := false
		for _, set := range sets {
			for _, group := range responseFields(set, fragments) {
				for _, f := range group {
					if f.Name == seg {
						selected = true
						next = append(next, f.SelectionSet)
					}
				}
			}
		}
		if !selected {
			return fmt.Errorf("the operation does not select %s", strings.Join(segments[:i+2], "."))
		}
		parent, sets = schema.Types[field.Type.Name()], next
	}
	return nil
}

// abstractMember returns the member named name of parent when parent is a
// union or interface, and nil otherwise.
func abstractMember(schema *ast.Schema, parent *ast.Definition, name string) *ast.Definition {
	if parent.Kind != ast.Union && parent.Kind != ast.Interface {
		return nil
	}
	for _, m := range schema.GetPossibleTypes(parent) {
		if m.Name == name {
			return m
		}
	}
	return nil
}

// typedSelections returns the selection sets of the inline fragments and
// fragment spreads in set, at any fragment depth, whose type condition is
// typeName.
func typedSelections(set ast.SelectionSet, fragments map[string]*ast.FragmentDefinition, typeName string) []ast.SelectionSet {
	var out []ast.SelectionSet
	var walk func(ast.SelectionSet, map[string]bool)
	walk = func(set ast.SelectionSet, visiting map[string]bool) {
		for _, sel := range set {
			switch s := sel.(type) {
			case *ast.InlineFragment:
				if s.TypeCondition == typeName {
					out = append(out, s.SelectionSet)
					continue
				}
				walk(s.SelectionSet, visiting)
			case *ast.FragmentSpread:
				frag := fragments[s.Name]
				if frag == nil || visiting[s.Name] {
					continue
				}
				if frag.TypeCondition == typeName {
					out = append(out, frag.SelectionSet)
					continue
				}
				visiting[s.Name] = true
				walk(frag.SelectionSet, visiting)
				delete(visiting, s.Name)
			}
		}
	}
	walk(set, map[string]bool{})
	return out
}

// operationTarget returns the field an operation addresses, as
// kind.field.field: the operation's single root field, followed down while
// the selection below the current field (fragments expanded) holds exactly
// one field and that field has a selection of its own. A document that
// selects stream(id:) { analytics(range:) { ... } } targets
// query.stream.analytics; one that selects stream(id:) { id name } targets
// query.stream. An operation whose target this rule cannot express names it
// with a @target annotation (resolveTarget).
func operationTarget(op *ast.OperationDefinition, fragments map[string]*ast.FragmentDefinition) (string, error) {
	fields := responseFields(op.SelectionSet, fragments)
	if len(fields) != 1 {
		return "", fmt.Errorf("selects %d root fields; a public operation selects exactly one", len(fields))
	}
	path := []string{string(op.Operation)}
	for {
		name, set := singleField(fields)
		path = append(path, name)
		next := responseFields(set, fragments)
		if len(next) != 1 {
			break
		}
		if _, childSet := singleField(next); len(childSet) == 0 {
			break
		}
		fields = next
	}
	return strings.Join(path, "."), nil
}

// singleField returns the name of the one field group in fields and the
// merged selection of its occurrences.
func singleField(fields map[string][]*ast.Field) (string, ast.SelectionSet) {
	var name string
	var set ast.SelectionSet
	for _, group := range fields {
		name = group[0].Name
		for _, f := range group {
			set = append(set, f.SelectionSet...)
		}
	}
	return name, set
}

// responseFields groups the fields of a selection set, fragments expanded,
// by response name.
func responseFields(set ast.SelectionSet, fragments map[string]*ast.FragmentDefinition) map[string][]*ast.Field {
	out := map[string][]*ast.Field{}
	var walk func(ast.SelectionSet, map[string]bool)
	walk = func(set ast.SelectionSet, visiting map[string]bool) {
		for _, sel := range set {
			switch s := sel.(type) {
			case *ast.Field:
				key := s.Alias
				if key == "" {
					key = s.Name
				}
				out[key] = append(out[key], s)
			case *ast.InlineFragment:
				walk(s.SelectionSet, visiting)
			case *ast.FragmentSpread:
				frag := fragments[s.Name]
				if frag == nil || visiting[s.Name] {
					continue
				}
				visiting[s.Name] = true
				walk(frag.SelectionSet, visiting)
				delete(visiting, s.Name)
			}
		}
	}
	walk(set, map[string]bool{})
	return out
}

// fragmentClosure returns the names of every fragment a selection set uses,
// directly or through other fragments, sorted.
func fragmentClosure(set ast.SelectionSet, fragments map[string]*ast.FragmentDefinition) ([]string, error) {
	used := map[string]bool{}
	var walk func(ast.SelectionSet) error
	walk = func(set ast.SelectionSet) error {
		for _, sel := range set {
			switch s := sel.(type) {
			case *ast.Field:
				if err := walk(s.SelectionSet); err != nil {
					return err
				}
			case *ast.InlineFragment:
				if err := walk(s.SelectionSet); err != nil {
					return err
				}
			case *ast.FragmentSpread:
				if used[s.Name] {
					continue
				}
				frag, ok := fragments[s.Name]
				if !ok {
					return fmt.Errorf("unknown fragment %s", s.Name)
				}
				used[s.Name] = true
				if err := walk(frag.SelectionSet); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(set); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(used))
	for name := range used {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}
