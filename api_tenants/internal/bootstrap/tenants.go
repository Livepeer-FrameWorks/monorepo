package bootstrap

import (
	"context"
	"errors"
	"fmt"
)

// ReconcileTenants reconciles the system tenant and every customer tenant. It
// runs against the caller-supplied executor (typically the outer bootstrap
// transaction); it returns a refreshed alias map for downstream reconcilers.
//
// Stable key: alias. The system tenant alias is hardcoded SystemTenantAlias and
// must not appear in the customer list. Tenant identity (alias→UUID) is the
// gate that makes bootstrap idempotent — see api_tenants/internal/bootstrap's
// alias-map docs.
func ReconcileTenants(ctx context.Context, exec DBTX, system *Tenant, customers []Tenant) (*AliasMap, Result, error) {
	if exec == nil {
		return nil, Result{}, errors.New("ReconcileTenants: nil executor")
	}

	aliases := &AliasMap{byAlias: map[string]string{}}
	if err := loadAliasMapInto(ctx, exec, aliases); err != nil {
		return nil, Result{}, err
	}

	res := Result{}

	if system != nil {
		if system.Alias == "" {
			system.Alias = SystemTenantAlias
		}
		if system.Alias != SystemTenantAlias {
			return nil, Result{}, fmt.Errorf("system_tenant.alias must be %q (got %q)", SystemTenantAlias, system.Alias)
		}
		action, err := upsertTenant(ctx, exec, *system, aliases)
		if err != nil {
			return nil, Result{}, fmt.Errorf("system_tenant: %w", err)
		}
		appendAction(&res, system.Alias, action)
	}

	for _, t := range customers {
		if t.Alias == SystemTenantAlias {
			return nil, Result{}, fmt.Errorf("customer tenant alias %q is reserved for the system tenant", SystemTenantAlias)
		}
		if !ValidAlias(t.Alias) {
			return nil, Result{}, fmt.Errorf("customer tenant alias %q invalid", t.Alias)
		}
		action, err := upsertTenant(ctx, exec, t, aliases)
		if err != nil {
			return nil, Result{}, fmt.Errorf("tenant %q: %w", t.Alias, err)
		}
		appendAction(&res, t.Alias, action)
	}

	return aliases, res, nil
}

func loadAliasMapInto(ctx context.Context, exec DBTX, m *AliasMap) error {
	rows, err := exec.QueryContext(ctx, `SELECT alias, tenant_id::text FROM quartermaster.bootstrap_tenant_aliases`)
	if err != nil {
		return fmt.Errorf("load alias map: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only

	for rows.Next() {
		var alias, id string
		if err := rows.Scan(&alias, &id); err != nil {
			return fmt.Errorf("scan alias row: %w", err)
		}
		m.byAlias[alias] = id
	}
	return rows.Err()
}

func upsertTenant(ctx context.Context, exec DBTX, t Tenant, aliases *AliasMap) (string, error) {
	if t.Name == "" {
		return "", fmt.Errorf("tenant %q: name required", t.Alias)
	}

	if id, ok := aliases.LookupAlias(t.Alias); ok {
		return updateTenantByID(ctx, exec, id, t)
	}

	tier := t.DeploymentTier
	if tier == "" {
		tier = "global"
	}
	primary := t.PrimaryColor
	if primary == "" {
		primary = "#6366f1"
	}
	secondary := t.SecondaryColor
	if secondary == "" {
		secondary = "#f59e0b"
	}

	const insertSQL = `
		INSERT INTO quartermaster.tenants (name, deployment_tier, primary_color, secondary_color, created_at, updated_at)
		VALUES ($1, $2, $3, $4, NOW(), NOW())
		RETURNING id::text`
	var id string
	if err := exec.QueryRowContext(ctx, insertSQL, t.Name, tier, primary, secondary).Scan(&id); err != nil {
		return "", fmt.Errorf("insert tenant: %w", err)
	}
	if err := recordAlias(ctx, exec, t.Alias, id); err != nil {
		return "", err
	}
	aliases.addAlias(t.Alias, id)
	return "created", nil
}

func updateTenantByID(ctx context.Context, exec DBTX, id string, t Tenant) (string, error) {
	const probeSQL = `
		SELECT name, COALESCE(deployment_tier,''), COALESCE(primary_color,''), COALESCE(secondary_color,'')
		FROM quartermaster.tenants WHERE id = $1::uuid`
	var curName, curTier, curPrimary, curSecondary string
	if err := exec.QueryRowContext(ctx, probeSQL, id).Scan(&curName, &curTier, &curPrimary, &curSecondary); err != nil {
		return "", fmt.Errorf("probe tenant %s: %w", id, err)
	}

	tier := t.DeploymentTier
	if tier == "" {
		tier = "global"
	}
	primary := t.PrimaryColor
	if primary == "" {
		primary = "#6366f1"
	}
	secondary := t.SecondaryColor
	if secondary == "" {
		secondary = "#f59e0b"
	}

	if curName == t.Name && curTier == tier && curPrimary == primary && curSecondary == secondary {
		return "noop", nil
	}

	const updateSQL = `
		UPDATE quartermaster.tenants
		SET name = $2, deployment_tier = $3, primary_color = $4, secondary_color = $5, updated_at = NOW()
		WHERE id = $1::uuid`
	if _, err := exec.ExecContext(ctx, updateSQL, id, t.Name, tier, primary, secondary); err != nil {
		return "", fmt.Errorf("update tenant %s: %w", id, err)
	}
	return "updated", nil
}

func appendAction(r *Result, key, action string) {
	switch action {
	case "created":
		r.Created = append(r.Created, key)
	case "updated":
		r.Updated = append(r.Updated, key)
	case "noop":
		r.Noop = append(r.Noop, key)
	}
}
