package credentials

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/cli/internal/config"
)

func sessionJWT(exp time.Time) string {
	return "eyJhbGciOiJIUzI1NiJ9." + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, exp.Unix()))) + ".signature"
}

func TestResolveUserAuthRenewsExpiredSession(t *testing.T) {
	old := sessionJWT(time.Now().Add(-time.Minute))
	want := sessionJWT(time.Now().Add(15 * time.Minute))
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/auth/oauth/token" || r.Method != http.MethodPost {
			t.Errorf("unexpected refresh request: %s %s", r.Method, r.URL)
		}
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Error("refresh must use only its JSON credential")
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["grant_type"] != "refresh_token" || body["refresh_token"] != "refresh" {
			t.Errorf("incorrect refresh body: %v", body)
		}
		fmt.Fprintf(w, `{"access_token":%q,"refresh_token":"rotated"}`, want)
	}))
	defer server.Close()
	store := newInMemoryStore()
	_ = store.Set(AccountUserSession, old)
	_ = store.Set(AccountUserRefresh, "refresh")
	_ = store.Set("cli_bridge_url", server.URL)
	got, err := ResolveUserAuth(config.MapEnv{}, store)
	if err != nil || got != want || calls != 1 {
		t.Fatalf("expired CLI session was not renewed: calls=%d token=%q err=%v", calls, got, err)
	}
	if got, _ := store.Get(AccountUserRefresh); got != "rotated" {
		t.Fatalf("rotated refresh was not saved: %q", got)
	}
}

func TestRefreshNeverUsesAnotherBridgeOrEnvironmentCredential(t *testing.T) {
	store := newInMemoryStore()
	token := sessionJWT(time.Now().Add(-time.Minute))
	_ = store.Set(AccountUserSession, token)
	_ = store.Set(AccountUserRefresh, "private-refresh")
	_ = store.Set(AccountUserBridge, "https://original.example")
	got, err := renewSessionContext(context.Background(), store, token, "https://another.example", true)
	if err != nil || got != token {
		t.Fatalf("different Bridge: got=%q err=%v", got, err)
	}
	got, err = ResolveUserAuth(config.MapEnv{EnvUserToken: "process-token"}, store)
	if err != nil || got != "process-token" {
		t.Fatalf("environment token: got=%q err=%v", got, err)
	}
	t.Setenv(EnvUserToken, "process-token")
	got, err = RefreshUserAuth(context.Background(), "https://original.example", token)
	if err != nil || got != token {
		t.Fatalf("environment override must prevent stored refresh: got=%q err=%v", got, err)
	}
}

func TestRefreshDoesNotFollowRedirects(t *testing.T) {
	var leaked atomic.Bool
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { leaked.Store(true) }))
	defer destination.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	store := newInMemoryStore()
	_ = store.Set(AccountUserSession, sessionJWT(time.Now().Add(-time.Minute)))
	_ = store.Set(AccountUserRefresh, "private-refresh")
	_ = store.Set(AccountUserBridge, origin.URL)
	if _, err := ResolveUserAuth(config.MapEnv{}, store); err == nil {
		t.Fatal("redirect must fail")
	}
	if leaked.Load() {
		t.Fatal("refresh credential followed a redirect")
	}
}

func TestConcurrentCLICredentialsRotateOnceAndPreserveTray(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	var calls atomic.Int32
	want := sessionJWT(time.Now().Add(15 * time.Minute))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		fmt.Fprintf(w, `{"access_token":%q,"refresh_token":"rotated"}`, want)
	}))
	defer server.Close()
	store := newFileStore()
	_ = store.Set("user_session", "tray-access")
	_ = store.Set("refresh_token", "tray-refresh")
	if err := SaveSession(context.Background(), store, server.URL, sessionJWT(time.Now().Add(-time.Minute)), "refresh"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			got, err := ResolveUserAuth(config.MapEnv{}, newFileStore())
			if err != nil || got != want {
				t.Errorf("resolve token=%q err=%v", got, err)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("concurrent renewals=%d, want one", calls.Load())
	}
	if err := ClearSession(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.Get(AccountUserSession); got != "" {
		t.Fatal("CLI logout retained access")
	}
	if got, _ := store.Get(AccountUserRefresh); got != "" {
		t.Fatal("CLI logout retained refresh")
	}
	if got, _ := store.Get("user_session"); got != "tray-access" {
		t.Fatal("CLI changed tray access")
	}
	if got, _ := store.Get("refresh_token"); got != "tray-refresh" {
		t.Fatal("CLI changed tray refresh")
	}
}

func TestResolveUserAuthRefreshFailureKeepsCredentials(t *testing.T) {
	for _, code := range []int{400, 503} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(code)
				fmt.Fprint(w, `{"error":"do-not-echo-refresh-secret"}`)
			}))
			defer server.Close()
			store := newInMemoryStore()
			old := sessionJWT(time.Now().Add(-time.Minute))
			_ = store.Set(AccountUserSession, old)
			_ = store.Set(AccountUserRefresh, "refresh")
			_ = store.Set("cli_bridge_url", server.URL)
			_, err := ResolveUserAuth(config.MapEnv{}, store)
			if err == nil || strings.Contains(err.Error(), "do-not-echo") {
				t.Fatalf("expected safe refresh error, got %v", err)
			}
			if got, _ := store.Get(AccountUserRefresh); got != "refresh" {
				t.Fatal("failed refresh erased credential")
			}
		})
	}
}

func TestSessionFileLockHonorsCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.lock")
	unlock, err := lockSessionFile(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = lockSessionFile(ctx, path)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("contended lock ignored cancellation: %v", err)
	}
	unlock()
	unlock, err = lockSessionFile(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
}

func TestSessionRefreshScheduling(t *testing.T) {
	for _, tc := range []struct {
		name, token string
		want        bool
	}{
		{"API token", "fw_api_token", false},
		{"unparseable", "x.!invalid.y", false},
		{"missing expiry", "x.e30.y", false},
		{"valid", sessionJWT(time.Now().Add(10 * time.Minute)), false},
		{"near expiry", sessionJWT(time.Now().Add(30 * time.Second)), true},
		{"expired", sessionJWT(time.Now().Add(-time.Minute)), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sessionNeedsRefresh(tc.token); got != tc.want {
				t.Fatalf("needs refresh=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestCLIRenewalSupportsOldBridgeCookies(t *testing.T) {
	want := sessionJWT(time.Now().Add(15 * time.Minute))
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/auth/oauth/token" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":"invalid request: Key: 'Code' Error:Field validation for 'Code' failed on the 'required' tag"}`)
			return
		}
		cookie, err := r.Cookie("refresh_token")
		if err != nil || cookie.Value != "native-refresh" {
			t.Errorf("legacy refresh cookie=%v err=%v", cookie, err)
		}
		http.SetCookie(w, &http.Cookie{Name: "access_token", Value: want, HttpOnly: true})
		http.SetCookie(w, &http.Cookie{Name: "refresh_token", Value: "rotated-legacy", HttpOnly: true})
		fmt.Fprint(w, `{"user":{"id":"user"}}`)
	}))
	defer server.Close()
	store := newInMemoryStore()
	if err := SaveSession(context.Background(), store, server.URL, sessionJWT(time.Now().Add(-time.Minute)), "native-refresh"); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveUserAuth(config.MapEnv{}, store)
	if err != nil || got != want {
		t.Fatalf("old Bridge renewal failed: token=%q err=%v", got, err)
	}
	if len(paths) != 2 || paths[1] != "/auth/refresh" {
		t.Fatalf("unexpected fallback paths: %v", paths)
	}
	if got, _ := store.Get(AccountUserRefresh); got != "rotated-legacy" {
		t.Fatalf("rotated legacy token not saved: %q", got)
	}
}
