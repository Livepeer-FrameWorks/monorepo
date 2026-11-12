package credentials

import (
	"context"
	"fmt"
	"strings"

	"frameworks/cli/internal/config"
	"github.com/google/uuid"
)

// SessionAuth identifies one invocation's credential owner. Secret fields must
// never enter command output; environment credentials have no stored origin.
type SessionAuth struct {
	Token       string `json:"-"`
	BridgeURL   string `json:"-"`
	Environment bool   `json:"environment"`
}

// ResolveSessionAuth reads a consistent stored token/origin pair and upgrades
// legacy sessions when refresh is available. Borrowed credentials bypass storage.
func ResolveSessionAuth(ctx context.Context, env config.Env, store Store) (SessionAuth, error) {
	if env == nil {
		env = config.OSEnv{}
	}
	if token := env.Get(EnvUserToken); token != "" {
		if strings.TrimSpace(token) == "" {
			return SessionAuth{}, fmt.Errorf("FW_USER_TOKEN contains no credential")
		}
		return SessionAuth{Token: strings.TrimSpace(token), Environment: true}, nil
	}
	if store == nil {
		return SessionAuth{}, fmt.Errorf("CLI credential store is unavailable")
	}
	var result SessionAuth
	err := withSessionLock(ctx, store, func(lockCtx context.Context) error {
		token, readErr := store.Get(AccountUserSession)
		if readErr != nil {
			return readErr
		}
		if token == "" {
			return fmt.Errorf("not signed in; run 'frameworks login'")
		}
		origin, readErr := store.Get(AccountUserBridge)
		if readErr != nil {
			return readErr
		}
		if origin == "" {
			return fmt.Errorf("CLI session has no login Bridge; run 'frameworks login'")
		}
		token, readErr = renewLocked(lockCtx, store, token, strings.TrimRight(origin, "/"), !namedSession(token))
		if readErr != nil {
			return readErr
		}
		result = SessionAuth{Token: token, BridgeURL: strings.TrimRight(origin, "/")}
		return nil
	})
	return result, err
}

// RefreshStoredUserAuth renews only the supplied owned credential at its login
// Bridge. A replacement login cannot be substituted into an in-flight request.
func RefreshStoredUserAuth(ctx context.Context, store Store, bridgeURL, rejectedToken string) (string, error) {
	if rejectedToken == "" {
		return rejectedToken, nil
	}
	if store == nil || bridgeURL == "" {
		return "", fmt.Errorf("refresh requires an owned credential and login Bridge")
	}
	return renewSessionContext(ctx, store, rejectedToken, strings.TrimRight(bridgeURL, "/"), true)
}

// ClearRevokedSession leaves a concurrent replacement login untouched. A refresh
// of the revoked session keeps its sid and must still be removed.
func ClearRevokedSession(ctx context.Context, store Store, bridgeURL, sessionID string) (bool, error) {
	if sessionID == "" {
		return false, nil
	}
	if store == nil || bridgeURL == "" {
		return false, fmt.Errorf("credential cleanup requires an owned login Bridge")
	}
	targetID, err := uuid.Parse(sessionID)
	if err != nil {
		return false, fmt.Errorf("invalid revoked session ID")
	}
	cleared := false
	err = withSessionLock(ctx, store, func(context.Context) error {
		origin, readErr := store.Get(AccountUserBridge)
		if readErr != nil {
			return readErr
		}
		if strings.TrimRight(origin, "/") != strings.TrimRight(bridgeURL, "/") {
			return nil
		}
		token, readErr := store.Get(AccountUserSession)
		if readErr != nil {
			return readErr
		}
		if currentID, idErr := uuid.Parse(userSessionID(token)); idErr == nil && currentID == targetID {
			if clearErr := clearSessionLocked(store); clearErr != nil {
				return clearErr
			}
			cleared = true
		}
		return nil
	})
	return cleared, err
}
