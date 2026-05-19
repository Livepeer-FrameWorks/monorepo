package cmd

import (
	"fmt"
	"strings"

	"frameworks/cli/pkg/inventory"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"
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
	if def, ok := resolveServiceDefinition(id, cfg); ok {
		return def.DefaultPort, nil
	}
	return 0, fmt.Errorf("no default port for service id: %s", id)
}

func resolveServiceDefinition(id string, cfg inventory.ServiceConfig) (servicedefs.Service, bool) {
	if def, ok := servicedefs.Lookup(id); ok {
		return def, true
	}
	if deploy := strings.TrimSpace(cfg.Deploy); deploy != "" {
		return servicedefs.Lookup(deploy)
	}
	return servicedefs.Service{}, false
}
