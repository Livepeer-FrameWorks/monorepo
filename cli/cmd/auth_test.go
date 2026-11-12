package cmd

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	fwcfg "frameworks/cli/internal/config"
	"frameworks/cli/internal/credentials"
)

const commandSessionID = "50000000-0000-4000-8000-0000000000ab"

func commandJWT(id string, expiry time.Time) string {
	claims, _ := json.Marshal(map[string]any{"sid": id, "user_id": "user-1", "tenant_id": "tenant-1", "email": "user@example.com", "exp": expiry.Unix()})
	return "eyJhbGciOiJIUzI1NiJ9." + base64.RawURLEncoding.EncodeToString(claims) + ".signature"
}

func runAuthJSON(t *testing.T, options authCommandOptions, args ...string) (string, error) {
	t.Helper()
	saved := fwcfg.GetRuntimeOverrides()
	fwcfg.SetRuntimeOverrides(fwcfg.RuntimeOverrides{OutputJSON: true})
	t.Cleanup(func() { fwcfg.SetRuntimeOverrides(saved) })
	command := newAuthCmdWithOptions(options)
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetErr(io.Discard)
	command.SetArgs(args)
	err := command.Execute()
	return out.String(), err
}

func authPageResponse(w http.ResponseWriter, current bool) {
	fmt.Fprintf(w, `{"data":{"authSessionsConnection":{"nodes":[{"id":%q,"clientKind":"CLI","clientId":"cli","authMethod":"DEVICE_CODE","label":"My CLI","current":%t,"createdAt":"2026-11-01T00:00:00Z","lastActiveAt":"2026-11-12T00:00:00Z","expiresAt":"2027-05-01T00:00:00Z"}],"totalCount":1,"pageInfo":{"hasNextPage":false}}}}`, commandSessionID, current)
}

func storedCommandOptions(t *testing.T, origin, token string) (authCommandOptions, *memoryCredentialStore) {
	t.Helper()
	store := &memoryCredentialStore{entries: map[string]string{}}
	if err := credentials.SaveSession(context.Background(), store, origin, token, "private-refresh"); err != nil {
		t.Fatal(err)
	}
	return authCommandOptions{Store: func() credentials.Store { return store }, Env: fwcfg.MapEnv{}, EnvironmentBridge: func() (string, error) { t.Fatal("stored credentials must use their original Bridge"); return "", nil }}, store
}

func TestAuthStatusUsesLoginBridgeAndHidesCredentials(t *testing.T) {
	token := commandJWT(commandSessionID, time.Now().Add(time.Hour))
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/graphql" || r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("wrong authenticated endpoint")
		}
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if !strings.Contains(request.Query, "fragment AuthSessionFields") || request.Variables["first"] != float64(100) {
			t.Error("missing shared operation or bounded page")
		}
		authPageResponse(w, true)
	}))
	defer server.Close()
	options, _ := storedCommandOptions(t, server.URL, token)
	out, err := runAuthJSON(t, options, "status")
	if err != nil || calls != 1 {
		t.Fatalf("status: calls=%d err=%v", calls, err)
	}
	for _, want := range []string{`"authenticated":true`, `"userId":"user-1"`, `"tenantId":"tenant-1"`, `"credentialSource":"cli_store"`, `"label":"My CLI"`, server.URL} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in %s", want, out)
		}
	}
	for _, secret := range []string{token, "private-refresh"} {
		if strings.Contains(out, secret) {
			t.Fatal("status exposed a credential")
		}
	}
}

func TestAuthEnvironmentTokenNeverReadsStoreOrRenews(t *testing.T) {
	for _, code := range []int{200, 401} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			token := commandJWT(commandSessionID, time.Now().Add(-time.Hour))
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/graphql" || r.Header.Get("Authorization") != "Bearer "+token {
					t.Error("borrowed token used for renewal or wrong endpoint")
				}
				w.WriteHeader(code)
				if code == 200 {
					authPageResponse(w, true)
				}
			}))
			defer server.Close()
			options := authCommandOptions{Store: func() credentials.Store { t.Fatal("borrowed credential accessed store"); return nil }, Env: fwcfg.MapEnv{credentials.EnvUserToken: token}, EnvironmentBridge: func() (string, error) { return server.URL, nil }}
			out, err := runAuthJSON(t, options, "status")
			if calls != 1 || (code == 401) != (err != nil) {
				t.Fatalf("calls=%d code=%d err=%v", calls, code, err)
			}
			if code == 200 && !strings.Contains(out, `"credentialSource":"FW_USER_TOKEN"`) {
				t.Fatal("borrowed credential owner missing")
			}
		})
	}
}

func TestAuthRevokeOwnSessionPreservesReplacementLogin(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		t.Run(fmt.Sprint(replacement), func(t *testing.T) {
			token := commandJWT(commandSessionID, time.Now().Add(time.Hour))
			var store *memoryCredentialStore
			var origin string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Query     string         `json:"query"`
					Variables map[string]any `json:"variables"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if request.Variables["id"] != strings.ToUpper(commandSessionID) || !strings.Contains(request.Query, "fragment AuthErrorFields") {
					t.Error("wrong revoke operation")
				}
				if replacement {
					if err := credentials.SaveSession(context.Background(), store, origin, commandJWT("50000000-0000-4000-8000-0000000000ac", time.Now().Add(time.Hour)), "replacement-refresh"); err != nil {
						t.Error(err)
					}
				}
				fmt.Fprint(w, `{"data":{"revokeAuthSession":{"__typename":"RevokeAuthSessionPayload","success":true,"current":true}}}`)
			}))
			defer server.Close()
			origin = server.URL
			options, s := storedCommandOptions(t, origin, token)
			store = s
			out, err := runAuthJSON(t, options, "sessions", "revoke", strings.ToUpper(commandSessionID))
			if err != nil || !strings.Contains(out, fmt.Sprintf(`"credentialsCleared":%t`, !replacement)) {
				t.Fatalf("revoke: %s err=%v", out, err)
			}
			retained, _ := store.Get(credentials.AccountUserSession)
			refresh, _ := store.Get(credentials.AccountUserRefresh)
			if replacement && (retained == "" || refresh != "replacement-refresh") || !replacement && (retained != "" || refresh != "") {
				t.Fatal("incorrect credential ownership cleanup")
			}
		})
	}
}

func TestAuthStatusRetries401OnceThroughOriginalBridge(t *testing.T) {
	old := commandJWT(commandSessionID, time.Now().Add(time.Hour))
	freshExpiry := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	fresh := commandJWT(commandSessionID, freshExpiry)
	calls, renewals := 0, 0
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/oauth/token" {
			renewals++
			fmt.Fprintf(w, `{"access_token":%q,"refresh_token":"rotated"}`, fresh)
			return
		}
		calls++
		data, _ := io.ReadAll(r.Body)
		if calls == 1 {
			body = string(data)
			w.WriteHeader(401)
			return
		}
		if body != string(data) || r.Header.Get("Authorization") != "Bearer "+fresh {
			t.Error("retry changed operation or reused rejected credential")
		}
		authPageResponse(w, true)
	}))
	defer server.Close()
	options, store := storedCommandOptions(t, server.URL, old)
	out, err := runAuthJSON(t, options, "status")
	if err != nil || calls != 2 || renewals != 1 {
		t.Fatalf("calls=%d renewals=%d err=%v", calls, renewals, err)
	}
	token, _ := store.Get(credentials.AccountUserSession)
	if token != fresh {
		t.Fatal("rotation not saved")
	}
	if !strings.Contains(out, freshExpiry.Format(time.RFC3339)) {
		t.Fatal("status displayed rejected token expiry")
	}
}

func TestAuthListBoundsAndRefusedMutation(t *testing.T) {
	token := commandJWT(commandSessionID, time.Now().Add(time.Hour))
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		fmt.Fprint(w, `{"data":{"revokeAuthSession":{"__typename":"AuthError","code":"NOT_FOUND","message":"session not found"}}}`)
	}))
	defer server.Close()
	options, store := storedCommandOptions(t, server.URL, token)
	if _, err := runAuthJSON(t, options, "sessions", "list", "--first", "101"); err == nil || calls != 0 {
		t.Fatal("unbounded list reached Bridge")
	}
	if _, err := runAuthJSON(t, options, "sessions", "revoke", commandSessionID); err == nil || err.Error() != "session not found" {
		t.Fatalf("mutation refusal: %v", err)
	}
	if got, _ := store.Get(credentials.AccountUserSession); got != token {
		t.Fatal("refused revoke cleared credentials")
	}
}
