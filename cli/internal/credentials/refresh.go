package credentials

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

var sessionMu sync.Mutex

// Unverified claims are a scheduling hint only; Bridge authenticates the token.
func sessionNeedsRefresh(token string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	claims, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var payload struct {
		Exp int64 `json:"exp"`
	}
	return json.Unmarshal(claims, &payload) == nil && payload.Exp > 0 && time.Until(time.Unix(payload.Exp, 0)) <= time.Minute
}

// RefreshUserAuth retries a rejected stored credential only against its login Bridge.
// Environment credentials belong to the invoking process (including the tray).
func RefreshUserAuth(ctx context.Context, bridgeURL, rejectedToken string) (string, error) {
	if os.Getenv(EnvUserToken) != "" || rejectedToken == "" {
		return rejectedToken, nil
	}
	return renewSessionContext(ctx, DefaultStore(), rejectedToken, strings.TrimRight(bridgeURL, "/"), true)
}

func resolveSession(store Store) (string, error) {
	var result string
	err := withSessionLock(context.Background(), store, func(ctx context.Context) error {
		token, err := store.Get(AccountUserSession)
		if err != nil {
			return err
		}
		result, err = renewLocked(ctx, store, token, "", false)
		return err
	})
	return result, err
}

func renewSessionContext(ctx context.Context, store Store, token, bridgeURL string, force bool) (string, error) {
	var result string
	err := withSessionLock(ctx, store, func(ctx context.Context) error {
		var err error
		result, err = renewLocked(ctx, store, token, bridgeURL, force)
		return err
	})
	return result, err
}

func withSessionLock(ctx context.Context, store Store, fn func(context.Context) error) error {
	sessionMu.Lock()
	defer sessionMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if locker, ok := store.(interface {
		lockSession(context.Context) (func(), error)
	}); ok {
		unlock, err := locker.lockSession(ctx)
		if err != nil {
			return err
		}
		defer unlock()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn(ctx)
}

func renewLocked(ctx context.Context, store Store, token, bridgeURL string, force bool) (string, error) {
	current, err := store.Get(AccountUserSession)
	if err != nil {
		return "", err
	}
	if current != token {
		if force {
			return token, nil
		}
		return current, nil
	}
	if !force && !sessionNeedsRefresh(current) {
		return current, nil
	}
	origin, err := store.Get(AccountUserBridge)
	if err != nil {
		return "", err
	}
	if bridgeURL != "" && strings.TrimRight(origin, "/") != bridgeURL {
		return token, nil
	}
	refresh, err := store.Get(AccountUserRefresh)
	if err != nil {
		return "", err
	}
	if refresh == "" {
		return token, nil
	}
	if origin == "" {
		return "", fmt.Errorf("CLI session has no login Bridge; run 'frameworks login'")
	}
	endpoint, err := url.Parse(origin)
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "https" && endpoint.Scheme != "http") || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return "", fmt.Errorf("invalid login Bridge; run 'frameworks login'")
	}
	body, err := json.Marshal(map[string]string{"grant_type": "refresh_token", "refresh_token": refresh})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(origin, "/")+"/auth/oauth/token", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "FrameWorks-CLI/1.0")
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("refresh CLI session: %w", err)
	}
	legacy := false
	if resp.StatusCode == http.StatusBadRequest {
		var failure struct {
			Error string `json:"error"`
		}
		if decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&failure); decodeErr == nil && strings.HasPrefix(failure.Error, "invalid request") {
			_ = resp.Body.Close()
			legacyReq, requestErr := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(origin, "/")+"/auth/refresh", nil)
			if requestErr != nil {
				return "", requestErr
			}
			legacyReq.AddCookie(&http.Cookie{Name: "refresh_token", Value: refresh})
			legacyReq.Header.Set("Accept", "application/json")
			legacyReq.Header.Set("User-Agent", "FrameWorks-CLI/1.0")
			resp, err = client.Do(legacyReq)
			if err != nil {
				return "", fmt.Errorf("legacy CLI refresh: %w", err)
			}
			legacy = true
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized {
		return "", fmt.Errorf("CLI session refresh rejected; run 'frameworks login'")
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("refresh CLI session: HTTP %d; credentials retained, retry later", resp.StatusCode)
	}
	var pair struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
	}
	if legacy {
		for _, cookie := range resp.Cookies() {
			switch cookie.Name {
			case "access_token":
				pair.Access = cookie.Value
			case "refresh_token":
				pair.Refresh = cookie.Value
			}
		}
	} else {
		if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&pair); err != nil {
			return "", fmt.Errorf("decode CLI refresh: %w", err)
		}
	}
	if strings.TrimSpace(pair.Access) == "" || strings.TrimSpace(pair.Refresh) == "" {
		return "", fmt.Errorf("CLI refresh returned an incomplete token pair")
	}
	// Save the rotated refresh first so a failed access-token write is recoverable.
	if err = store.Set(AccountUserRefresh, pair.Refresh); err != nil {
		return "", fmt.Errorf("save CLI refresh: %w", err)
	}
	if err = store.Set(AccountUserSession, pair.Access); err != nil {
		return "", fmt.Errorf("save CLI session: %w", err)
	}
	return pair.Access, nil
}

// SaveSession binds the CLI credential to its issuing Bridge, independently of the tray.
func SaveSession(ctx context.Context, store Store, bridgeURL, access, refresh string) error {
	return withSessionLock(ctx, store, func(_ context.Context) error {
		// A replacement login must never pair its access token with an old refresh.
		if err := store.Delete(AccountUserRefresh); err != nil {
			return err
		}
		if err := store.Delete(AccountUserSession); err != nil {
			return err
		}
		if err := store.Set(AccountUserBridge, strings.TrimRight(bridgeURL, "/")); err != nil {
			return err
		}
		if refresh != "" {
			if err := store.Set(AccountUserRefresh, refresh); err != nil {
				return err
			}
		}
		return store.Set(AccountUserSession, access)
	})
}

// ClearSession shares the renewal lock so a refresh cannot restore a logged-out CLI.
func ClearSession(ctx context.Context, store Store) error {
	return withSessionLock(ctx, store, func(_ context.Context) error {
		return clearSessionLocked(store)
	})
}
