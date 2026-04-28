package bootstrap

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func sysTenant() *Tenant {
	return &Tenant{Alias: SystemTenantAlias, Name: "FrameWorks", DeploymentTier: "global", PrimaryColor: "#000", SecondaryColor: "#fff"}
}

func TestReconcileTenantsRejectsNilExecutor(t *testing.T) {
	if _, _, err := ReconcileTenants(context.Background(), nil, sysTenant(), nil); err == nil {
		t.Fatal("expected error on nil executor")
	}
}

func TestReconcileTenantsRejectsCustomerWithSystemAlias(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT alias, tenant_id::text FROM quartermaster.bootstrap_tenant_aliases")).
		WillReturnRows(sqlmock.NewRows([]string{"alias", "tenant_id"}))
	customers := []Tenant{{Alias: SystemTenantAlias, Name: "Bad"}}
	if _, _, err := ReconcileTenants(context.Background(), db, nil, customers); err == nil {
		t.Fatal("expected error: customer alias collides with system alias")
	}
}

func TestReconcileTenantsRejectsBadAlias(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT alias, tenant_id::text FROM quartermaster.bootstrap_tenant_aliases")).
		WillReturnRows(sqlmock.NewRows([]string{"alias", "tenant_id"}))
	customers := []Tenant{{Alias: "UPPER", Name: "Whatever"}}
	if _, _, err := ReconcileTenants(context.Background(), db, nil, customers); err == nil {
		t.Fatal("expected error on invalid alias")
	}
}

func TestReconcileTenantsCreatesSystemAndCustomer(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(regexp.QuoteMeta("SELECT alias, tenant_id::text FROM quartermaster.bootstrap_tenant_aliases")).
		WillReturnRows(sqlmock.NewRows([]string{"alias", "tenant_id"}))

	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO quartermaster.tenants")).
		WithArgs("FrameWorks", "global", "#000", "#fff").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("uuid-system"))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO quartermaster.bootstrap_tenant_aliases")).
		WithArgs(SystemTenantAlias, "uuid-system").
		WillReturnResult(sqlmock.NewResult(0, 1))

	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO quartermaster.tenants")).
		WithArgs("Acme", "global", "#6366f1", "#f59e0b").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("uuid-acme"))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO quartermaster.bootstrap_tenant_aliases")).
		WithArgs("acme", "uuid-acme").
		WillReturnResult(sqlmock.NewResult(0, 1))

	aliases, res, err := ReconcileTenants(context.Background(), db, sysTenant(),
		[]Tenant{{Alias: "acme", Name: "Acme"}})
	if err != nil {
		t.Fatalf("ReconcileTenants: %v", err)
	}
	if len(res.Created) != 2 {
		t.Errorf("created count = %d, want 2 (got %v)", len(res.Created), res.Created)
	}
	if id, _ := aliases.LookupAlias("acme"); id != "uuid-acme" {
		t.Errorf("alias map missing customer entry: got %q", id)
	}
	if id, _ := aliases.LookupAlias(SystemTenantAlias); id != "uuid-system" {
		t.Errorf("alias map missing system entry: got %q", id)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestReconcileTenantsNoopOnUnchanged(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(regexp.QuoteMeta("SELECT alias, tenant_id::text FROM quartermaster.bootstrap_tenant_aliases")).
		WillReturnRows(sqlmock.NewRows([]string{"alias", "tenant_id"}).AddRow(SystemTenantAlias, "uuid-system"))
	mock.ExpectQuery(regexp.QuoteMeta("FROM quartermaster.tenants WHERE id = $1::uuid")).
		WithArgs("uuid-system").
		WillReturnRows(sqlmock.NewRows([]string{"name", "deployment_tier", "primary_color", "secondary_color"}).
			AddRow("FrameWorks", "global", "#000", "#fff"))

	_, res, err := ReconcileTenants(context.Background(), db, sysTenant(), nil)
	if err != nil {
		t.Fatalf("ReconcileTenants: %v", err)
	}
	if len(res.Noop) != 1 {
		t.Errorf("expected 1 noop, got %v", res)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}
