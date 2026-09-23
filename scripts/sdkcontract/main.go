// Command sdkcontract maintains the public SDK operation contract in
// pkg/graphql/public: it writes the public schema (schema.graphql without its
// @internal fields) that every other step reads, generates the default
// operations (pkg/graphql/public/generated, defaultops.go), reports
// @experimental fields against their until release, lints the public
// operations, audits their coverage of the public schema, keeps the operation
// manifest of each SDK line (majors/v<N>.json), emits the per-language
// manifest and event registry the SDK runtimes compile in, and runs the two
// compatibility checks:
//
//	api-compat     every operation of every live SDK line validates against
//	               the schema of every stable release at or above the line's
//	               minimum server, and no operation's since rises within a line
//	schema-compat  the working-tree public schema makes no breaking change to
//	               anything reachable in the latest release tag's public
//	               schema; report only while that tag is older than the
//	               oldest live minimum server (schemadiff.go)
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
	}
	cmd, args := os.Args[1], os.Args[2:]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	repo := fs.String("repo", ".", "repository root")
	check := fs.Bool("check", false, "report stale output instead of writing it")
	lang := fs.String("lang", "", "emit: ts, go, or py")
	if err := fs.Parse(args); err != nil {
		printUsage()
	}

	var err error
	switch cmd {
	case "audit":
		err = runAudit(*repo)
	case "public-schema":
		err = runPublicSchema(*repo, *check)
	case "generate-ops":
		err = runGenerateOps(*repo, *check)
	case "experimental":
		err = runExperimental(*repo)
	case "reference":
		err = runReference(*repo, *check)
	case "lint":
		err = runLint(*repo)
	case "manifest":
		err = runManifest(*repo, *check)
	case "emit":
		err = runEmit(*repo, *lang, *check)
	case "api-compat":
		err = runAPICompat(*repo)
	case "schema-compat":
		err = runSchemaCompat(*repo)
	default:
		printUsage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "sdkcontract:", err)
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "usage: sdkcontract public-schema|generate-ops|experimental|audit|reference|lint|manifest|emit|api-compat|schema-compat [-repo DIR] [-check] [-lang ts|go|py]")
	os.Exit(2)
}

func failList(title string, items []string) error {
	return fmt.Errorf("%s:\n  %s", title, strings.Join(items, "\n  "))
}

func runLint(repo string) error {
	ops, err := loadOperations(repo)
	if err != nil {
		return err
	}
	schema, err := loadPublicSchema(repo, headRef)
	if err != nil {
		return err
	}
	if problems := lintOperations(schema, ops); len(problems) > 0 {
		return failList("public operations violate the contract", problems)
	}
	fmt.Printf("sdkcontract: %d public operations pass lint\n", len(ops))
	return nil
}

// currentLine computes the current line's manifest entry from the files.
func currentLine(repo string) (*supportFile, *manifest, manifestLine, error) {
	support, err := loadSupport(repo)
	if err != nil {
		return nil, nil, manifestLine{}, err
	}
	ops, err := loadOperations(repo)
	if err != nil {
		return nil, nil, manifestLine{}, err
	}
	head, err := loadPublicSchema(repo, headRef)
	if err != nil {
		return nil, nil, manifestLine{}, err
	}
	if problems := lintOperations(head, ops); len(problems) > 0 {
		return nil, nil, manifestLine{}, failList("public operations violate the contract", problems)
	}
	matrix, err := loadMatrix(repo)
	if err != nil {
		return nil, nil, manifestLine{}, err
	}
	line := support.line(support.Current)
	entries, problems := currentLineOperations(newSchemaCache(repo), matrix, *line, ops)
	if len(problems) > 0 {
		return nil, nil, manifestLine{}, failList("operations of line "+line.Line+" do not hold across releases", problems)
	}
	m, err := loadManifest(repo, majorOf(line.Line))
	if err != nil {
		return nil, nil, manifestLine{}, err
	}
	return support, m, manifestLine{MinServer: line.MinServer, Operations: entries}, nil
}

func runManifest(repo string, check bool) error {
	support, m, computed, err := currentLine(repo)
	if err != nil {
		return err
	}
	m.Lines[support.Current] = computed
	data, err := m.encode()
	if err != nil {
		return err
	}
	ops, err := loadOperations(repo)
	if err != nil {
		return err
	}
	head, err := loadPublicSchema(repo, headRef)
	if err != nil {
		return err
	}
	fixture, err := renderOperationsFixture(head, ops)
	if err != nil {
		return err
	}
	files := map[string]string{
		fmt.Sprintf("%s/v%d.json", majorsDir, m.Major): string(data),
		operationsFixturePath:                          fixture,
	}
	stale, err := writeOrCheck(repo, files, check)
	if err != nil {
		return err
	}
	if len(stale) > 0 {
		return failList("operation manifest out of date; run make sdk-manifest", stale)
	}
	fmt.Printf("sdkcontract: line %s manifest has %d operations\n", support.Current, len(computed.Operations))
	return nil
}

func runEmit(repo, lang string, check bool) error {
	support, err := loadSupport(repo)
	if err != nil {
		return err
	}
	line := support.line(support.Current)
	m, err := loadManifest(repo, majorOf(line.Line))
	if err != nil {
		return err
	}
	entry, ok := m.Lines[line.Line]
	if !ok {
		return fmt.Errorf("manifest has no line %s; run make sdk-manifest", line.Line)
	}
	version, err := sdkVersion(repo)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(version, line.Line+".") {
		return fmt.Errorf("%s version %s is not on the current line %s in %s", npmPackagePath, version, line.Line, supportPath)
	}
	in := emitInput{Version: version, Line: line.Line, MinServer: line.MinServer, Ops: entry.Operations, Events: publicEvents()}
	files, err := in.render(lang)
	if err != nil {
		return err
	}
	stale, err := writeOrCheck(repo, files, check)
	if err != nil {
		return err
	}
	if len(stale) > 0 {
		return failList("SDK contract files out of date; run make sdk-generate", stale)
	}
	return nil
}

func runAPICompat(repo string) error {
	support, committed, computed, err := currentLine(repo)
	if err != nil {
		return err
	}
	var problems []string
	cur, has := committed.Lines[support.Current]
	byName := map[string]manifestOperation{}
	if has {
		for _, op := range cur.Operations {
			byName[op.Name] = op
		}
	}
	for _, op := range computed.Operations {
		prev, ok := byName[op.Name]
		switch {
		case !ok || prev.Hash != op.Hash || prev.Since != op.Since || prev.Kind != op.Kind:
			problems = append(problems, fmt.Sprintf("%s: manifest entry is stale; run make sdk-manifest", op.Name))
		}
	}
	if len(byName) != len(computed.Operations) {
		problems = append(problems, "manifest lists operations the files no longer declare; run make sdk-manifest")
	}

	// Since and presence are fixed for a line once it is released.
	tag, err := latestSDKRelease(repo, support.Current)
	if err != nil {
		return err
	}
	if tag != "" {
		released, rerr := releasedLine(repo, tag, committed.Major, support.Current)
		if rerr != nil {
			return rerr
		}
		if released != nil {
			now := map[string]manifestOperation{}
			for _, op := range computed.Operations {
				now[op.Name] = op
			}
			for _, op := range released.Operations {
				cur, ok := now[op.Name]
				if !ok {
					problems = append(problems, fmt.Sprintf("%s: released in %s and removed within line %s", op.Name, tag, support.Current))
					continue
				}
				was, _ := parseSemver(op.Since)
				is, _ := parseSemver(cur.Since)
				if was.Less(is) {
					problems = append(problems, fmt.Sprintf("%s: since rose from %s (released in %s) to %s within line %s", op.Name, op.Since, tag, cur.Since, support.Current))
				}
			}
		}
	}

	// Frozen live lines keep validating on every later release.
	matrix, err := loadMatrix(repo)
	if err != nil {
		return err
	}
	cache := newSchemaCache(repo)
	for _, line := range support.Lines {
		if line.Status != lineLive || line.Line == support.Current {
			continue
		}
		frozen, ok := committed.Lines[line.Line]
		if !ok {
			problems = append(problems, fmt.Sprintf("line %s is live but has no manifest entry", line.Line))
			continue
		}
		min, _ := parseSemver(line.MinServer)
		for _, op := range frozen.Operations {
			since, err := sinceOf(cache, matrix, min, op.Document)
			if err != nil {
				problems = append(problems, fmt.Sprintf("line %s: %s: %v", line.Line, op.Name, err))
				continue
			}
			if was, _ := parseSemver(op.Since); was.Less(since) {
				problems = append(problems, fmt.Sprintf("line %s: %s: since rose from %s to %s", line.Line, op.Name, op.Since, since))
			}
		}
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		return failList("API compatibility check failed", problems)
	}

	line := support.line(support.Current)
	min, _ := parseSemver(line.MinServer)
	checked := []string{}
	for _, t := range matrix.from(min) {
		checked = append(checked, t.String())
	}
	checked = append(checked, "working tree ("+matrix.Pending.String()+")")
	if len(matrix.from(min)) == 0 {
		fmt.Printf("sdkcontract: no stable release at or above %s yet; validated against the working tree only\n", line.MinServer)
	}
	fmt.Printf("sdkcontract: %d operations of line %s validate on %s\n", len(computed.Operations), support.Current, strings.Join(checked, ", "))
	return nil
}
