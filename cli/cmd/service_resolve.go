package cmd

import (
	"fmt"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/servicedefs"
)

// resolveDeployName returns the deploy slug for a canonical service ID.
func resolveDeployName(id string, cfg inventory.ServiceConfig) (string, error) {
	if deploy, ok := servicedefs.DeployName(id, cfg.Deploy); ok {
		return deploy, nil
	}
	return "", fmt.Errorf("unknown service id: %s", id)
}

// resolvePort returns the configured port or the registry default.
func resolvePort(id string, cfg inventory.ServiceConfig) (int, error) {
	if cfg.Port != 0 {
		return cfg.Port, nil
	}
	if port, ok := servicedefs.DefaultPort(id); ok {
		return port, nil
	}
	return 0, fmt.Errorf("no default port for service id: %s", id)
}
