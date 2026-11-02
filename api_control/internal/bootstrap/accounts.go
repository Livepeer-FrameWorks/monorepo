package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	fwdb "github.com/Livepeer-FrameWorks/monorepo/pkg/database"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/yugabyte/pgx/v5/pgconn"
)

// ReconcileAccounts reconciles every Account's users into commodore.users.
// Stable key: (tenant_id, email). Idempotent semantics:
//
//   - User absent ⇒ create.
//   - User present, role/first_name/last_name/permissions match ⇒ noop.
//   - User present, profile fields differ ⇒ update profile only.
//   - User present and the entry sets ResetCredentials=true AND
//     allowResetCredentials=true ⇒ rehash + write password_hash.
//   - User present and password reset is NOT permitted/asked ⇒ leave the
//     existing password_hash untouched. This is the "never reset existing
//     passwords by accident" guarantee.
//   - Wallet declared and not linked ⇒ link it to the user (reported as
//     updated when the user itself was a noop). Linked to the user ⇒ noop.
//     Linked to another user ⇒ deferred with a warning; the link is never
//     moved. Removing the field leaves an existing link in place.
//
// allowResetCredentials reflects the operator-supplied flag at the cobra layer
// (commodore bootstrap --reset-credentials). Without it, ResetCredentials=true
// in the rendered file is treated as a no-op for password (with a warning) so
// a stale rendered artifact can't silently rotate live credentials.
//
// resolver resolves an Account.Tenant ref into a tenant UUID. The cobra
// dispatcher wires it to a Quartermaster gRPC client; tests inject a static
// map.
func ReconcileAccounts(
	ctx context.Context,
	exec DBTX,
	accounts []Account,
	resolver TenantResolver,
	allowResetCredentials bool,
) (Result, []string, error) {
	if exec == nil {
		return Result{}, nil, errors.New("ReconcileAccounts: nil executor")
	}
	if resolver == nil {
		return Result{}, nil, errors.New("ReconcileAccounts: nil tenant resolver")
	}

	res := Result{}
	var warnings []string

	for _, acc := range accounts {
		if err := validateAccount(acc); err != nil {
			return Result{}, warnings, err
		}
		alias, err := AliasFromRef(acc.Tenant.Ref)
		if err != nil {
			return Result{}, warnings, fmt.Errorf("account %s: %w", acc.Tenant.Ref, err)
		}
		tenantID, err := resolver.Resolve(ctx, alias)
		if err != nil {
			return Result{}, warnings, fmt.Errorf("account %s: %w", acc.Tenant.Ref, err)
		}
		for _, u := range acc.Users {
			if err := validateUser(u); err != nil {
				return Result{}, warnings, fmt.Errorf("account %s: %w", acc.Tenant.Ref, err)
			}
			userID, action, warn, err := reconcileUser(ctx, exec, tenantID, u, allowResetCredentials)
			if err != nil {
				return Result{}, warnings, fmt.Errorf("account %s user %s: %w", acc.Tenant.Ref, u.Email, err)
			}
			if warn != "" {
				warnings = append(warnings, warn)
			}
			key := acc.Tenant.Ref + "/" + u.Email
			if u.Wallet != "" && action != "deferred" {
				walletAction, walletErr := reconcileWallet(ctx, exec, tenantID, userID, u.Wallet)
				if walletErr != nil {
					return Result{}, warnings, fmt.Errorf("account %s user %s: %w", acc.Tenant.Ref, u.Email, walletErr)
				}
				switch walletAction {
				case "linked":
					if action == "noop" {
						action = "updated"
					}
				case "conflict":
					warnings = append(warnings, fmt.Sprintf("account %s user %s: wallet %s is linked to another user; it was not linked to this user", acc.Tenant.Ref, u.Email, u.Wallet))
					res.Deferred = append(res.Deferred, key+"/wallet")
				}
			}
			switch action {
			case "created":
				res.Created = append(res.Created, key)
			case "updated":
				res.Updated = append(res.Updated, key)
			case "noop":
				res.Noop = append(res.Noop, key)
			case "deferred":
				res.Deferred = append(res.Deferred, key)
			}
		}
	}

	return res, warnings, nil
}

func validateAccount(a Account) error {
	if a.Tenant.Ref == "" {
		return errors.New("tenant.ref required")
	}
	switch a.Kind {
	case AccountSystemOperator:
		if !isSystemTenantRef(a.Tenant.Ref) {
			return errors.New("system_operator must reference quartermaster.system_tenant")
		}
	case AccountCustomer:
	default:
		return fmt.Errorf("account kind must be %q or %q (got %q)", AccountSystemOperator, AccountCustomer, a.Kind)
	}
	for _, user := range a.Users {
		if user.PlatformOperator && !isSystemTenantRef(a.Tenant.Ref) {
			return errors.New("platform operators must belong to the system tenant")
		}
	}
	return nil
}

func validateUser(u AccountUser) error {
	if u.Email == "" {
		return errors.New("user email required")
	}
	switch u.Role {
	case "owner", "admin", "member":
	default:
		return fmt.Errorf("user %q: role must be \"owner\"|\"admin\"|\"member\" (got %q)", u.Email, u.Role)
	}
	if u.Password == "" {
		return fmt.Errorf("user %q: password required (rendered file should carry resolved plaintext)", u.Email)
	}
	if u.Wallet != "" {
		if _, err := auth.NormalizeEthAddress(u.Wallet); err != nil {
			return fmt.Errorf("user %q: wallet: %w", u.Email, err)
		}
	}
	return nil
}

// reconcileWallet links an Ethereum wallet to a bootstrap user. It returns
// "linked", "noop" (already linked to this user) or "conflict" (linked to
// another user, left untouched). Like the rest of the accounts bootstrap it
// emits no service event, so a link made here records no wallet_linked auth
// event.
func reconcileWallet(ctx context.Context, exec DBTX, tenantID, userID, wallet string) (string, error) {
	address, err := auth.NormalizeEthAddress(wallet)
	if err != nil {
		return "", fmt.Errorf("wallet: %w", err)
	}
	queries := commodoredb.New(exec)
	existing, err := queries.GetWalletIdentityByAddress(ctx, commodoredb.GetWalletIdentityByAddressParams{
		ChainType: string(auth.ChainEthereum), WalletAddress: address,
	})
	switch {
	case err == nil:
		if existing.UserID == userID {
			return "noop", nil
		}
		return "conflict", nil
	case !errors.Is(err, sql.ErrNoRows):
		return "", fmt.Errorf("probe wallet: %w", err)
	}
	if _, err := queries.InsertLinkedWallet(ctx, commodoredb.InsertLinkedWalletParams{
		TenantID: tenantID, UserID: userID, WalletAddress: address,
	}); err != nil {
		return "", fmt.Errorf("link wallet: %w", err)
	}
	return "linked", nil
}

// reconcileUser returns the user's ID, the action taken and an optional warning.
func reconcileUser(ctx context.Context, exec DBTX, tenantID string, u AccountUser, allowReset bool) (string, string, string, error) {
	queries := commodoredb.New(exec)
	current, err := queries.GetBootstrapUser(ctx, commodoredb.GetBootstrapUserParams{TenantID: tenantID, Email: u.Email})
	switch {
	case errors.Is(err, sql.ErrNoRows):
		hash, hashErr := auth.HashPassword(u.Password)
		if hashErr != nil {
			return "", "", "", fmt.Errorf("hash password: %w", hashErr)
		}
		userID := uuid.New().String()
		_, inTransaction := exec.(*sql.Tx)
		if inTransaction {
			if saveErr := queries.BeginBootstrapAccountUser(ctx); saveErr != nil {
				return "", "", "", saveErr
			}
		}
		if insertErr := queries.CreateBootstrapUser(ctx, commodoredb.CreateBootstrapUserParams{
			ID: userID, TenantID: tenantID, Email: u.Email, PasswordHash: hash,
			FirstName: u.FirstName, LastName: u.LastName, Role: u.Role,
			Permissions: defaultPermissions(u.Role), PlatformOperator: u.PlatformOperator,
		}); insertErr != nil {
			if closedTenantAccountError(insertErr) {
				if inTransaction {
					if rollbackErr := queries.RollbackBootstrapAccountUser(ctx); rollbackErr != nil {
						return "", "", "", rollbackErr
					}
					if releaseErr := queries.ReleaseBootstrapAccountUser(ctx); releaseErr != nil {
						return "", "", "", releaseErr
					}
				}
				return "", "deferred", fmt.Sprintf("account tenant %s user %s is permanently closed; remove this user declaration", tenantID, u.Email), nil
			}
			return "", "", "", fmt.Errorf("insert user: %w", insertErr)
		}
		if inTransaction {
			if releaseErr := queries.ReleaseBootstrapAccountUser(ctx); releaseErr != nil {
				return "", "", "", releaseErr
			}
		}
		return userID, "created", "", nil
	case err != nil:
		return "", "", "", fmt.Errorf("probe user: %w", err)
	}

	desiredPerms := defaultPermissions(u.Role)
	profileEq := current.FirstName == u.FirstName && current.LastName == u.LastName && current.Role == u.Role &&
		stringSliceEq(current.Permissions, desiredPerms) && current.PlatformOperator == u.PlatformOperator

	if u.ResetCredentials {
		if !allowReset {
			profileNote := ""
			if !profileEq {
				profileNote = " (profile changes still applied)"
			}
			warn := fmt.Sprintf("user %q has reset_credentials=true but --reset-credentials was not passed; password left untouched%s", u.Email, profileNote)
			if profileEq {
				return current.ID, "noop", warn, nil
			}
			if err := updateUserProfile(ctx, queries, current.ID, tenantID, u, desiredPerms); err != nil {
				return "", "", warn, err
			}
			return current.ID, "updated", warn, nil
		}
		hash, hashErr := auth.HashPassword(u.Password)
		if hashErr != nil {
			return "", "", "", fmt.Errorf("hash password: %w", hashErr)
		}
		if updateErr := queries.UpdateBootstrapUserWithCredentials(ctx, commodoredb.UpdateBootstrapUserWithCredentialsParams{
			FirstName: u.FirstName, LastName: u.LastName, Role: u.Role,
			Permissions: desiredPerms, PasswordHash: hash, PlatformOperator: u.PlatformOperator,
			ID: current.ID, TenantID: tenantID,
		}); updateErr != nil {
			return "", "", "", fmt.Errorf("update user (with credentials): %w", updateErr)
		}
		return current.ID, "updated", "", nil
	}

	if profileEq {
		return current.ID, "noop", "", nil
	}
	if err := updateUserProfile(ctx, queries, current.ID, tenantID, u, desiredPerms); err != nil {
		return "", "", "", err
	}
	return current.ID, "updated", "", nil
}

func updateUserProfile(ctx context.Context, queries *commodoredb.Queries, id, tenantID string, u AccountUser, perms []string) error {
	if err := queries.UpdateBootstrapUserProfile(ctx, commodoredb.UpdateBootstrapUserProfileParams{
		FirstName: u.FirstName, LastName: u.LastName, Role: u.Role,
		Permissions: perms, PlatformOperator: u.PlatformOperator, ID: id, TenantID: tenantID,
	}); err != nil {
		return fmt.Errorf("update user profile: %w", err)
	}
	return nil
}

// defaultPermissions mirrors api_control/internal/grpc/server.go's
// getDefaultPermissions so bootstrap-created users land with the same
// permission set runtime UserCreate emits.
func defaultPermissions(role string) []string {
	switch role {
	case "owner", "admin":
		return []string{"read", "write", "admin"}
	case "member":
		return []string{"read", "write"}
	default:
		return []string{"read"}
	}
}

func stringSliceEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// AliasFromRef parses a TenantRef.Ref into the alias the resolver should look
// up via Quartermaster's ResolveTenantAliases gRPC.
func AliasFromRef(ref string) (string, error) {
	if ref == "quartermaster.system_tenant" {
		return "frameworks", nil
	}
	const prefix = "quartermaster.tenants["
	if strings.HasPrefix(ref, prefix) && strings.HasSuffix(ref, "]") {
		return strings.TrimSuffix(strings.TrimPrefix(ref, prefix), "]"), nil
	}
	return "", fmt.Errorf("malformed tenant ref %q", ref)
}

// closedTenantAccountError distinguishes the account guard from unrelated checks.
func closedTenantAccountError(err error) bool {
	if fwdb.SQLState(err) != "23514" {
		return false
	}
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
		return pgErr.ConstraintName == "tenant_account_closed"
	}
	if pqErr, ok := errors.AsType[*pq.Error](err); ok {
		return pqErr.Constraint == "tenant_account_closed"
	}
	return false
}
