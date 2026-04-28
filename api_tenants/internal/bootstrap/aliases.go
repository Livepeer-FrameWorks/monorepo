package bootstrap

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// AliasMap is the live alias→tenant_id resolver. Loaded once per reconcile run
// from quartermaster.bootstrap_tenant_aliases; reconcilers extend it as they
// create tenants. Concurrent access is single-threaded inside a reconcile run.
type AliasMap struct {
	byAlias map[string]string
}

// LoadAliasMap reads the entire alias mapping table. Cheap (≤ tens of rows).
// Caller supplies the executor — typically the outer bootstrap transaction so
// reads see uncommitted aliases written earlier in the same reconcile.
func LoadAliasMap(ctx context.Context, exec DBTX) (*AliasMap, error) {
	rows, err := exec.QueryContext(ctx, `SELECT alias, tenant_id::text FROM quartermaster.bootstrap_tenant_aliases`)
	if err != nil {
		return nil, fmt.Errorf("load alias map: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only

	m := &AliasMap{byAlias: map[string]string{}}
	for rows.Next() {
		var alias, id string
		if err := rows.Scan(&alias, &id); err != nil {
			return nil, fmt.Errorf("scan alias row: %w", err)
		}
		m.byAlias[alias] = id
	}
	return m, rows.Err()
}

// LookupAlias returns the tenant UUID for an alias. ok=false when unknown.
func (m *AliasMap) LookupAlias(alias string) (string, bool) {
	if m == nil {
		return "", false
	}
	id, ok := m.byAlias[alias]
	return id, ok
}

// LookupRef resolves a TenantRef to a tenant UUID. The system tenant alias
// (SystemTenantAlias) is used for ref `quartermaster.system_tenant`; customer
// aliases are extracted from `quartermaster.tenants[<alias>]`.
func (m *AliasMap) LookupRef(ref string) (string, error) {
	alias, err := AliasFromRef(ref)
	if err != nil {
		return "", err
	}
	id, ok := m.LookupAlias(alias)
	if !ok {
		return "", fmt.Errorf("tenant ref %q: alias %q not in quartermaster.bootstrap_tenant_aliases (run quartermaster bootstrap first)", ref, alias)
	}
	return id, nil
}

// recordAlias inserts or refreshes the alias→tenant_id row inside the outer
// reconcile transaction. Use after creating a tenant so the same alias finds
// the same tenant on subsequent reconcile runs.
func recordAlias(ctx context.Context, exec DBTX, alias, tenantID string) error {
	if !ValidAlias(alias) {
		return fmt.Errorf("alias %q invalid: must match ^[a-z][a-z0-9-]*$ (1-64 chars)", alias)
	}
	const upsert = `
		INSERT INTO quartermaster.bootstrap_tenant_aliases (alias, tenant_id, created_at, updated_at)
		VALUES ($1, $2::uuid, NOW(), NOW())
		ON CONFLICT (alias) DO UPDATE SET
			tenant_id = EXCLUDED.tenant_id,
			updated_at = NOW()
		WHERE quartermaster.bootstrap_tenant_aliases.tenant_id = EXCLUDED.tenant_id`
	res, err := exec.ExecContext(ctx, upsert, alias, tenantID)
	if err != nil {
		return fmt.Errorf("upsert alias %q: %w", alias, err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		// The WHERE clause guards against alias collision: an existing alias
		// pointing at a different tenant must NOT be silently overwritten.
		return fmt.Errorf("alias %q already mapped to a different tenant; refusing silent reassignment", alias)
	}
	return nil
}

// validAliasRE mirrors cli/pkg/bootstrap.validAliasRE — operator-readable
// identifier, no whitespace/slashes/brackets/uppercase, must start with a
// letter.
var validAliasRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

// ValidAlias reports whether s matches the alias charset contract.
func ValidAlias(s string) bool { return validAliasRE.MatchString(s) }

// AliasFromRef parses a TenantRef.Ref into the alias it points at.
//   - "quartermaster.system_tenant"        -> ("frameworks", nil)
//   - "quartermaster.tenants[<alias>]"     -> ("<alias>", nil)
//
// Anything else fails loud.
func AliasFromRef(ref string) (string, error) {
	if ref == "quartermaster.system_tenant" {
		return SystemTenantAlias, nil
	}
	const prefix = "quartermaster.tenants["
	if strings.HasPrefix(ref, prefix) && strings.HasSuffix(ref, "]") {
		alias := strings.TrimSuffix(strings.TrimPrefix(ref, prefix), "]")
		if !ValidAlias(alias) {
			return "", fmt.Errorf("malformed tenant ref %q: alias %q fails charset", ref, alias)
		}
		return alias, nil
	}
	return "", fmt.Errorf("malformed tenant ref %q: expected quartermaster.system_tenant or quartermaster.tenants[<alias>]", ref)
}

// addAlias adds an entry to the in-memory map after a tenant was just created
// in the same transaction; subsequent reconcilers in the same run can resolve
// against it without re-reading from the DB.
func (m *AliasMap) addAlias(alias, tenantID string) {
	if m == nil {
		return
	}
	m.byAlias[alias] = tenantID
}
