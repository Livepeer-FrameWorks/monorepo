package provisioner

import (
	"maps"

	"frameworks/cli/pkg/inventory"
)

// FinalServiceEnv returns the environment the role for serviceID writes for
// the process on host: the task env plus the keys the role itself adds, such
// as CLUSTER_ID and NODE_ID for Go services and MESH_PRIVATE_KEY_FILE for
// Privateer. Contract checks run on this map because a key the role adds is
// present at startup even though the task env lacks it.
func FinalServiceEnv(serviceID string, host inventory.Host, config ServiceConfig) map[string]string {
	switch {
	case serviceID == "privateer":
		return privateerEnv(host, config)
	case isGenericServiceRole(serviceID):
		return buildServiceEnvMap(config)
	default:
		out := make(map[string]string, len(config.EnvVars))
		maps.Copy(out, config.EnvVars)
		return out
	}
}
