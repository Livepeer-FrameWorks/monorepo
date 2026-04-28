package bootstrap

import (
	"errors"
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"
)

func validRendered() *Rendered {
	d, _ := Derive(minimalManifest(), DeriveOptions{SharedEnv: map[string]string{"ACME_EMAIL": "ops@example.com"}})
	r, _ := Render(d, nil, nil)
	return r
}

func TestValidateAcceptsManifestDerivedRender(t *testing.T) {
	if err := validRendered().Validate(); err != nil {
		t.Fatalf("expected manifest-derived render to validate clean; got %v", err)
	}
}

func TestValidateRejectsTLSBundleWithoutEmail(t *testing.T) {
	m := minimalManifest()
	m.Services = map[string]inventory.ServiceConfig{
		"chatwoot": {Enabled: true, Host: "core-eu-1", Port: 18092},
	}
	d, _ := Derive(m, DeriveOptions{})
	r, _ := Render(d, nil, nil)
	err := r.Validate()
	if err == nil || !strings.Contains(err.Error(), "tls_bundles") || !strings.Contains(err.Error(), "email") {
		t.Fatalf("expected tls bundle email error; got %v", err)
	}
}

func TestValidateRejectsUnknownTenantOnCluster(t *testing.T) {
	r := validRendered()
	r.Quartermaster.Clusters[0].OwnerTenant = TenantRefAlias("ghost")
	err := r.Validate()
	if err == nil {
		t.Fatal("expected validation error for ghost owner_tenant ref")
	}
	if !strings.Contains(err.Error(), "owner_tenant") || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("error should mention owner_tenant and the bad value; got %v", err)
	}
}

func TestValidateRejectsClusterPricingForUnknownCluster(t *testing.T) {
	r := validRendered()
	r.Purser.ClusterPricing = append(r.Purser.ClusterPricing, ClusterPricing{
		ClusterID:    "ghost-cluster",
		PricingModel: "tiered",
	})
	err := r.Validate()
	if err == nil {
		t.Fatal("expected validation error for unknown cluster_id in pricing")
	}
	if !strings.Contains(err.Error(), "ghost-cluster") {
		t.Fatalf("error should mention the bad cluster_id; got %v", err)
	}
}

func TestValidateRejectsMultipleDefaultClusters(t *testing.T) {
	r := validRendered()
	r.Quartermaster.Clusters = append(r.Quartermaster.Clusters, Cluster{
		ID:          "second",
		Name:        "Second",
		Type:        "central",
		OwnerTenant: TenantRefSystem(),
		IsDefault:   true,
		Mesh:        ClusterMesh{CIDR: "10.99.32.0/20"},
	})
	err := r.Validate()
	if err == nil || !strings.Contains(err.Error(), "is_default") {
		t.Fatalf("expected multiple-default error; got %v", err)
	}
}

func TestValidateRejectsCustomerAccountWithBillingNone(t *testing.T) {
	r := validRendered()
	r.Quartermaster.Tenants = append(r.Quartermaster.Tenants, Tenant{Alias: "northwind", Name: "Northwind Traders"})
	r.Accounts = append(r.Accounts, AccountRendered{
		Kind:    AccountCustomer,
		Tenant:  TenantRef{Ref: "quartermaster.tenants[northwind]"},
		Users:   []AccountUserRendered{{AccountUserCommon: AccountUserCommon{Email: "admin@northwind.example", Role: "owner"}, Password: "pw"}},
		Billing: AccountBilling{Mode: "none"},
	})
	err := r.Validate()
	if err == nil || !strings.Contains(err.Error(), "customer accounts require billing") {
		t.Fatalf("expected customer-billing-required error; got %v", err)
	}
}

func TestValidateRejectsSystemOperatorWithBillingPrepaid(t *testing.T) {
	r := validRendered()
	r.Accounts = append(r.Accounts, AccountRendered{
		Kind:    AccountSystemOperator,
		Tenant:  TenantRef{Ref: "quartermaster.system_tenant"},
		Users:   []AccountUserRendered{{AccountUserCommon: AccountUserCommon{Email: "ops@example.com", Role: "owner"}, Password: "pw"}},
		Billing: AccountBilling{Mode: "prepaid"},
	})
	err := r.Validate()
	if err == nil || !strings.Contains(err.Error(), "system_operator accounts must have billing.model = none") {
		t.Fatalf("expected system_operator-billing-none error; got %v", err)
	}
}

// TestValidateRejectsInvalidAlias pins the alias charset contract: aliases must
// match ^[a-z][a-z0-9-]*$ and stay within MaxAliasLen.
func TestValidateRejectsInvalidAlias(t *testing.T) {
	cases := []string{
		"Northwind",      // uppercase
		"northwind corp", // whitespace
		"northwind/corp", // slash
		"northwind]corp", // bracket
		"1northwind",     // starts with digit
		"-northwind",     // starts with hyphen
		strings.Repeat("a", MaxAliasLen+1),
	}
	for _, alias := range cases {
		r := validRendered()
		r.Quartermaster.Tenants = []Tenant{{Alias: alias, Name: "X"}}
		err := r.Validate()
		if err == nil || !strings.Contains(err.Error(), "invalid alias") {
			t.Errorf("alias %q should be rejected; got %v", alias, err)
		}
	}
}

func TestValidateRejectsMalformedTenantRef(t *testing.T) {
	cases := []string{
		"quartermaster.tenants[northwind corp]", // whitespace inside ref
		"quartermaster.tenants[]",               // empty alias
		"quartermaster.something_else",          // wrong key
		"foo",                                   // not a ref at all
	}
	for _, ref := range cases {
		r := validRendered()
		r.Quartermaster.Clusters[0].OwnerTenant = TenantRef{Ref: ref}
		err := r.Validate()
		if err == nil || !strings.Contains(err.Error(), "malformed tenant ref") {
			t.Errorf("ref %q should be rejected as malformed; got %v", ref, err)
		}
	}
}

func TestValidateRejectsAccountWithUnknownTenantRef(t *testing.T) {
	r := validRendered()
	r.Accounts = append(r.Accounts, AccountRendered{
		Kind:    AccountCustomer,
		Tenant:  TenantRef{Ref: "quartermaster.tenants[ghost]"},
		Users:   []AccountUserRendered{{AccountUserCommon: AccountUserCommon{Email: "x@example.com", Role: "owner"}, Password: "pw"}},
		Billing: AccountBilling{Mode: "prepaid", Tier: "developer"},
	})
	err := r.Validate()
	if err == nil || !strings.Contains(err.Error(), "does not resolve") {
		t.Fatalf("expected unresolved tenant ref error; got %v", err)
	}
}

func TestValidateCollectsMultipleErrors(t *testing.T) {
	r := validRendered()
	r.Quartermaster.Clusters[0].OwnerTenant = TenantRefAlias("ghost")
	r.Quartermaster.Clusters[0].Type = ""
	r.Purser.ClusterPricing[0].PricingModel = ""

	err := r.Validate()
	if err == nil {
		t.Fatal("expected aggregated validation errors")
	}
	var errs ValidationErrors
	if !errors.As(err, &errs) {
		t.Fatalf("Validate should return ValidationErrors; got %T", err)
	}
	if len(errs) < 3 {
		t.Fatalf("expected at least 3 errors; got %d: %v", len(errs), errs)
	}
}
