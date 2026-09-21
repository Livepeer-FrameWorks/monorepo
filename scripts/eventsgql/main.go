// Command eventsgql generates the GraphQL types of the public tenant events
// from the event registry (pkg/events): an object type per public event
// message, the PublicEventData union of them, the PublicEvent envelope, and
// the gqlgen models that bind each type to its generated proto Go type, so
// resolvers return the proto message itself.
//
// It rewrites the delimited blocks in pkg/graphql/schema.graphql and
// api_gateway/gqlgen.yml. With -check it writes nothing and exits 1 when
// either block differs from the registry.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
)

const (
	schemaPath = "pkg/graphql/schema.graphql"
	modelsPath = "api_gateway/gqlgen.yml"
)

func main() {
	repo := flag.String("repo", ".", "repository root")
	check := flag.Bool("check", false, "report stale blocks instead of writing them")
	flag.Parse()

	stale, err := run(*repo, *check)
	if err != nil {
		fmt.Fprintln(os.Stderr, "eventsgql:", err)
		os.Exit(1)
	}
	if len(stale) > 0 {
		fmt.Fprintf(os.Stderr, "eventsgql: %s out of date with the event registry; run make graphql-events\n", strings.Join(stale, ", "))
		os.Exit(1)
	}
}

// registryEvents returns the public events of the linked registry.
func registryEvents() []eventMessage {
	var out []eventMessage
	for _, spec := range events.Specs() {
		if !spec.Public() {
			continue
		}
		out = append(out, eventMessage{
			Type:      spec.Type,
			Aggregate: spec.Aggregate,
			Desc:      spec.NewMessage().ProtoReflect().Descriptor(),
		})
	}
	return out
}

// render returns both files with their generated blocks replaced.
func render(schema, models string, evs []eventMessage) (string, string, error) {
	out, err := generate(evs)
	if err != nil {
		return "", "", err
	}
	handWritten := handWrittenTypeNames(schema)
	var clashes []string
	for _, name := range generatedTypeNames(out.Schema) {
		if handWritten[name] {
			clashes = append(clashes, name)
		}
	}
	if len(clashes) > 0 {
		sort.Strings(clashes)
		return "", "", fmt.Errorf("generated types also declared by hand in %s: %s", schemaPath, strings.Join(clashes, ", "))
	}
	newSchema, err := splice(schema, schemaBegin, schemaEnd, out.Schema)
	if err != nil {
		return "", "", fmt.Errorf("%s: %w", schemaPath, err)
	}
	newModels, err := splice(models, modelsBegin, modelsEnd, out.Models)
	if err != nil {
		return "", "", fmt.Errorf("%s: %w", modelsPath, err)
	}
	return newSchema, newModels, nil
}

func run(repo string, check bool) ([]string, error) {
	files := []string{filepath.Join(repo, schemaPath), filepath.Join(repo, modelsPath)}
	current := make([]string, len(files))
	for i, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		current[i] = string(raw)
	}
	schema, models, err := render(current[0], current[1], registryEvents())
	if err != nil {
		return nil, err
	}
	var stale []string
	for i, next := range []string{schema, models} {
		if next == current[i] {
			continue
		}
		if check {
			stale = append(stale, files[i])
			continue
		}
		if err := os.WriteFile(files[i], []byte(next), 0o644); err != nil {
			return nil, err
		}
	}
	return stale, nil
}
