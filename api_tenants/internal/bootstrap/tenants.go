package bootstrap

import (
	"context"
	"errors"
	"fmt"

	"frameworks/api_tenants/internal/database/quartermasterdb"
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
	rows, err := quartermasterdb.New(exec).ListBootstrapTenantAliases(ctx)
	if err != nil {
		return fmt.Errorf("load alias map: %w", err)
	}
	for _, row := range rows {
		m.byAlias[row.Alias] = row.TenantID
	}
	return nil
}

func upsertTenant(ctx context.Context, exec DBTX, t Tenant, aliases *AliasMap) (string, error) {
	if t.Name == "" {
		return "", fmt.Errorf("tenant %q: name required", t.Alias)
	}

	if id, ok := aliases.LookupAlias(t.Alias); ok {
		return updateTenantByID(ctx, exec, id, t)
	}

	// deployment_tier is an insert-only seed: 'free' unless the desired state
	// names a tier. Purser owns the column afterwards (stamps the billing
	// tier_name), so updates below never touch it.
	tier := t.DeploymentTier
	if tier == "" {
		tier = "free"
	}
	primary := t.PrimaryColor
	if primary == "" {
		primary = "#6366f1"
	}
	secondary := t.SecondaryColor
	if secondary == "" {
		secondary = "#f59e0b"
	}

	id, err := quartermasterdb.New(exec).InsertBootstrapTenant(ctx, quartermasterdb.InsertBootstrapTenantParams{
		Name: t.Name, DeploymentTier: tier, PrimaryColor: primary, SecondaryColor: secondary,
	})
	if err != nil {
		return "", fmt.Errorf("insert tenant: %w", err)
	}
	if err := recordAlias(ctx, exec, t.Alias, id); err != nil {
		return "", err
	}
	aliases.addAlias(t.Alias, id)
	return "created", nil
}

// updateTenantByID reconciles name and branding only. deployment_tier is
// deliberately excluded: Purser owns it after insert (stamps the billing
// tier_name), and bootstrap rewriting it from desired state would fight that
// authority on every run.
func updateTenantByID(ctx context.Context, exec DBTX, id string, t Tenant) (string, error) {
	queries := quartermasterdb.New(exec)
	current, err := queries.GetBootstrapTenant(ctx, id)
	if err != nil {
		return "", fmt.Errorf("probe tenant %s: %w", id, err)
	}

	primary := t.PrimaryColor
	if primary == "" {
		primary = "#6366f1"
	}
	secondary := t.SecondaryColor
	if secondary == "" {
		secondary = "#f59e0b"
	}

	if current.Name == t.Name && current.PrimaryColor == primary && current.SecondaryColor == secondary {
		return "noop", nil
	}

	if err := queries.UpdateBootstrapTenant(ctx, quartermasterdb.UpdateBootstrapTenantParams{
		ID: id, Name: t.Name, PrimaryColor: primary, SecondaryColor: secondary,
	}); err != nil {
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
