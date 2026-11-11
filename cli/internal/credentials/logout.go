package credentials

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func namedSession(token string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var claims struct {
		SessionID string `json:"sid"`
	}
	return json.Unmarshal(data, &claims) == nil && claims.SessionID != ""
}

// LogoutSession uses only the owned CLI credential and its original Bridge.
// A session-less credential must never call an older Bridge's global logout.
func LogoutSession(ctx context.Context, store Store) (bool, error) {
	revoked := false
	err := withSessionLock(ctx, store, func(ctx context.Context) error {
		token, err := store.Get(AccountUserSession)
		if err != nil {
			return err
		}
		origin, err := store.Get(AccountUserBridge)
		if err != nil {
			return err
		}
		if token != "" {
			token, err = renewLocked(ctx, store, token, origin, !namedSession(token))
			if err != nil {
				return err
			}
		}
		if namedSession(token) {
			endpoint, parseErr := url.Parse(origin)
			if parseErr != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Scheme != "https" && endpoint.Scheme != "http" {
				return fmt.Errorf("invalid login Bridge; use 'frameworks logout --local' to clear only local credentials")
			}
			req, requestErr := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(origin, "/")+"/auth/logout", nil)
			if requestErr != nil {
				return requestErr
			}
			req.Header.Set("Authorization", "Bearer "+token)
			client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
			response, requestErr := client.Do(req)
			if requestErr != nil {
				return fmt.Errorf("revoke CLI session: %w", requestErr)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusUnauthorized && response.StatusCode/100 != 2 {
				return fmt.Errorf("revoke CLI session: HTTP %d; retry, or use 'frameworks logout --local' to clear only local credentials", response.StatusCode)
			}
			revoked = true
		}
		return clearSessionLocked(store)
	})
	return revoked, err
}

func clearSessionLocked(store Store) error {
	for _, account := range []string{AccountUserRefresh, AccountUserSession, AccountUserBridge} {
		if err := store.Delete(account); err != nil {
			return err
		}
	}
	return nil
}
