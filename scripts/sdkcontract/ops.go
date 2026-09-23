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
	// Target is the field the operation addresses (operationTarget), as
	// kind.field.field.
	Target string
}

// loadOperations parses every operation document under pkg/graphql/public,
// hand-written and generated, and returns one self-contained document per
// operation, sorted by name.
func loadOperations(repo string) ([]operation, error) {
	sources, err := operationSources(repo, true)
	if err != nil {
		return nil, err
	}
	return parseOperations(sources)
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

// parseOperations returns the operations of sources. Operation and fragment
// names are unique, every operation selects one root field, and no two
// operations address the same target.
func parseOperations(sources []*ast.Source) ([]operation, error) {
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
		target, err := operationTarget(item.op, fragments)
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
		out = append(out, operation{
			Name:     item.op.Name,
			Kind:     string(item.op.Operation),
			Document: text,
			Hash:     "sha256:" + hex.EncodeToString(sum[:]),
			File:     item.file,
			Target:   target,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// operationTarget returns the field an operation addresses, as
// kind.field.field: the operation's single root field, followed down while
// the selection below the current field (fragments expanded) holds exactly
// one field and that field has a selection of its own. A document that
// selects stream(id:) { analytics(range:) { ... } } targets
// query.stream.analytics; one that selects stream(id:) { id name } targets
// query.stream.
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
