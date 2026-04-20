package credentials

import (
	"frameworks/cli/internal/config"
)

// ResolveUserAuth returns the user session JWT (operator identity).
// SERVICE_TOKEN is platform configuration and lives in gitops/manifest
// env_files; it is NEVER read from the credential store or env vars.
// Platform flows must source it via the platformauth package.
func ResolveUserAuth(env config.Env, store Store) (string, error) {
	if env == nil {
		env = config.OSEnv{}
	}
	getter := configEnvAdapter{env: env}
	return resolveWith(store, getter, AccountUserSession)
}

// configEnvAdapter bridges config.Env (used across the resolver layer)
// to the local envGetter the credential store expects. Keeps test
// fixtures injectable instead of always reading os.Getenv.
type configEnvAdapter struct{ env config.Env }

func (a configEnvAdapter) Get(key string) string { return a.env.Get(key) }
