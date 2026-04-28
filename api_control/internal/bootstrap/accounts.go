package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"frameworks/pkg/auth"

	"github.com/google/uuid"
	"github.com/lib/pq"
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
			action, warn, err := reconcileUser(ctx, exec, tenantID, u, allowResetCredentials)
			if err != nil {
				return Result{}, warnings, fmt.Errorf("account %s user %s: %w", acc.Tenant.Ref, u.Email, err)
			}
			if warn != "" {
				warnings = append(warnings, warn)
			}
			key := acc.Tenant.Ref + "/" + u.Email
			switch action {
			case "created":
				res.Created = append(res.Created, key)
			case "updated":
				res.Updated = append(res.Updated, key)
			case "noop":
				res.Noop = append(res.Noop, key)
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
	case AccountSystemOperator, AccountCustomer:
	default:
		return fmt.Errorf("account kind must be %q or %q (got %q)", AccountSystemOperator, AccountCustomer, a.Kind)
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
	return nil
}

func reconcileUser(ctx context.Context, exec DBTX, tenantID string, u AccountUser, allowReset bool) (string, string, error) {
	const probeSQL = `
		SELECT id::text,
		       COALESCE(first_name, ''),
		       COALESCE(last_name, ''),
		       role,
		       COALESCE(permissions, '{}')
		FROM commodore.users
		WHERE tenant_id = $1::uuid AND email = $2`
	var (
		id, curFirst, curLast, curRole string
		curPerms                       pq.StringArray
	)
	err := exec.QueryRowContext(ctx, probeSQL, tenantID, u.Email).Scan(&id, &curFirst, &curLast, &curRole, &curPerms)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		hash, hashErr := auth.HashPassword(u.Password)
		if hashErr != nil {
			return "", "", fmt.Errorf("hash password: %w", hashErr)
		}
		userID := uuid.New().String()
		const insertSQL = `
			INSERT INTO commodore.users
				(id, tenant_id, email, password_hash, first_name, last_name, role, permissions,
				 is_active, verified, created_at, updated_at)
			VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8,
			        true, true, NOW(), NOW())`
		if _, insertErr := exec.ExecContext(ctx, insertSQL,
			userID, tenantID, u.Email, hash, u.FirstName, u.LastName, u.Role,
			pq.Array(defaultPermissions(u.Role)),
		); insertErr != nil {
			return "", "", fmt.Errorf("insert user: %w", insertErr)
		}
		return "created", "", nil
	case err != nil:
		return "", "", fmt.Errorf("probe user: %w", err)
	}

	desiredPerms := defaultPermissions(u.Role)
	profileEq := curFirst == u.FirstName && curLast == u.LastName && curRole == u.Role &&
		stringSliceEq([]string(curPerms), desiredPerms)

	if u.ResetCredentials {
		if !allowReset {
			profileNote := ""
			if !profileEq {
				profileNote = " (profile changes still applied)"
			}
			warn := fmt.Sprintf("user %q has reset_credentials=true but --reset-credentials was not passed; password left untouched%s", u.Email, profileNote)
			if profileEq {
				return "noop", warn, nil
			}
			if err := updateUserProfile(ctx, exec, id, u, desiredPerms); err != nil {
				return "", warn, err
			}
			return "updated", warn, nil
		}
		hash, hashErr := auth.HashPassword(u.Password)
		if hashErr != nil {
			return "", "", fmt.Errorf("hash password: %w", hashErr)
		}
		const updateSQL = `
			UPDATE commodore.users
			SET first_name = $2, last_name = $3, role = $4, permissions = $5,
			    password_hash = $6, updated_at = NOW()
			WHERE id = $1::uuid`
		if _, updateErr := exec.ExecContext(ctx, updateSQL, id, u.FirstName, u.LastName, u.Role,
			pq.Array(desiredPerms), hash); updateErr != nil {
			return "", "", fmt.Errorf("update user (with credentials): %w", updateErr)
		}
		return "updated", "", nil
	}

	if profileEq {
		return "noop", "", nil
	}
	if err := updateUserProfile(ctx, exec, id, u, desiredPerms); err != nil {
		return "", "", err
	}
	return "updated", "", nil
}

func updateUserProfile(ctx context.Context, exec DBTX, id string, u AccountUser, perms []string) error {
	const updateSQL = `
		UPDATE commodore.users
		SET first_name = $2, last_name = $3, role = $4, permissions = $5, updated_at = NOW()
		WHERE id = $1::uuid`
	if _, err := exec.ExecContext(ctx, updateSQL, id, u.FirstName, u.LastName, u.Role, pq.Array(perms)); err != nil {
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
