package provisioner

import (
	"fmt"

	"frameworks/cli/pkg/gitops"
)

func fetchGitopsManifest(channel, version string, metadata map[string]interface{}) (*gitops.Manifest, error) {
	manifest, err := gitops.FetchFromRepositories(gitops.FetchOptions{}, gitopsRepositoriesFromMetadata(metadata), channel, version)
	if err != nil {
		return nil, fmt.Errorf("fetch gitops manifest: %w", err)
	}
	return manifest, nil
}

func gitopsRepositoriesFromMetadata(metadata map[string]interface{}) []string {
	if len(metadata) == 0 {
		return nil
	}

	var repos []string
	switch v := metadata["gitops_repositories"].(type) {
	case []string:
		repos = append(repos, v...)
	case []interface{}:
		for _, item := range v {
			if repo, ok := item.(string); ok && repo != "" {
				repos = append(repos, repo)
			}
		}
	case string:
		if v != "" {
			repos = append(repos, v)
		}
	}

	if repo, ok := metadata["gitops_repository"].(string); ok && repo != "" {
		repos = append(repos, repo)
	}

	return repos
}
