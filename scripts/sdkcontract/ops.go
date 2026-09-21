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

// operation is one curated public operation with the fragments it uses,
// printed as a self-contained document.
type operation struct {
	Name     string
	Kind     string
	Document string
	Hash     string
	File     string
}

// loadOperations parses every .graphql file under pkg/graphql/public and
// returns one self-contained document per operation, sorted by name.
func loadOperations(repo string) ([]operation, error) {
	root := filepath.Join(repo, publicDir)
	var sources []*ast.Source
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".graphql" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(repo, path)
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
	return parseOperations(sources)
}

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
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
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
