package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// HashInputs describes everything that contributes to a Go service's
// source-hash. Building it explicitly (rather than walking arbitrary file
// trees) lets us reason about exactly what triggers a rebuild and lets us
// unit-test the contributing pieces in isolation.
type HashInputs struct {
	// MonorepoRoot is the repository root; all paths below are joined to it.
	MonorepoRoot string

	// Component carries the release-components.json entry. The Name +
	// Context + Cmd + CGO + DarwinBinary fields all feed the hash.
	Component ReleaseComponent

	// WorkflowSalt is a content-hash of release.yml + this tool's own
	// source tree. Any change to build logic invalidates every artefact.
	WorkflowSalt string

	// GoToolchainVersion is the contents of .go-version. A toolchain bump
	// invalidates every Go artefact.
	GoToolchainVersion string
}

// ComputeServiceSourceHash returns a sha256:<hex> string that fingerprints
// every input that affects the service binary's output. The recipe matches
// docs/architecture/build-and-packaging.md "What goes in the hash":
//
//   - All Go source files in the import closure of the `cmd` target,
//     restricted to packages from the monorepo (the service module + the
//     pkg/ replace target).
//   - service go.mod, go.sum.
//   - pkg/go.mod, pkg/go.sum (required because of the `replace ../pkg`
//     directive).
//   - The exact .proto source closure: the .proto behind each generated proto
//     package in the Go import closure, plus their transitive proto imports -
//     never every .proto under pkg/proto. Committed generated *.pb.go are
//     already hashed via the Go closure; CI (`make proto` + git diff) keeps
//     them in sync with source, so this layer is defense-in-depth, not the
//     sole signal.
//   - Explicit extra hash paths from release-components.json (other non-Go
//     contract inputs).
//   - The service Dockerfile.
//   - The release-components.json entry (cgo, darwin_binary flags).
//   - Go toolchain version.
//   - Workflow salt.
//
// Test files (*_test.go) are excluded; they don't affect the binary.
func ComputeServiceSourceHash(inputs HashInputs) (string, []string, error) {
	h := sha256.New()
	var contributingFiles []string

	// `go list` runs against on-disk source and reads no network. A 2-minute
	// timeout is generous for the slowest service in the repo while still
	// guarding against a wedged toolchain.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// 1. Source closure: go list -deps -json $cmd from the service module, for
	// every platform this component actually ships. The release matrix builds
	// each service native binary for linux/amd64 and linux/arm64, plus
	// darwin/arm64 when darwin_binary is set. A platform-specific file (e.g.
	// *_darwin.go) can be absent from another platform's closure, so the hash is
	// the UNION across platforms, or a platform-only change would be carried
	// forward incorrectly.
	platforms := []struct{ goos, goarch string }{{"linux", "amd64"}, {"linux", "arm64"}}
	if inputs.Component.DarwinBinary {
		platforms = append(platforms, struct{ goos, goarch string }{"darwin", "arm64"})
	}
	fileSet := map[string]struct{}{}
	for _, p := range platforms {
		platFiles, listErr := goListDepSources(ctx, inputs.MonorepoRoot, inputs.Component, p.goos, p.goarch)
		if listErr != nil {
			return "", nil, fmt.Errorf("go list deps for %s (%s/%s): %w", inputs.Component.Name, p.goos, p.goarch, listErr)
		}
		for _, f := range platFiles {
			fileSet[f] = struct{}{}
		}
	}
	files := make([]string, 0, len(fileSet))
	for f := range fileSet {
		files = append(files, f)
	}
	sort.Strings(files)
	for _, f := range files {
		if fileErr := hashFileRel(h, inputs.MonorepoRoot, f, &contributingFiles); fileErr != nil {
			return "", nil, fmt.Errorf("hash %s: %w", f, fileErr)
		}
	}

	// 2. Service module go.mod + go.sum.
	for _, rel := range []string{"go.mod", "go.sum"} {
		path := filepath.Join(inputs.MonorepoRoot, inputs.Component.Context, rel)
		if fileErr := hashFileRelIfExists(h, inputs.MonorepoRoot, path, &contributingFiles); fileErr != nil {
			return "", nil, fileErr
		}
	}

	// 3. pkg/go.mod + pkg/go.sum (required by the `replace ../pkg` directive).
	for _, rel := range []string{"go.mod", "go.sum"} {
		path := filepath.Join(inputs.MonorepoRoot, "pkg", rel)
		if fileErr := hashFileRelIfExists(h, inputs.MonorepoRoot, path, &contributingFiles); fileErr != nil {
			return "", nil, fileErr
		}
	}

	// 4. Contract/source inputs that are not visible to go list. The .proto
	// definitions are part of a service's ABI even before the generated .pb.go
	// files are refreshed, so we hash the EXACT proto-source closure: only the
	// .proto files behind the generated proto packages actually in this
	// service's Go import closure (plus their transitive proto imports), never
	// every .proto under pkg/proto. This keeps an unrelated proto edit from
	// invalidating services that don't compile it.
	extraFiles, err := collectExtraHashFiles(inputs.MonorepoRoot, inputs.Component.ExtraHashPaths)
	if err != nil {
		return "", nil, err
	}
	protoSources, err := collectProtoSourceClosure(inputs.MonorepoRoot, files)
	if err != nil {
		return "", nil, err
	}
	extraFiles = append(extraFiles, protoSources...)
	extraFiles = uniqueSortedPaths(extraFiles)
	for _, f := range extraFiles {
		if fileErr := hashFileRel(h, inputs.MonorepoRoot, f, &contributingFiles); fileErr != nil {
			return "", nil, fmt.Errorf("hash %s: %w", f, fileErr)
		}
	}

	// 5. Service Dockerfile.
	if inputs.Component.Dockerfile != "" {
		path := filepath.Join(inputs.MonorepoRoot, inputs.Component.Dockerfile)
		if fileErr := hashFileRelIfExists(h, inputs.MonorepoRoot, path, &contributingFiles); fileErr != nil {
			return "", nil, fileErr
		}
	}

	// 6. release-components.json entry, serialized canonically so flag
	// changes (cgo, darwin_binary) invalidate the hash.
	entryJSON, err := json.Marshal(inputs.Component)
	if err != nil {
		return "", nil, fmt.Errorf("marshal component entry: %w", err)
	}
	mustWrite(h, []byte("component-entry:"), entryJSON, []byte("\n"))

	// 7. Go toolchain version.
	mustWrite(h, []byte("go-toolchain:"), []byte(inputs.GoToolchainVersion), []byte("\n"))

	// 8. Workflow salt.
	mustWrite(h, []byte("workflow-salt:"), []byte(inputs.WorkflowSalt), []byte("\n"))

	return "sha256:" + hex.EncodeToString(h.Sum(nil)), contributingFiles, nil
}

// collectProtoSourceClosure returns the exact set of pkg/proto/*.proto source
// files that contribute to a service's binary: the .proto behind every
// generated proto package in the Go import closure, transitively following each
// .proto's own `import "...";` statements. Resolution maps a generated package
// back to its source via the protoc `// source:` header (the authoritative
// invariant) rather than assuming the directory basename equals the proto stem.
//
// closureFiles is the absolute-path Go import closure from goListDepSources;
// only its pkg/proto/<x>/*.pb.go entries seed the proto closure.
func collectProtoSourceClosure(monorepoRoot string, closureFiles []string) ([]string, error) {
	protoDir := filepath.Join(monorepoRoot, "pkg", "proto")

	seeds := map[string]struct{}{}
	for _, f := range closureFiles {
		if !isUnderRelPath(monorepoRoot, f, "pkg/proto") {
			continue
		}
		if !strings.HasSuffix(f, ".pb.go") {
			continue
		}
		source, err := readProtoSourceHeader(f)
		if err != nil {
			return nil, err
		}
		if source == "" {
			continue
		}
		seeds[filepath.Join(protoDir, source)] = struct{}{}
	}

	// BFS the .proto import graph so a service that compiles package A also
	// hashes the protos A imports (e.g. ipc.proto -> common.proto).
	visited := map[string]struct{}{}
	queue := make([]string, 0, len(seeds))
	for s := range seeds {
		queue = append(queue, s)
	}
	for len(queue) > 0 {
		path := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if _, ok := visited[path]; ok {
			continue
		}
		visited[path] = struct{}{}

		imports, err := readProtoImports(path)
		if err != nil {
			return nil, err
		}
		for _, imp := range imports {
			// Resolve by presence: an import vendored under pkg/proto (including
			// the local google/protobuf/*.proto well-known-type fixtures) is a
			// real source input and is hashed; one that resolves nowhere in the
			// repo is external (its identity rides in go.sum / the protoc pin).
			resolved := filepath.Join(protoDir, filepath.FromSlash(imp))
			if _, statErr := os.Stat(resolved); statErr != nil {
				if os.IsNotExist(statErr) {
					continue // external import; identity rides in go.sum / the protoc pin
				}
				// Any other stat error (permission, symlink loop, bad path) is
				// non-deterministic; fail loudly rather than silently dropping.
				return nil, fmt.Errorf("stat proto import %q: %w", imp, statErr)
			}
			queue = append(queue, resolved)
		}
	}

	out := make([]string, 0, len(visited))
	for path := range visited {
		out = append(out, path)
	}
	sort.Strings(out)
	return out, nil
}

// readProtoSourceHeader returns the source .proto filename recorded in a
// protoc-generated file's `// source: <name>.proto` header (e.g. "ipc.proto"),
// or "" if the file carries no such header.
func readProtoSourceHeader(pbGoPath string) (string, error) {
	b, err := os.ReadFile(pbGoPath)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", pbGoPath, err)
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "// source:"); ok {
			return strings.TrimSpace(rest), nil
		}
		// The header block is at the top of the file; stop once code starts.
		if strings.HasPrefix(line, "package ") {
			break
		}
	}
	return "", nil
}

// readProtoImports parses the `import "...";` statements from a .proto file. A
// missing file is treated as no imports so a stale generated reference never
// fails the hash (the Go build, not this tool, is the source of truth for
// existence).
func readProtoImports(protoPath string) ([]string, error) {
	b, err := os.ReadFile(protoPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", protoPath, err)
	}
	var out []string
	for line := range strings.SplitSeq(string(b), "\n") {
		line = strings.TrimSpace(line)
		rest, ok := strings.CutPrefix(line, "import ")
		if !ok {
			continue
		}
		// Skip the optional "public"/"weak" qualifier.
		rest = strings.TrimSpace(rest)
		rest = strings.TrimPrefix(rest, "public ")
		rest = strings.TrimPrefix(rest, "weak ")
		rest = strings.TrimSpace(rest)
		if !strings.HasPrefix(rest, `"`) {
			continue
		}
		path, _, ok := strings.Cut(rest[1:], `"`)
		if !ok {
			continue
		}
		out = append(out, path)
	}
	return out, nil
}

func collectExtraHashFiles(root string, paths []string) ([]string, error) {
	seen := map[string]struct{}{}
	for _, raw := range paths {
		rel := filepath.Clean(strings.TrimSpace(raw))
		if rel == "." || rel == "" {
			return nil, fmt.Errorf("extra_hash_paths contains empty path")
		}
		if filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("extra_hash_paths entry %q must stay under the repository root", raw)
		}

		abs := filepath.Join(root, rel)
		info, err := os.Stat(abs)
		if err != nil {
			return nil, fmt.Errorf("stat extra_hash_paths entry %q: %w", raw, err)
		}
		if !info.IsDir() {
			seen[abs] = struct{}{}
			continue
		}
		if walkErr := filepath.WalkDir(abs, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			seen[path] = struct{}{}
			return nil
		}); walkErr != nil {
			return nil, fmt.Errorf("walk extra_hash_paths entry %q: %w", raw, walkErr)
		}
	}

	out := make([]string, 0, len(seen))
	for path := range seen {
		out = append(out, path)
	}
	sort.Strings(out)
	return out, nil
}

func uniqueSortedPaths(paths []string) []string {
	seen := map[string]struct{}{}
	for _, path := range paths {
		seen[path] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for path := range seen {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

// dirWithinRepo reports whether dir is inside root, tolerating symlinked paths
// (go list canonicalizes Dir, e.g. /var -> /private/var on macOS, while root may
// not be canonical).
func dirWithinRepo(root, dir string) bool {
	if relWithinRoot(root, dir) {
		return true
	}
	cr, e1 := filepath.EvalSymlinks(root)
	cd, e2 := filepath.EvalSymlinks(dir)
	if e1 != nil || e2 != nil {
		return false
	}
	return relWithinRoot(cr, cd)
}

func relWithinRoot(root, dir string) bool {
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func isUnderRelPath(root, path, relDir string) bool {
	if isUnderRelPathRaw(root, path, relDir) {
		return true
	}
	canonicalRoot, rootErr := filepath.EvalSymlinks(root)
	canonicalPath, pathErr := filepath.EvalSymlinks(path)
	if rootErr != nil || pathErr != nil {
		return false
	}
	return isUnderRelPathRaw(canonicalRoot, canonicalPath, relDir)
}

func isUnderRelPathRaw(root, path, relDir string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(filepath.Clean(rel))
	relDir = filepath.ToSlash(filepath.Clean(relDir))
	return rel == relDir || strings.HasPrefix(rel, relDir+"/")
}

// goListDeps walks the import closure of the cmd target via `go list -deps
// -json` and returns the sorted, deduplicated list of source files (no
// *_test.go) for monorepo-internal packages. Packages from the standard
// library and third-party modules are excluded; their identity is
// captured via go.sum.
func goListDepSources(ctx context.Context, monorepoRoot string, comp ReleaseComponent, goos, goarch string) ([]string, error) {
	moduleDir := filepath.Join(monorepoRoot, comp.Context)
	target := comp.Cmd
	if target == "" {
		target = "./..."
	}
	cmd := exec.CommandContext(ctx, "go", "list", "-deps", "-json", target)
	cmd.Dir = moduleDir
	cmd.Env = canonicalGoListEnv(os.Environ(), comp.CGO, goos, goarch)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("go list -deps -json %s (in %s): %w\nstderr: %s", target, moduleDir, err, stderr.String())
	}

	// Find the service module's path so we know which deps are
	// monorepo-internal. We accept any package whose ImportPath either
	// starts with the service module (e.g. frameworks/api_billing/...)
	// or with the pkg module (github.com/Livepeer-FrameWorks/monorepo/pkg/...).
	servicePrefix, err := readModulePath(filepath.Join(moduleDir, "go.mod"))
	if err != nil {
		return nil, err
	}
	pkgPrefix := "github.com/Livepeer-FrameWorks/monorepo/pkg"

	dec := json.NewDecoder(&stdout)
	seen := map[string]struct{}{}
	for dec.More() {
		var pkg goListPkg
		if err := dec.Decode(&pkg); err != nil {
			return nil, fmt.Errorf("decode go list output: %w", err)
		}
		internal := isMonorepoPackage(pkg.ImportPath, servicePrefix, pkgPrefix)
		// A file-target cmd (e.g. "cmd/decklog/main.go") lists as the synthetic
		// "command-line-arguments" package; accept its files when they live in
		// the monorepo so the entrypoint isn't silently dropped from the hash.
		if !internal && pkg.ImportPath == "command-line-arguments" && dirWithinRepo(monorepoRoot, pkg.Dir) {
			internal = true
		}
		if !internal {
			continue
		}
		for _, set := range [][]string{pkg.GoFiles, pkg.CgoFiles, pkg.CFiles, pkg.CXXFiles, pkg.HFiles, pkg.EmbedFiles, pkg.SFiles} {
			for _, file := range set {
				abs := filepath.Join(pkg.Dir, file)
				seen[abs] = struct{}{}
			}
		}
	}

	out := make([]string, 0, len(seen))
	for f := range seen {
		out = append(out, f)
	}
	sort.Strings(out)
	return out, nil
}

func canonicalGoListEnv(base []string, cgo bool, goos, goarch string) []string {
	out := make([]string, 0, len(base)+4)
	for _, kv := range base {
		key, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		switch key {
		case "GOFLAGS", "GOOS", "GOARCH", "CGO_ENABLED":
			continue
		default:
			out = append(out, kv)
		}
	}
	cgoEnabled := "0"
	if cgo {
		cgoEnabled = "1"
	}
	return append(out,
		"GOFLAGS=",
		"GOOS="+goos,
		"GOARCH="+goarch,
		"CGO_ENABLED="+cgoEnabled,
	)
}

// goListPkg is the subset of `go list -json` we read. See `go help list`
// for the full schema.
type goListPkg struct {
	Dir        string   `json:"Dir"`
	ImportPath string   `json:"ImportPath"`
	GoFiles    []string `json:"GoFiles"`
	CgoFiles   []string `json:"CgoFiles"`
	CFiles     []string `json:"CFiles"`
	CXXFiles   []string `json:"CXXFiles"`
	HFiles     []string `json:"HFiles"`
	SFiles     []string `json:"SFiles"`
	EmbedFiles []string `json:"EmbedFiles"`
}

func isMonorepoPackage(importPath, servicePrefix, pkgPrefix string) bool {
	if servicePrefix != "" && (importPath == servicePrefix || strings.HasPrefix(importPath, servicePrefix+"/")) {
		return true
	}
	if importPath == pkgPrefix || strings.HasPrefix(importPath, pkgPrefix+"/") {
		return true
	}
	return false
}

// readModulePath extracts the module path from a go.mod file. The first
// non-comment `module` directive wins.
func readModulePath(goModPath string) (string, error) {
	b, err := os.ReadFile(goModPath)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", goModPath, err)
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "module ") {
			continue
		}
		path := strings.TrimSpace(strings.TrimPrefix(line, "module"))
		path = strings.Trim(path, `"`)
		return path, nil
	}
	return "", fmt.Errorf("no module directive in %s", goModPath)
}

func hashFileRel(h hash.Hash, root, path string, contributingFiles *[]string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	label := filepath.Clean(path)
	if rel, relErr := filepath.Rel(root, path); relErr == nil && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".." {
		label = filepath.ToSlash(rel)
	}
	mustWrite(h, []byte("file:"), []byte(label), []byte("\n"), b, []byte("\n"))
	if contributingFiles != nil {
		*contributingFiles = append(*contributingFiles, path)
	}
	return nil
}

func hashFileRelIfExists(h hash.Hash, root, path string, contributingFiles *[]string) error {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return hashFileRel(h, root, path, contributingFiles)
}

// mustWrite is documented in hash.Hash.Write as never returning an error.
// We treat the call as infallible to keep the call sites readable.
func mustWrite(h hash.Hash, parts ...[]byte) {
	for _, p := range parts {
		if _, err := h.Write(p); err != nil {
			panic(fmt.Errorf("hash.Hash.Write returned an error (violates contract): %w", err))
		}
	}
}

// HashWorkflowFiles returns sha256:<hex> over release.yml + the
// release-plan tool's own source tree (excluding test files). This is
// the WorkflowSalt; a change to build logic forces all components to
// rebuild on the next release.
func HashWorkflowFiles(monorepoRoot string) (string, error) {
	h := sha256.New()
	paths := []string{
		filepath.Join(monorepoRoot, ".github", "workflows", "release.yml"),
	}
	toolDir := filepath.Join(monorepoRoot, "tools", "release-plan")
	if err := filepath.Walk(toolDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			paths = append(paths, path)
		}
		return nil
	}); err != nil {
		return "", fmt.Errorf("walk %s: %w", toolDir, err)
	}
	sort.Strings(paths)
	for _, p := range paths {
		if err := hashFileRel(h, monorepoRoot, p, nil); err != nil {
			return "", err
		}
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// ReadGoToolchainVersion returns the contents of .go-version, trimmed.
func ReadGoToolchainVersion(monorepoRoot string) (string, error) {
	b, err := os.ReadFile(filepath.Join(monorepoRoot, ".go-version"))
	if err != nil {
		return "", fmt.Errorf("read .go-version: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}
