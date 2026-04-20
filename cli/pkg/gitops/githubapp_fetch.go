package gitops

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"frameworks/cli/pkg/githubapp"
	"frameworks/cli/pkg/inventory"
	fwsops "frameworks/cli/pkg/sops"
)

func NewGithubAppFetcher() inventory.GithubFetcher {
	return &githubAppFetcher{}
}

type githubAppFetcher struct{}

func (githubAppFetcher) Fetch(ctx context.Context, in inventory.GithubFetchInput) (inventory.GithubFetchResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	stdout := in.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}

	fmt.Fprintf(stdout, "Fetching manifest from %s (ref %s)...\n", in.Repo, in.Ref)

	client, err := githubapp.NewClient(ctx, githubapp.Config{
		AppID:          in.AppID,
		InstallationID: in.InstallationID,
		PrivateKeyPath: in.PrivateKeyPath,
		Repo:           in.Repo,
		Ref:            in.Ref,
	})
	if err != nil {
		return inventory.GithubFetchResult{}, fmt.Errorf("GitHub App authentication failed: %w", err)
	}

	manifestData, err := client.Fetch(ctx, in.ManifestPath)
	if err != nil {
		return inventory.GithubFetchResult{}, fmt.Errorf("fetch %s from %s: %w", in.ManifestPath, in.Repo, err)
	}

	parsed, err := inventory.ParseManifest(manifestData)
	if err != nil {
		return inventory.GithubFetchResult{}, fmt.Errorf("parse manifest from %s: %w", in.Repo, err)
	}

	tmpDir, err := os.MkdirTemp("", "frameworks-manifest-*")
	if err != nil {
		return inventory.GithubFetchResult{}, fmt.Errorf("create temp dir: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(tmpDir) }

	manifestDir := filepath.Dir(in.ManifestPath)
	filesToFetch := collectReferencedFiles(parsed, manifestDir)

	for _, name := range filesToFetch {
		data, err := client.Fetch(ctx, name)
		if err != nil {
			fmt.Fprintf(stdout, "Warning: could not fetch %s: %v\n", name, err)
			continue
		}
		if fwsops.IsEncrypted(data) {
			plain, err := fwsops.DecryptData(data, fwsops.FormatFromPath(name), in.AgeKeyPath)
			if err != nil {
				cleanup()
				return inventory.GithubFetchResult{}, fmt.Errorf("decrypt %s: %w", name, err)
			}
			data = plain
			fmt.Fprintf(stdout, "Decrypted %s (SOPS/age)\n", name)
		}
		localPath := filepath.Join(tmpDir, name)
		if err := os.MkdirAll(filepath.Dir(localPath), 0o700); err != nil {
			fmt.Fprintf(stdout, "Warning: could not create dir for %s: %v\n", name, err)
			continue
		}
		if err := os.WriteFile(localPath, data, 0o600); err != nil {
			fmt.Fprintf(stdout, "Warning: could not write %s: %v\n", name, err)
			continue
		}
		fmt.Fprintf(stdout, "Fetched %s\n", name)
	}

	// Write the manifest at its canonical repo-relative location inside
	// the tempdir so manifest-relative paths (env_files with ../../)
	// resolve against the fetched files.
	tmpManifest := filepath.Join(tmpDir, in.ManifestPath)
	if err := os.MkdirAll(filepath.Dir(tmpManifest), 0o700); err != nil {
		cleanup()
		return inventory.GithubFetchResult{}, fmt.Errorf("create manifest dir: %w", err)
	}
	if err := os.WriteFile(tmpManifest, manifestData, 0o600); err != nil {
		cleanup()
		return inventory.GithubFetchResult{}, fmt.Errorf("write temp manifest: %w", err)
	}

	return inventory.GithubFetchResult{
		Path:    tmpManifest,
		TmpDir:  tmpDir,
		Cleanup: cleanup,
	}, nil
}

func collectReferencedFiles(m *inventory.Manifest, manifestDir string) []string {
	var paths []string
	add := func(relPath string) {
		if relPath == "" {
			return
		}
		repoPath, err := resolveManifestToRepoPath(manifestDir, relPath)
		if err != nil {
			return
		}
		paths = append(paths, repoPath)
	}
	for _, envFile := range m.SharedEnvFiles() {
		add(envFile)
	}
	add(m.HostsFile)
	for _, svc := range m.Services {
		add(svc.EnvFile)
	}
	for _, iface := range m.Interfaces {
		add(iface.EnvFile)
	}

	seen := map[string]struct{}{}
	out := paths[:0]
	for _, p := range paths {
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}

// resolveManifestToRepoPath rejects absolute paths and paths that escape
// the repo root — GitHub API fetches must stay inside the checkout.
func resolveManifestToRepoPath(manifestDir, relPath string) (string, error) {
	if filepath.IsAbs(relPath) {
		return "", fmt.Errorf("absolute path %q is not valid in a repository manifest", relPath)
	}
	resolved := filepath.Clean(filepath.Join(manifestDir, relPath))
	if strings.HasPrefix(resolved, "..") {
		return "", fmt.Errorf("path %q resolves outside repository root (resolved to %q)", relPath, resolved)
	}
	return resolved, nil
}
