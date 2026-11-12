package credentials

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"frameworks/cli/internal/config"
)

func namedAuthJWT(id string, expiry time.Time) string {
	return "eyJhbGciOiJIUzI1NiJ9." + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"sid":%q,"exp":%d}`, id, expiry.Unix()))) + ".signature"
}

func TestResolveSessionAuthOwnershipAndLegacyUpgrade(t *testing.T) {
	const id = "50000000-0000-4000-8000-0000000000ab"
	fresh := namedAuthJWT(id, time.Now().Add(time.Hour))
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/auth/oauth/token" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		fmt.Fprintf(w, `{"access_token":%q,"refresh_token":"rotated"}`, fresh)
	}))
	defer server.Close()
	store := newInMemoryStore()
	if err := SaveSession(context.Background(), store, server.URL, sessionJWT(time.Now().Add(time.Hour)), "refresh"); err != nil {
		t.Fatal(err)
	}
	auth, err := ResolveSessionAuth(context.Background(), config.MapEnv{}, store)
	if err != nil || auth.Token != fresh || auth.BridgeURL != server.URL || auth.Environment || calls != 1 {
		t.Fatalf("legacy upgrade: %+v calls=%d err=%v", auth, calls, err)
	}
	borrowed, err := ResolveSessionAuth(context.Background(), config.MapEnv{EnvUserToken: "borrowed"}, nil)
	if err != nil || borrowed.Token != "borrowed" || !borrowed.Environment || borrowed.BridgeURL != "" || calls != 1 {
		t.Fatalf("borrowed credential: %+v calls=%d err=%v", borrowed, calls, err)
	}
	if _, err = ResolveSessionAuth(context.Background(), config.MapEnv{EnvUserToken: " \n"}, nil); err == nil {
		t.Fatal("empty override must not select stored credentials")
	}
}

func TestClearRevokedSessionPreservesCredentialOwner(t *testing.T) {
	const id = "50000000-0000-4000-8000-0000000000ab"
	for _, tc := range []struct {
		name, tokenID, origin, target string
		want                          bool
	}{
		{"own session", id, "https://original.example", id, true},
		{"equivalent UUID", id, "https://original.example", strings.ToUpper(id), true},
		{"replacement login", "50000000-0000-4000-8000-0000000000ac", "https://original.example", id, false},
		{"other Bridge", id, "https://replacement.example", id, false},
		{"missing session", "", "https://original.example", id, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newInMemoryStore()
			token := namedAuthJWT(tc.tokenID, time.Now().Add(time.Hour))
			if err := SaveSession(context.Background(), store, tc.origin, token, "private-refresh"); err != nil {
				t.Fatal(err)
			}
			_ = store.Set("user_session", "tray-token")
			cleared, err := ClearRevokedSession(context.Background(), store, "https://original.example/", tc.target)
			if err != nil || cleared != tc.want {
				t.Fatalf("cleared=%t want=%t err=%v", cleared, tc.want, err)
			}
			got, _ := store.Get(AccountUserSession)
			if tc.want && got != "" || !tc.want && got != token {
				t.Fatal("incorrect credential cleanup")
			}
			if tray, _ := store.Get("user_session"); tray != "tray-token" {
				t.Fatal("cleanup changed tray credentials")
			}
		})
	}
}

func TestRefreshStoredUserAuthDoesNotSubstituteReplacement(t *testing.T) {
	store := newInMemoryStore()
	original := namedAuthJWT("old", time.Now().Add(time.Hour))
	replacement := namedAuthJWT("new", time.Now().Add(time.Hour))
	if err := SaveSession(context.Background(), store, "https://original.example", replacement, "replacement-refresh"); err != nil {
		t.Fatal(err)
	}
	got, err := RefreshStoredUserAuth(context.Background(), store, "https://original.example", original)
	if err != nil || got != original {
		t.Fatalf("in-flight request received a replacement identity: err=%v", err)
	}
}
