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
//   - The service Dockerfile.
//   - The release-components.json entry (cgo, darwin_binary flags).
//   - Go toolchain version.
//   - Workflow salt.
//
// Test files (*_test.go) are excluded — they don't affect the binary.
func ComputeServiceSourceHash(inputs HashInputs) (string, []string, error) {
	h := sha256.New()
	var contributingFiles []string

	// `go list` runs against on-disk source and reads no network. A 2-minute
	// timeout is generous for the slowest service in the repo while still
	// guarding against a wedged toolchain.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// 1. Source closure: go list -deps -json $cmd from the service module.
	files, err := goListDepSources(ctx, inputs.MonorepoRoot, inputs.Component)
	if err != nil {
		return "", nil, fmt.Errorf("go list deps for %s: %w", inputs.Component.Name, err)
	}
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

	// 4. Service Dockerfile.
	if inputs.Component.Dockerfile != "" {
		path := filepath.Join(inputs.MonorepoRoot, inputs.Component.Dockerfile)
		if fileErr := hashFileRelIfExists(h, inputs.MonorepoRoot, path, &contributingFiles); fileErr != nil {
			return "", nil, fileErr
		}
	}

	// 5. release-components.json entry — serialized canonically so flag
	// changes (cgo, darwin_binary) invalidate the hash.
	entryJSON, err := json.Marshal(inputs.Component)
	if err != nil {
		return "", nil, fmt.Errorf("marshal component entry: %w", err)
	}
	mustWrite(h, []byte("component-entry:"), entryJSON, []byte("\n"))

	// 6. Go toolchain version.
	mustWrite(h, []byte("go-toolchain:"), []byte(inputs.GoToolchainVersion), []byte("\n"))

	// 7. Workflow salt.
	mustWrite(h, []byte("workflow-salt:"), []byte(inputs.WorkflowSalt), []byte("\n"))

	return "sha256:" + hex.EncodeToString(h.Sum(nil)), contributingFiles, nil
}

// goListDeps walks the import closure of the cmd target via `go list -deps
// -json` and returns the sorted, deduplicated list of source files (no
// *_test.go) for monorepo-internal packages. Packages from the standard
// library and third-party modules are excluded — their identity is
// captured via go.sum.
func goListDepSources(ctx context.Context, monorepoRoot string, comp ReleaseComponent) ([]string, error) {
	moduleDir := filepath.Join(monorepoRoot, comp.Context)
	target := comp.Cmd
	if target == "" {
		target = "./..."
	}
	cmd := exec.CommandContext(ctx, "go", "list", "-deps", "-json", target)
	cmd.Dir = moduleDir
	cmd.Env = append(os.Environ(), "GOFLAGS=") // Strip caller's flags; we want canonical output.
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
		if !isMonorepoPackage(pkg.ImportPath, servicePrefix, pkgPrefix) {
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
	for _, line := range strings.Split(string(b), "\n") {
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
// release-plan tool's own source tree (excluding generated files). This is
// the WorkflowSalt — a change to build logic forces all components to
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
		if strings.HasSuffix(path, ".go") {
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
