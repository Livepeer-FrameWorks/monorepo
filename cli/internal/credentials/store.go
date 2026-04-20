// Package credentials stores human-facing secrets for the CLI and tray.
//
// On macOS credentials live in the login Keychain under ServiceName; on
// other platforms in $XDG_DATA_HOME/frameworks/credentials with mode
// 0600. FW_USER_TOKEN overrides the store for the user session. The
// platform SERVICE_TOKEN is NOT a human credential — it is gitops/manifest
// configuration and is resolved via the platformauth package, not here.
package credentials

const ServiceName = "com.livepeer.frameworks"

const (
	AccountUserSession = "user_session"
	// AccountUserRefresh is the tray's refresh token. The CLI never
	// reads it, but logout must clear it so a tray refresh cycle can't
	// silently repopulate user_session after the CLI logs out.
	AccountUserRefresh = "refresh_token"
)

const EnvUserToken = "FW_USER_TOKEN"

type Store interface {
	Get(account string) (string, error)
	Set(account, value string) error
	Delete(account string) error
	Name() string
}

func DefaultStore() Store { return defaultStore() }

func Resolve(account string) (string, error) {
	return resolveWith(DefaultStore(), osEnv{}, account)
}

type envGetter interface {
	Get(key string) string
}

type osEnv struct{}

func (osEnv) Get(key string) string { return osGetenv(key) }

func resolveWith(store Store, env envGetter, account string) (string, error) {
	if envKey := envNameFor(account); envKey != "" {
		if v := env.Get(envKey); v != "" {
			return v, nil
		}
	}
	return store.Get(account)
}

func envNameFor(account string) string {
	if account == AccountUserSession {
		return EnvUserToken
	}
	return ""
}
