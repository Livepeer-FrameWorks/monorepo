package credentials

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func logoutJWT() string {
	return "header." + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"sid":"cli-session","exp":%d}`, time.Now().Add(time.Hour).Unix()))) + ".signature"
}

func TestLogoutRevokesOwnedCredentialDespiteEnvironmentOverride(t *testing.T) {
	t.Setenv(EnvUserToken, "borrowed-tray-token")
	owned := logoutJWT()
	called := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		if r.Method != http.MethodPost || r.URL.Path != "/auth/logout" || r.Header.Get("Authorization") != "Bearer "+owned || r.Header.Get("Cookie") != "" {
			t.Errorf("logout leaked or selected wrong credential: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	store := newInMemoryStore()
	if err := SaveSession(context.Background(), store, server.URL, owned, "owned-refresh"); err != nil {
		t.Fatal(err)
	}
	revoked, err := LogoutSession(context.Background(), store)
	if err != nil || !revoked || called != 1 || len(store.m) != 0 {
		t.Fatalf("logout revoked=%v calls=%d remaining=%d err=%v", revoked, called, len(store.m), err)
	}
}

func TestLogoutNeverInvokesOldGlobalLogout(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		t.Errorf("legacy local logout made request to %s", r.URL.Path)
	}))
	defer server.Close()
	store := newInMemoryStore()
	if err := SaveSession(context.Background(), store, server.URL, sessionJWT(time.Now().Add(time.Hour)), ""); err != nil {
		t.Fatal(err)
	}
	revoked, err := LogoutSession(context.Background(), store)
	if err != nil || revoked || calls != 0 || len(store.m) != 0 {
		t.Fatalf("legacy logout revoked=%v calls=%d err=%v", revoked, calls, err)
	}
}

func TestLogoutFailureRetainsCredentialAndDoesNotFollowRedirect(t *testing.T) {
	for _, code := range []int{http.StatusServiceUnavailable, http.StatusTemporaryRedirect} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			targetCalls := 0
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls++ }))
			defer target.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", target.URL)
				w.WriteHeader(code)
			}))
			defer server.Close()
			store := newInMemoryStore()
			if err := SaveSession(context.Background(), store, server.URL, logoutJWT(), "refresh"); err != nil {
				t.Fatal(err)
			}
			if _, err := LogoutSession(context.Background(), store); err == nil {
				t.Fatal("unconfirmed revocation accepted")
			}
			if targetCalls != 0 || len(store.m) != 3 {
				t.Fatalf("failure leaked token or erased retry credentials: target calls=%d remaining=%d", targetCalls, len(store.m))
			}
		})
	}
}
