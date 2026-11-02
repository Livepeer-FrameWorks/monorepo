package bootstrap

import (
	"context"
	"database/sql"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

type staticResolverFn func(context.Context, string) (string, error)

func (s staticResolverFn) Resolve(ctx context.Context, alias string) (string, error) {
	return s(ctx, alias)
}

func staticResolver(id string) TenantResolver {
	return staticResolverFn(func(context.Context, string) (string, error) { return id, nil })
}

func sysAccount() Account {
	return Account{
		Kind:   AccountSystemOperator,
		Tenant: TenantRef{Ref: "quartermaster.system_tenant"},
		Users: []AccountUser{{
			Email:     "ops@example.com",
			Role:      "owner",
			FirstName: "Ops",
			LastName:  "Person",
			Password:  "supersecret",
		}},
	}
}

func TestReconcileAccountsRejectsNilDB(t *testing.T) {
	if _, _, err := ReconcileAccounts(context.Background(), nil, []Account{sysAccount()}, staticResolver("uuid"), false); err == nil {
		t.Fatal("expected error on nil db")
	}
}

func TestReconcileAccountsRejectsNilResolver(t *testing.T) {
	db, _, _ := sqlmock.New()
	defer db.Close()
	if _, _, err := ReconcileAccounts(context.Background(), db, []Account{sysAccount()}, nil, false); err == nil {
		t.Fatal("expected error on nil resolver")
	}
}

func TestReconcileAccountsRejectsBadRole(t *testing.T) {
	db, _, _ := sqlmock.New()
	defer db.Close()
	bad := sysAccount()
	bad.Users[0].Role = "superuser"
	if _, _, err := ReconcileAccounts(context.Background(), db, []Account{bad}, staticResolver("uuid-system"), false); err == nil {
		t.Fatal("expected error on invalid role")
	}
}

func TestReconcileAccountsCreatesUser(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	mock.ExpectQuery(regexp.QuoteMeta("FROM commodore.users")).
		WithArgs("uuid-system", "ops@example.com").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO commodore.users")).
		WillReturnResult(sqlmock.NewResult(0, 1))

	res, _, err := ReconcileAccounts(context.Background(), db, []Account{sysAccount()}, staticResolver("uuid-system"), false)
	if err != nil {
		t.Fatalf("ReconcileAccounts: %v", err)
	}
	if len(res.Created) != 1 {
		t.Errorf("expected 1 created, got %v", res)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet: %v", err)
	}
}

func TestReconcileAccountsResetCredentialsRequiresFlag(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	// existing user matches the desired profile exactly
	mock.ExpectQuery(regexp.QuoteMeta("FROM commodore.users")).
		WithArgs("uuid-system", "ops@example.com").
		WillReturnRows(sqlmock.NewRows([]string{"id", "first_name", "last_name", "role", "permissions", "platform_operator"}).
			AddRow("uuid-user", "Ops", "Person", "owner", `{read,write,admin}`, false))

	acc := sysAccount()
	acc.Users[0].ResetCredentials = true
	res, warnings, err := ReconcileAccounts(context.Background(), db, []Account{acc}, staticResolver("uuid-system"), false)
	if err != nil {
		t.Fatalf("ReconcileAccounts: %v", err)
	}
	if len(res.Noop) != 1 {
		t.Errorf("expected 1 noop (password should NOT have been rewritten without --reset-credentials), got %v", res)
	}
	if len(warnings) != 1 {
		t.Errorf("expected 1 warning about reset_credentials without flag, got %v", warnings)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet: %v", err)
	}
}

func TestAliasFromRefUnit(t *testing.T) {
	cases := []struct {
		ref     string
		want    string
		wantErr bool
	}{
		{"quartermaster.system_tenant", "frameworks", false},
		{"quartermaster.tenants[acme]", "acme", false},
		{"random.thing", "", true},
	}
	for _, c := range cases {
		got, err := AliasFromRef(c.ref)
		if c.wantErr {
			if err == nil {
				t.Errorf("AliasFromRef(%q): expected error", c.ref)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("AliasFromRef(%q) = (%q, %v), want (%q, nil)", c.ref, got, err, c.want)
		}
	}
}

const bootstrapWallet = "0x52908400098527886E0F7030069857D2E4169EE7"

func existingOpsUser(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(regexp.QuoteMeta("FROM commodore.users")).
		WithArgs("uuid-system", "ops@example.com").
		WillReturnRows(sqlmock.NewRows([]string{"id", "first_name", "last_name", "role", "permissions", "platform_operator"}).
			AddRow("uuid-user", "Ops", "Person", "owner", `{read,write,admin}`, false))
}

func walletAccount() Account {
	acc := sysAccount()
	acc.Users[0].Wallet = "0x52908400098527886e0f7030069857d2e4169ee7"
	return acc
}

func TestReconcileAccountsLinksDeclaredWallet(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	existingOpsUser(mock)
	mock.ExpectQuery(regexp.QuoteMeta("FROM commodore.wallet_identities")).
		WithArgs("ethereum", bootstrapWallet).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO commodore.wallet_identities")).
		WithArgs("uuid-system", "uuid-user", bootstrapWallet).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at"}).AddRow("uuid-wallet", nil))

	res, warnings, err := ReconcileAccounts(context.Background(), db, []Account{walletAccount()}, staticResolver("uuid-system"), false)
	if err != nil {
		t.Fatalf("ReconcileAccounts: %v", err)
	}
	if len(res.Updated) != 1 || len(res.Noop) != 0 || len(warnings) != 0 {
		t.Fatalf("linking a wallet to an unchanged user = %+v warnings=%v, want one updated", res, warnings)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet: %v", err)
	}
}

func TestReconcileAccountsKeepsLinkedWallet(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	existingOpsUser(mock)
	mock.ExpectQuery(regexp.QuoteMeta("FROM commodore.wallet_identities")).
		WithArgs("ethereum", bootstrapWallet).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "user_id"}).AddRow("uuid-system", "uuid-user"))

	res, _, err := ReconcileAccounts(context.Background(), db, []Account{walletAccount()}, staticResolver("uuid-system"), false)
	if err != nil {
		t.Fatalf("ReconcileAccounts: %v", err)
	}
	if len(res.Noop) != 1 || len(res.Updated) != 0 {
		t.Fatalf("an already linked wallet = %+v, want one noop", res)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet: %v", err)
	}
}

func TestReconcileAccountsDefersWalletOfAnotherUser(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	existingOpsUser(mock)
	mock.ExpectQuery(regexp.QuoteMeta("FROM commodore.wallet_identities")).
		WithArgs("ethereum", bootstrapWallet).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "user_id"}).AddRow("uuid-other-tenant", "uuid-other-user"))

	res, warnings, err := ReconcileAccounts(context.Background(), db, []Account{walletAccount()}, staticResolver("uuid-system"), false)
	if err != nil {
		t.Fatalf("ReconcileAccounts: %v", err)
	}
	// The user's own fields and its wallet are reported under separate keys:
	// the unchanged user is a noop and only its wallet is deferred.
	key := "quartermaster.system_tenant/ops@example.com"
	if len(res.Deferred) != 1 || res.Deferred[0] != key+"/wallet" || len(warnings) != 1 {
		t.Fatalf("a wallet linked elsewhere = %+v warnings=%v, want only %s/wallet deferred with a warning", res, warnings, key)
	}
	if len(res.Noop) != 1 || res.Noop[0] != key || len(res.Updated) != 0 || len(res.Created) != 0 {
		t.Fatalf("a wallet linked elsewhere = %+v, want the unchanged user %s as a noop", res, key)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet: %v (the other user's link must not be touched)", err)
	}
}

func TestReconcileAccountsRejectsMalformedWallet(t *testing.T) {
	db, _, _ := sqlmock.New()
	defer db.Close()
	acc := sysAccount()
	acc.Users[0].Wallet = "0x1234"
	if _, _, err := ReconcileAccounts(context.Background(), db, []Account{acc}, staticResolver("uuid-system"), false); err == nil {
		t.Fatal("expected error on a malformed wallet")
	}
}
