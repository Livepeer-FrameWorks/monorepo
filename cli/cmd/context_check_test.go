package cmd

import (
	"context"
	"errors"
	"testing"

	fwcfg "frameworks/cli/internal/config"
	fwcredentials "frameworks/cli/internal/credentials"
)

// stubStore satisfies credentials.Store for tests. Get returns whatever
// is in entries; Set/Delete record calls but otherwise no-op.
type stubStore struct {
	entries map[string]string
	getErr  error
}

func (s *stubStore) Get(account string) (string, error) {
	if s.getErr != nil {
		return "", s.getErr
	}
	return s.entries[account], nil
}
func (s *stubStore) Set(account, value string) error { s.entries[account] = value; return nil }
func (s *stubStore) Delete(account string) error     { delete(s.entries, account); return nil }
func (s *stubStore) Name() string                    { return "stub" }

func TestRunPersonaChecksRejectsEmptyPersona(t *testing.T) {
	t.Parallel()
	got := runPersonaChecks(context.Background(), fwcfg.Context{}, fwcfg.Config{}, fwcfg.MapEnv{}, &stubStore{})
	if len(got) != 1 || got[0].OK || got[0].Name != "persona" {
		t.Fatalf("expected single failed 'persona' result, got %+v", got)
	}
}

func TestRunPersonaChecksRejectsUnknownPersona(t *testing.T) {
	t.Parallel()
	got := runPersonaChecks(context.Background(), fwcfg.Context{Persona: "operator"}, fwcfg.Config{}, fwcfg.MapEnv{}, &stubStore{})
	if len(got) != 1 || got[0].OK {
		t.Fatalf("expected failed result for unknown persona, got %+v", got)
	}
}

func TestRunPersonaChecksPlatformMissingSystemTenantID(t *testing.T) {
	t.Parallel()
	c := fwcfg.Context{Persona: fwcfg.PersonaPlatform}
	got := runPersonaChecks(context.Background(), c, fwcfg.Config{}, fwcfg.MapEnv{}, &stubStore{})
	if len(got) < 1 || got[0].Name != "system_tenant_id" || got[0].OK {
		t.Fatalf("expected failed system_tenant_id assertion, got %+v", got)
	}
	// Service-token assertion must still run (independent failure modes).
	foundServiceToken := false
	for _, r := range got {
		if r.Name == "service_token" {
			foundServiceToken = true
			if r.OK {
				t.Fatalf("service_token should fail without gitops source, got OK=true")
			}
		}
	}
	if !foundServiceToken {
		t.Fatalf("expected service_token assertion in results, got %+v", got)
	}
}

func TestRunPersonaChecksPlatformWithSystemTenantIDStillFailsWithoutGitops(t *testing.T) {
	t.Parallel()
	c := fwcfg.Context{Persona: fwcfg.PersonaPlatform, SystemTenantID: "00000000-0000-0000-0000-000000000001"}
	got := runPersonaChecks(context.Background(), c, fwcfg.Config{}, fwcfg.MapEnv{}, &stubStore{})
	// system_tenant_id passes; service_token fails because no gitops source.
	if len(got) != 2 {
		t.Fatalf("expected 2 results, got %d (%+v)", len(got), got)
	}
	if got[0].Name != "system_tenant_id" || !got[0].OK {
		t.Fatalf("system_tenant_id should pass, got %+v", got[0])
	}
	if got[1].Name != "service_token" || got[1].OK {
		t.Fatalf("service_token should fail without gitops, got %+v", got[1])
	}
}

func TestRunPersonaChecksSelfHostedNoJWT(t *testing.T) {
	t.Parallel()
	c := fwcfg.Context{Persona: fwcfg.PersonaSelfHosted}
	got := runPersonaChecks(context.Background(), c, fwcfg.Config{}, fwcfg.MapEnv{}, &stubStore{entries: map[string]string{}})
	if len(got) != 1 || got[0].Name != "owner_jwt" || got[0].OK {
		t.Fatalf("expected failed owner_jwt, got %+v", got)
	}
}

func TestRunPersonaChecksSelfHostedJWTViaStore(t *testing.T) {
	t.Parallel()
	c := fwcfg.Context{Persona: fwcfg.PersonaSelfHosted}
	store := &stubStore{entries: map[string]string{fwcredentials.AccountUserSession: "eyJtoken"}}
	got := runPersonaChecks(context.Background(), c, fwcfg.Config{}, fwcfg.MapEnv{}, store)
	if len(got) != 1 || got[0].Name != "owner_jwt" || !got[0].OK {
		t.Fatalf("expected passing owner_jwt, got %+v", got)
	}
}

func TestRunPersonaChecksUserPersonaJWTViaEnv(t *testing.T) {
	t.Parallel()
	c := fwcfg.Context{Persona: fwcfg.PersonaUser}
	env := fwcfg.MapEnv{fwcredentials.EnvUserToken: "eyJtoken"}
	got := runPersonaChecks(context.Background(), c, fwcfg.Config{}, env, &stubStore{})
	if len(got) != 1 || got[0].Name != "user_jwt" || !got[0].OK {
		t.Fatalf("expected passing user_jwt, got %+v", got)
	}
}

func TestRunPersonaChecksEdgePersonaTreatedAsUser(t *testing.T) {
	t.Parallel()
	c := fwcfg.Context{Persona: fwcfg.PersonaEdge}
	got := runPersonaChecks(context.Background(), c, fwcfg.Config{}, fwcfg.MapEnv{}, &stubStore{})
	if len(got) != 1 || got[0].Name != "user_jwt" || got[0].OK {
		t.Fatalf("expected failed user_jwt for empty creds, got %+v", got)
	}
}

func TestRunPersonaChecksStoreErrorPropagates(t *testing.T) {
	t.Parallel()
	c := fwcfg.Context{Persona: fwcfg.PersonaSelfHosted}
	store := &stubStore{getErr: errors.New("keychain locked")}
	got := runPersonaChecks(context.Background(), c, fwcfg.Config{}, fwcfg.MapEnv{}, store)
	if len(got) != 1 || got[0].OK {
		t.Fatalf("expected failed result on store error, got %+v", got)
	}
}
