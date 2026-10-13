package provisioner

import (
	"maps"
	"strings"
	"sync"

	"frameworks/cli/internal/configschema"
	"frameworks/cli/pkg/inventory"
)

// FinalServiceEnv returns the environment the role for serviceID writes for
// the process on host: the task env plus the keys the role itself adds, such
// as CLUSTER_ID and NODE_ID for Go services and MESH_PRIVATE_KEY_FILE for
// Privateer, narrowed to what the process reads. Contract checks run on this
// map because a key the role adds is present at startup even though the task
// env lacks it.
func FinalServiceEnv(serviceID string, host inventory.Host, config ServiceConfig) map[string]string {
	switch {
	case serviceID == "privateer":
		return privateerEnv(host, config)
	case isGenericServiceRole(serviceID):
		return scopeProcessEnv(serviceID, buildServiceEnvMap(config))
	default:
		out := make(map[string]string, len(config.EnvVars))
		maps.Copy(out, config.EnvVars)
		return out
	}
}

// Every task env starts from the manifest's whole shared and cluster env, which
// holds every platform credential (DNS provider tokens, LLM and payment keys,
// registry tokens, database owner passwords). Role builders read inputs from
// that full env; scopeProcessEnv is applied last, to the env written on the
// host, so a process holds only the settings it reads.
//
// processWideEnvKeys are read by every FrameWorks Go service outside its typed
// configuration: pkg/config reads BUILD_ENV and LOG_LEVEL, gin reads GIN_MODE,
// and the role contract adds CLUSTER_ID and NODE_ID.
var processWideEnvKeys = []string{"BUILD_ENV", "LOG_LEVEL", "GIN_MODE", "CLUSTER_ID", "NODE_ID"}

// processEnvScope is the allow-list for one service that is not described by
// the typed configuration schema.
type processEnvScope struct {
	keys     []string
	prefixes []string
}

// livepeerProcessEnv is what the FrameWorks go-livepeer fork reads: every flag
// as LP_<FLAG> (ff.WithEnvVarPrefix("LP")), its FRAMEWORKS_* telemetry, health
// and selection settings, its LIVE_AI_* settings, and the listed process keys.
// The lowercase flag inputs (eth_url, network, ...) become argv or LP_* values
// and are not read from the environment.
var livepeerProcessEnv = processEnvScope{
	keys:     []string{"HOME", "GEOIP_MMDB_PATH", "GRPC_TLS_CA_PATH", "NV_DEVICE", "FAL_API_KEY"},
	prefixes: []string{"LP_", "FRAMEWORKS_", "LIVE_AI_"},
}

// interfaceProcessEnv covers Chartroom (SvelteKit adapter-node), Foredeck and
// Logbook (static servers): the listener and proxy-header settings, the
// public runtime-config basemap keys, and $env/dynamic/public.
var interfaceProcessEnv = processEnvScope{
	keys: []string{
		"PORT", "HOST", "NODE_ENV", "ORIGIN", "SOCKET_PATH", "PROTOCOL_HEADER", "HOST_HEADER", "PORT_HEADER",
		"ADDRESS_HEADER", "XFF_DEPTH", "BODY_SIZE_LIMIT", "SHUTDOWN_TIMEOUT", "IDLE_TIMEOUT",
		"BASEMAP_PROVIDER", "BASEMAP_STYLE", "CARTO_BASEMAP_BROWSER_KEY",
	},
	prefixes: []string{"PUBLIC_"},
}

var unschematizedProcessEnv = map[string]processEnvScope{
	// The gateway also authenticates to Decklog with the internal service
	// token; the signer talks to no FrameWorks service.
	"livepeer-gateway": {keys: append([]string{"SERVICE_TOKEN"}, livepeerProcessEnv.keys...), prefixes: livepeerProcessEnv.prefixes},
	"livepeer-signer":  livepeerProcessEnv,
	"metabase":         {prefixes: []string{"MB_", "JAVA_"}},
	"grafana":          {prefixes: []string{"GF_"}},
	"chartroom":        interfaceProcessEnv,
	"foredeck":         interfaceProcessEnv,
	"logbook":          interfaceProcessEnv,
}

var (
	schemaEnvKeysOnce sync.Once
	schemaEnvKeys     map[string]map[string]struct{}
	schemaEnvKeysErr  error
)

// serviceSchemaEnvKeys returns every variable of every command of a service
// (server, bootstrap and data-migration variants share the env file), or nil
// when the schema does not describe the service.
func serviceSchemaEnvKeys(serviceName string) (map[string]struct{}, error) {
	schemaEnvKeysOnce.Do(func() {
		schema, err := configschema.Load()
		if err != nil {
			schemaEnvKeysErr = err
			return
		}
		schemaEnvKeys = map[string]map[string]struct{}{}
		for _, section := range schema.Services {
			keys := schemaEnvKeys[section.Service]
			if keys == nil {
				keys = map[string]struct{}{}
				schemaEnvKeys[section.Service] = keys
			}
			for _, variable := range section.Variables {
				keys[variable.Key] = struct{}{}
			}
		}
	})
	if schemaEnvKeysErr != nil {
		return nil, schemaEnvKeysErr
	}
	return schemaEnvKeys[serviceName], nil
}

// scopeProcessEnv narrows a rendered env to the keys serviceName's process
// reads. A service with no known contract keeps the full env.
func scopeProcessEnv(serviceName string, env map[string]string) map[string]string {
	allowed := func(string) bool { return true }
	if scope, ok := unschematizedProcessEnv[serviceName]; ok {
		allowed = func(key string) bool {
			for _, prefix := range scope.prefixes {
				if strings.HasPrefix(key, prefix) {
					return true
				}
			}
			for _, k := range scope.keys {
				if k == key {
					return true
				}
			}
			for _, k := range processWideEnvKeys {
				if k == key {
					return true
				}
			}
			return false
		}
	} else if keys, err := serviceSchemaEnvKeys(serviceName); err == nil && keys != nil {
		allowed = func(key string) bool {
			if _, ok := keys[key]; ok {
				return true
			}
			for _, k := range processWideEnvKeys {
				if k == key {
					return true
				}
			}
			return false
		}
	}
	out := make(map[string]string, len(env))
	for key, value := range env {
		if allowed(key) {
			out[key] = value
		}
	}
	return out
}
