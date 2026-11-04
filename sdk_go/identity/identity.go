package identity

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/lexer"
	"github.com/vektah/gqlparser/v2/parser"
)

var identities = struct {
	sync.Mutex
	values map[[2]string]string
	order  [][2]string
}{values: make(map[[2]string]string)}

// Sum identifies a selected operation and its reachable fragments.
// Tokens retain their kind and UTF-8 byte length, so formatting and comments
// do not change identity while literals, aliases, defaults, and order do.
func Sum(query, operationName string) (string, error) {
	key := [2]string{query, operationName}
	identities.Lock()
	value, exists := identities.values[key]
	identities.Unlock()
	if exists {
		return value, nil
	}
	value, err := sum(query, operationName)
	if err == nil && len(query) <= 256*1024 {
		identities.Lock()
		if _, exists := identities.values[key]; !exists {
			if len(identities.order) == 256 {
				delete(identities.values, identities.order[0])
				identities.order = identities.order[1:]
			}
			identities.values[key] = value
			identities.order = append(identities.order, key)
		}
		identities.Unlock()
	}
	return value, err
}

func sum(query, operationName string) (string, error) {
	query, err := normalizeStrings(query)
	if err != nil {
		return "", err
	}
	doc, err := parser.ParseQuery(&ast.Source{Input: query})
	if err != nil {
		return "", err
	}
	if operationName == "" && len(doc.Operations) != 1 {
		return "", fmt.Errorf("document must select exactly one GraphQL operation")
	}
	operationNames := map[string]bool{}
	for _, operation := range doc.Operations {
		if operationNames[operation.Name] {
			return "", fmt.Errorf("duplicate operation %q", operation.Name)
		}
		operationNames[operation.Name] = true
	}
	op := doc.Operations.ForName(operationName)
	if op == nil {
		return "", fmt.Errorf("operation %q not found", operationName)
	}
	fragments := map[string]*ast.FragmentDefinition{}
	for _, f := range doc.Fragments {
		if fragments[f.Name] != nil {
			return "", fmt.Errorf("duplicate fragment %q", f.Name)
		}
		fragments[f.Name] = f
	}
	used := map[string]bool{}
	active := map[string]bool{}
	var walk func(ast.SelectionSet) error
	walk = func(set ast.SelectionSet) error {
		for _, selection := range set {
			switch s := selection.(type) {
			case *ast.Field:
				if err := walk(s.SelectionSet); err != nil {
					return err
				}
			case *ast.InlineFragment:
				if err := walk(s.SelectionSet); err != nil {
					return err
				}
			case *ast.FragmentSpread:
				if active[s.Name] {
					return fmt.Errorf("cyclic fragment %q", s.Name)
				}
				if used[s.Name] {
					continue
				}
				f := fragments[s.Name]
				if f == nil {
					return fmt.Errorf("missing fragment %q", s.Name)
				}
				active[s.Name] = true
				if err := walk(f.SelectionSet); err != nil {
					return err
				}
				delete(active, s.Name)
				used[s.Name] = true
			}
		}
		return nil
	}
	if err := walk(op.SelectionSet); err != nil {
		return "", err
	}
	positions := []int{}
	for _, operation := range doc.Operations {
		positions = append(positions, operation.Position.Start)
	}
	for _, fragment := range doc.Fragments {
		positions = append(positions, fragment.Position.Start)
	}
	sort.Ints(positions)
	runes := []rune(query)
	ends := map[int]int{}
	for i, start := range positions {
		end := len(runes)
		if i+1 < len(positions) {
			end = positions[i+1]
		}
		ends[start] = end
	}
	parts := []int{op.Position.Start}
	names := make([]string, 0, len(used))
	for name := range used {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		parts = append(parts, fragments[name].Position.Start)
	}
	var canonical strings.Builder
	canonical.WriteString("fw-operation-v1;")
	first := true
	for _, start := range parts {
		scanner := lexer.New(&ast.Source{Input: string(runes[start:ends[start]])})
		for {
			token, err := scanner.ReadToken()
			if err != nil {
				return "", err
			}
			if token.Kind == lexer.EOF {
				break
			}
			if token.Kind == lexer.Comment {
				continue
			}
			if first && token.Kind == lexer.BraceL {
				canonical.WriteString("Name:5:query;")
			}
			first = false
			kind := token.Kind.String()
			if token.Kind == lexer.BlockString {
				kind = "String"
			}
			value := token.Value
			if token.Kind < lexer.Name {
				value = ""
			}
			fmt.Fprintf(&canonical, "%s:%d:%s;", kind, len(value), value)
		}
	}
	sum := sha256.Sum256([]byte(canonical.String()))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
