package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// WebappHashInputs describes everything that contributes to a webapp's
// source-hash. Mirrors HashInputs for Go services so the two recipes have
// the same shape at the call site.
type WebappHashInputs struct {
	MonorepoRoot         string
	Webapp               ReleaseWebapp
	WorkflowSalt         string
	NodeToolchainVersion string
}

// ComputeWebappSourceHash returns sha256:<hex> over everything that affects
// the webapp's built bundle. Recipe (docs/architecture/build-and-packaging.md
// "What goes in the hash"):
//
//   - Files under <context>/ excluding node_modules and the build output
//     directory (build_dir).
//   - Root pnpm-lock.yaml, hashed in full.
//   - Root pnpm-workspace.yaml.
//   - Repository paths in the webapp's extra_hash_paths declaration. These
//     cover workspace packages and other Docker COPY inputs outside <context>.
//   - <context>/Dockerfile if present.
//   - For chartroom (the GraphQL-codegen consumer): pkg/graphql/schema.graphql,
//     pkg/graphql/operations/**/*.gql, and the webapp's houdini.config.*.
//   - Webapp's entry in release-components.json (env_prefix, build_dir).
//   - Node toolchain version (.nvmrc or package.json engines).
//   - Workflow salt.
func ComputeWebappSourceHash(inputs WebappHashInputs) (string, []string, error) {
	h := sha256.New()
	var contributing []string

	// 1. Walk the webapp source tree, excluding build output + node_modules.
	contextAbs := filepath.Join(inputs.MonorepoRoot, inputs.Webapp.Context)
	buildDir := strings.TrimSpace(inputs.Webapp.BuildDir)
	if buildDir == "" {
		buildDir = "build"
	}
	excludeDirs := map[string]struct{}{
		"node_modules": {},
		buildDir:       {},
		".svelte-kit":  {}, // SvelteKit generated dir; regenerated on each build
		"$houdini":     {}, // Houdini generated dir; regenerated from schema/operations
		".astro":       {}, // Astro generated cache
	}
	var sourceFiles []string
	if err := filepath.Walk(contextAbs, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			rel, relErr := filepath.Rel(contextAbs, path)
			if relErr != nil {
				return relErr
			}
			parts := strings.Split(rel, string(filepath.Separator))
			for _, p := range parts {
				if _, skip := excludeDirs[p]; skip {
					return filepath.SkipDir
				}
			}
			return nil
		}
		sourceFiles = append(sourceFiles, path)
		return nil
	}); err != nil {
		return "", nil, fmt.Errorf("walk webapp source %s: %w", contextAbs, err)
	}
	sort.Strings(sourceFiles)
	for _, f := range sourceFiles {
		if err := hashFileRel(h, inputs.MonorepoRoot, f, &contributing); err != nil {
			return "", nil, err
		}
	}

	// 2. Root pnpm workspace + lock.
	for _, rel := range []string{"pnpm-workspace.yaml", "pnpm-lock.yaml", "package.json", ".npmrc"} {
		path := filepath.Join(inputs.MonorepoRoot, rel)
		if err := hashFileRelIfExists(h, inputs.MonorepoRoot, path, &contributing); err != nil {
			return "", nil, err
		}
	}

	// 3. Workspace packages and other repository-root Docker inputs declared by
	// the component. Generated dependency trees and build outputs are excluded;
	// only their reproducible source inputs belong in the release identity.
	extraFiles, err := collectWebappExtraHashFiles(inputs.MonorepoRoot, inputs.Webapp.ExtraHashPaths)
	if err != nil {
		return "", nil, err
	}
	for _, f := range extraFiles {
		if hashErr := hashFileRel(h, inputs.MonorepoRoot, f, &contributing); hashErr != nil {
			return "", nil, fmt.Errorf("hash %s: %w", f, hashErr)
		}
	}

	// 4. GraphQL codegen inputs: chartroom (and any other webapp consuming
	// pkg/graphql/operations) is downstream of schema + operations + houdini
	// config. Hash defensively whenever the webapp's package.json mentions
	// houdini, rather than name-matching "chartroom".
	if webappConsumesGraphQL(contextAbs) {
		schemaPath := filepath.Join(inputs.MonorepoRoot, "pkg", "graphql", "schema.graphql")
		if hashErr := hashFileRelIfExists(h, inputs.MonorepoRoot, schemaPath, &contributing); hashErr != nil {
			return "", nil, hashErr
		}
		opsDir := filepath.Join(inputs.MonorepoRoot, "pkg", "graphql", "operations")
		var opsFiles []string
		if walkErr := filepath.Walk(opsDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				// Tolerate a missing pkg/graphql/operations dir (only some
				// monorepo checkouts have it). Other errors propagate.
				if os.IsNotExist(err) {
					return filepath.SkipDir
				}
				return err
			}
			if !info.IsDir() && strings.HasSuffix(path, ".gql") {
				opsFiles = append(opsFiles, path)
			}
			return nil
		}); walkErr != nil && !os.IsNotExist(walkErr) {
			return "", nil, fmt.Errorf("walk graphql operations: %w", walkErr)
		}
		sort.Strings(opsFiles)
		for _, f := range opsFiles {
			if hashErr := hashFileRel(h, inputs.MonorepoRoot, f, &contributing); hashErr != nil {
				return "", nil, hashErr
			}
		}
	}

	// 5. release-components entry (env_prefix, build_dir, extra hash paths).
	entryJSON, err := json.Marshal(inputs.Webapp)
	if err != nil {
		return "", nil, err
	}
	mustWrite(h, []byte("webapp-entry:"), entryJSON, []byte("\n"))

	// 6. Node toolchain.
	mustWrite(h, []byte("node-toolchain:"), []byte(inputs.NodeToolchainVersion), []byte("\n"))

	// 7. Workflow salt.
	mustWrite(h, []byte("workflow-salt:"), []byte(inputs.WorkflowSalt), []byte("\n"))

	return "sha256:" + hex.EncodeToString(h.Sum(nil)), contributing, nil
}

func collectWebappExtraHashFiles(root string, paths []string) ([]string, error) {
	seen := make(map[string]struct{})
	excludedDirs := map[string]struct{}{
		"node_modules": {},
		"build":        {},
		"dist":         {},
		"coverage":     {},
		".svelte-kit":  {},
		"$houdini":     {},
		".astro":       {},
	}
	for _, raw := range paths {
		rel := filepath.Clean(strings.TrimSpace(raw))
		if rel == "." || rel == "" {
			return nil, fmt.Errorf("extra_hash_paths contains empty path")
		}
		if filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("extra_hash_paths entry %q must stay under the repository root", raw)
		}

		abs := filepath.Join(root, rel)
		info, err := os.Lstat(abs)
		if err != nil {
			return nil, fmt.Errorf("stat extra_hash_paths entry %q: %w", raw, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("extra_hash_paths entry %q must not be a symlink", raw)
		}
		if !info.IsDir() {
			seen[abs] = struct{}{}
			continue
		}
		if walkErr := filepath.WalkDir(abs, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type()&os.ModeSymlink != 0 {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.IsDir() {
				if path != abs {
					if _, excluded := excludedDirs[entry.Name()]; excluded {
						return filepath.SkipDir
					}
				}
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

// webappConsumesGraphQL reports whether the webapp's package.json depends
// on the GraphQL codegen toolchain.
func webappConsumesGraphQL(contextAbs string) bool {
	b, err := os.ReadFile(filepath.Join(contextAbs, "package.json"))
	if err != nil {
		return false
	}
	var pkg struct {
		Dependencies         map[string]string `json:"dependencies"`
		DevDependencies      map[string]string `json:"devDependencies"`
		OptionalDependencies map[string]string `json:"optionalDependencies"`
	}
	if err := json.Unmarshal(b, &pkg); err != nil {
		return false
	}
	for _, deps := range []map[string]string{pkg.Dependencies, pkg.DevDependencies, pkg.OptionalDependencies} {
		for name := range deps {
			if name == "houdini" || strings.HasPrefix(name, "houdini-") || strings.HasPrefix(name, "@kitql/") || strings.Contains(name, "graphql-codegen") {
				return true
			}
		}
	}
	return false
}

// ReadNodeToolchainVersion returns the contents of .nvmrc, trimmed. If
// .nvmrc isn't present we fall back to an empty string — the hash still
// flips when CI bumps Node, because the workflow salt covers release.yml.
func ReadNodeToolchainVersion(monorepoRoot string) string {
	b, err := os.ReadFile(filepath.Join(monorepoRoot, ".nvmrc"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
