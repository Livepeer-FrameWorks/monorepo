//go:build schema_verify

package quartermasterdb

import (
	"testing"
)

func TestTenantControlRecipientsCanonicalizeWithoutAdmission_RealPG(t *testing.T) {
	db := startQuartermasterQueryCatalogRealPG(t)
	ctx := t.Context()
	const owner = "11111111-1111-4111-8111-111111111191"
	const foreign = "11111111-1111-4111-8111-111111111192"
	if _, err := db.ExecContext(ctx, `INSERT INTO quartermaster.tenants(id,name) VALUES($1,'control inventory'),($2,'foreign inventory')`, owner, foreign); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO quartermaster.infrastructure_clusters(cluster_id,cluster_name,cluster_type,cluster_class,base_url,cell_id,control_cell_id,health_status,is_active)
 VALUES ('inventory-eu','EU','edge','platform_official','example.test','inventory-eu','inventory-eu','unhealthy',true),
 ('inventory-us','US','edge','platform_official','example.test','inventory-us','inventory-us','healthy',true),
 ('inventory-private-a','Private A','edge','tenant_private','example.test','inventory-private-a','inventory-us','unknown',true),
 ('inventory-private-b','Private B','edge','tenant_private','example.test','inventory-private-b','inventory-us','unknown',false),
 ('inventory-unresolved','Unresolved','edge','tenant_private','example.test','inventory-unresolved',NULL,'unknown',true),
 ('inventory-central','Central','central','platform_official','example.test','inventory-central','inventory-central','healthy',true),
 ('inventory-foreign','Foreign','edge','platform_official','example.test','inventory-foreign','inventory-foreign','healthy',true)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO quartermaster.tenant_cluster_access(tenant_id,cluster_id,access_source,is_active,subscription_status)
 SELECT $1::uuid,cluster_id,'operator_override',false,'suspended' FROM quartermaster.infrastructure_clusters
 WHERE cluster_id IN ('inventory-eu','inventory-private-a','inventory-private-b','inventory-unresolved','inventory-central')`, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO quartermaster.tenant_cluster_access(tenant_id,cluster_id,access_source) VALUES($1,'inventory-foreign','operator_override')`, foreign); err != nil {
		t.Fatal(err)
	}
	rows, err := New(db).ListTenantControlRecipients(ctx, owner, []string{"retired-unknown", "inventory-central"})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, row := range rows {
		got[row.ClusterID] = row.CellID
	}
	for cluster, want := range map[string]string{"inventory-eu": "inventory-eu", "inventory-private-a": "inventory-us", "inventory-private-b": "inventory-us", "inventory-unresolved": "", "retired-unknown": ""} {
		actual, found := got[cluster]
		if !found || actual != want {
			t.Fatalf("control recipient %s=%q found=%v want=%q", cluster, actual, found, want)
		}
	}
	if len(got) != 5 {
		t.Fatalf("central/foreign recipients leaked: %v", got)
	}
	empty, err := New(db).ListTenantControlRecipients(ctx, "11111111-1111-4111-8111-111111111193", nil)
	if err != nil || len(empty) != 0 {
		t.Fatalf("authoritative empty inventory=%v err=%v", empty, err)
	}
}
