//go:build schema_verify

package store

import (
	"context"
	"database/sql"
	"fmt"
	"os/exec"
	"testing"
	"time"

	fieldcrypt "github.com/Livepeer-FrameWorks/monorepo/pkg/crypto"
	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	_ "github.com/lib/pq"
)

func TestNavigatorStoreQueryPack_RealPG(t *testing.T) {
	db := startNavigatorStoreRealPG(t)
	enc, err := fieldcrypt.DeriveFieldEncryptor([]byte("navigator-real-postgres-contract-secret"), "navigator-store")
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore(db, enc)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	tenantID := "10000000-0000-0000-0000-000000000001"

	platformCert := &Certificate{Domain: "cdn.example.test", CertPEM: "platform-cert", KeyPEM: "platform-key", ExpiresAt: now.Add(24 * time.Hour)}
	if err := store.SaveCertificate(ctx, "", platformCert); err != nil {
		t.Fatalf("save platform certificate: %v", err)
	}
	tenantCert := &Certificate{Domain: "tenant.example.test", CertPEM: "tenant-cert", KeyPEM: "tenant-key", ExpiresAt: now.Add(12 * time.Hour), IssuerCA: "google-trust"}
	if err := store.SaveCertificate(ctx, tenantID, tenantCert); err != nil {
		t.Fatalf("save tenant certificate: %v", err)
	}
	gotCert, err := store.GetCertificate(ctx, tenantID, tenantCert.Domain)
	if err != nil || gotCert.KeyPEM != "tenant-key" || gotCert.IssuerCA != "google-trust" {
		t.Fatalf("tenant certificate = %#v, err = %v", gotCert, err)
	}
	var storedKey string
	if err := db.QueryRowContext(ctx, "SELECT key_pem FROM navigator.certificates WHERE id = $1::uuid", tenantCert.ID).Scan(&storedKey); err != nil {
		t.Fatal(err)
	}
	if storedKey == tenantCert.KeyPEM {
		t.Fatal("certificate private key was stored without field encryption")
	}
	if certs, err := store.ListExpiringCertificates(ctx, 18*time.Hour); err != nil || len(certs) != 1 || certs[0].Domain != tenantCert.Domain {
		t.Fatalf("expiring certificates = %#v, err = %v", certs, err)
	}
	if cert, err := store.GetCertificate(ctx, "", platformCert.Domain); err != nil || cert.KeyPEM != platformCert.KeyPEM {
		t.Fatalf("platform certificate = %#v, err = %v", cert, err)
	}
	if certs, err := store.ListCertificatesForTenant(ctx, ""); err != nil || len(certs) != 1 {
		t.Fatalf("platform certificates = %#v, err = %v", certs, err)
	}
	if certs, err := store.ListCertificatesForTenant(ctx, tenantID); err != nil || len(certs) != 1 {
		t.Fatalf("tenant certificates = %#v, err = %v", certs, err)
	}

	account := &ACMEAccount{Email: "ops@example.test", Registration: `{"uri":"account"}`, PrivateKeyPEM: "account-key", CA: "google-trust"}
	if err := store.SaveACMEAccount(ctx, tenantID, account); err != nil {
		t.Fatalf("save ACME account: %v", err)
	}
	gotAccount, err := store.GetACMEAccount(ctx, tenantID, account.Email, account.CA)
	if err != nil || gotAccount.PrivateKeyPEM != account.PrivateKeyPEM || gotAccount.CA != account.CA {
		t.Fatalf("ACME account = %#v, err = %v", gotAccount, err)
	}
	platformAccount := &ACMEAccount{Email: "platform@example.test", Registration: `{"uri":"platform"}`, PrivateKeyPEM: "platform-account-key"}
	if err := store.SaveACMEAccount(ctx, "", platformAccount); err != nil {
		t.Fatalf("save platform ACME account: %v", err)
	}
	if got, err := store.GetACMEAccount(ctx, "", platformAccount.Email, ""); err != nil || got.CA != "letsencrypt" || got.PrivateKeyPEM != platformAccount.PrivateKeyPEM {
		t.Fatalf("platform ACME account = %#v, err = %v", got, err)
	}

	bundle := &TLSBundle{BundleID: "tenant:" + tenantID, Domains: []string{"z.example.test", "a.example.test"}, CertPEM: "bundle-cert", KeyPEM: "bundle-key", ExpiresAt: now.Add(8 * time.Hour)}
	if err := store.SaveTLSBundle(ctx, bundle); err != nil {
		t.Fatalf("save TLS bundle: %v", err)
	}
	gotBundle, err := store.GetTLSBundle(ctx, bundle.BundleID)
	if err != nil || len(gotBundle.Domains) != 2 || gotBundle.Domains[0] != "a.example.test" || gotBundle.KeyPEM != bundle.KeyPEM {
		t.Fatalf("TLS bundle = %#v, err = %v", gotBundle, err)
	}
	if bundles, err := store.ListExpiringTLSBundles(ctx, 12*time.Hour); err != nil || len(bundles) != 1 {
		t.Fatalf("expiring TLS bundles = %#v, err = %v", bundles, err)
	}

	ca := &InternalCA{Role: "intermediate", CertPEM: "ca-cert", KeyPEM: "ca-key", ExpiresAt: now.Add(365 * 24 * time.Hour)}
	if err := store.SaveInternalCA(ctx, ca); err != nil {
		t.Fatalf("save internal CA: %v", err)
	}
	if gotCA, err := store.GetInternalCA(ctx, ca.Role); err != nil || gotCA.KeyPEM != ca.KeyPEM {
		t.Fatalf("internal CA = %#v, err = %v", gotCA, err)
	}
	rootCertOnly := &InternalCA{Role: "root_cert_only", CertPEM: "root-cert", ExpiresAt: now.Add(10 * 365 * 24 * time.Hour)}
	if err := store.SaveInternalCA(ctx, rootCertOnly); err != nil {
		t.Fatalf("save root cert-only CA: %v", err)
	}
	if got, err := store.GetInternalCA(ctx, rootCertOnly.Role); err != nil || got.KeyPEM != "" {
		t.Fatalf("root cert-only CA = %#v, err = %v", got, err)
	}
	internalCert := &InternalCertificate{NodeID: "node-1", ClusterID: "cluster-1", ServiceType: "helmsman", CertPEM: "internal-cert", KeyPEM: "internal-key", ExpiresAt: now.Add(24 * time.Hour)}
	if err := store.SaveInternalCertificate(ctx, internalCert); err != nil {
		t.Fatalf("save internal certificate: %v", err)
	}
	if got, err := store.GetInternalCertificate(ctx, internalCert.NodeID, internalCert.ServiceType); err != nil || got.KeyPEM != internalCert.KeyPEM {
		t.Fatalf("internal certificate = %#v, err = %v", got, err)
	}

	alias, err := store.EnsureTenantAlias(ctx, tenantID, "acme")
	if err != nil || alias.Status != "cert_issuing" {
		t.Fatalf("ensure alias = %#v, err = %v", alias, err)
	}
	if err := store.SetTenantAliasStatus(ctx, tenantID, "cert_issued", ""); err != nil {
		t.Fatal(err)
	}
	alias, err = store.EnsureTenantAlias(ctx, tenantID, "acme")
	if err != nil || alias.Status != "cert_issued" || !alias.CertIssuedAt.Valid {
		t.Fatalf("idempotent alias = %#v, err = %v", alias, err)
	}
	alias, err = store.EnsureTenantAlias(ctx, tenantID, "renamed")
	if err != nil || alias.Status != "cert_issuing" || alias.CertIssuedAt.Valid {
		t.Fatalf("renamed alias = %#v, err = %v", alias, err)
	}
	if got, err := store.GetTenantAlias(ctx, tenantID); err != nil || got.Subdomain != "renamed" {
		t.Fatalf("get tenant alias = %#v, err = %v", got, err)
	}
	if rows, err := store.ListPendingTenantAliases(ctx); err != nil || len(rows) != 1 {
		t.Fatalf("pending tenant aliases = %#v, err = %v", rows, err)
	}
	if rows, err := store.ListTenantAliasesByStatus(ctx, []string{"cert_issuing"}); err != nil || len(rows) != 1 {
		t.Fatalf("tenant aliases by status = %#v, err = %v", rows, err)
	}
	if err := store.UpsertTenantEdgeApplyState(ctx, &TenantEdgeApplyState{
		TenantID: tenantID, ClusterID: "cluster-1", NodeID: "node-1", BundleID: bundle.BundleID, State: "in_dns",
	}); err != nil {
		t.Fatal(err)
	}
	if hasDNS, err := store.TenantAliasHasDNS(ctx, tenantID); err != nil || !hasDNS {
		t.Fatalf("alias has DNS = %v, err = %v", hasDNS, err)
	}
	if rows, err := store.ListTenantEdgeApplyState(ctx, tenantID, "in_dns"); err != nil || len(rows) != 1 {
		t.Fatalf("filtered edge state = %#v, err = %v", rows, err)
	}
	if rows, err := store.ListTenantEdgeApplyState(ctx, tenantID, ""); err != nil || len(rows) != 1 {
		t.Fatalf("all edge state = %#v, err = %v", rows, err)
	}
	if err := store.InsertTenantAliasRetirement(ctx, tenantID, "retired"); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertTenantAliasRetirement(ctx, tenantID, "retired"); err != nil {
		t.Fatalf("replay tenant alias retirement: %v", err)
	}
	if err := store.RecordTenantAliasRetirementFailure(ctx, tenantID, "retired", "provider unavailable"); err != nil {
		t.Fatal(err)
	}
	if rows, err := store.ListTenantAliasRetirements(ctx); err != nil || len(rows) != 1 || rows[0].Attempts != 1 {
		t.Fatalf("tenant alias retirements = %#v, err = %v", rows, err)
	}
	if labels, err := store.ListTenantAliasRetirementLabels(ctx, tenantID); err != nil || len(labels) != 1 || labels[0] != "retired" {
		t.Fatalf("tenant alias retirement labels = %#v, err = %v", labels, err)
	}
	if err := store.DeleteTenantAliasRetirement(ctx, tenantID, "retired"); err != nil {
		t.Fatal(err)
	}

	domain, err := store.EnsureTenantCustomDomain(ctx, tenantID, "media.example.test", "challenge-one")
	if err != nil || domain.Status != "pending_verification" {
		t.Fatalf("ensure custom domain = %#v, err = %v", domain, err)
	}
	domain, err = store.EnsureTenantCustomDomain(ctx, tenantID, domain.Domain, "must-not-replace")
	if err != nil || domain.AcmeDNSSubdomain != "challenge-one" {
		t.Fatalf("replayed custom domain = %#v, err = %v", domain, err)
	}
	if err := store.SetTenantCustomDomainStatus(ctx, tenantID, domain.Domain, "verified", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTenantCustomDomainCertMetadata(ctx, tenantID, domain.Domain, "letsencrypt", sql.NullTime{Time: now.Add(90 * 24 * time.Hour), Valid: true}); err != nil {
		t.Fatal(err)
	}
	domain, err = store.GetTenantCustomDomain(ctx, tenantID, domain.Domain)
	if err != nil || !domain.LastVerifiedAt.Valid || domain.IssuerID.String != "letsencrypt" || !domain.CertExpiresAt.Valid {
		t.Fatalf("custom domain metadata = %#v, err = %v", domain, err)
	}
	if rows, err := store.ListTenantCustomDomains(ctx, tenantID); err != nil || len(rows) != 1 {
		t.Fatalf("tenant custom domains = %#v, err = %v", rows, err)
	}
	if rows, err := store.ListTenantCustomDomainsByStatus(ctx, []string{"verified"}); err != nil || len(rows) != 1 {
		t.Fatalf("tenant custom domains by status = %#v, err = %v", rows, err)
	}
	if err := store.SetTenantCustomDomainStatus(ctx, tenantID, "missing.example.test", "verified", ""); err != ErrNotFound {
		t.Fatalf("missing custom domain error = %v, want ErrNotFound", err)
	}
	if err := store.DeleteTenantCustomDomain(ctx, tenantID, domain.Domain); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteTenantEdgeApplyStateForCluster(ctx, tenantID, "cluster-1"); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertTenantEdgeApplyState(ctx, &TenantEdgeApplyState{
		TenantID: tenantID, ClusterID: "cluster-2", NodeID: "node-2", BundleID: bundle.BundleID, State: "applied",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteTenantEdgeApplyState(ctx, tenantID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteTenantAlias(ctx, tenantID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteCertificate(ctx, tenantID, tenantCert.Domain); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteCertificate(ctx, "", platformCert.Domain); err != nil {
		t.Fatal(err)
	}
}

func startNavigatorStoreRealPG(t *testing.T) *sql.DB {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-navigator-store-realpg-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = dockerpg.CLI("rm", "-fv", name) })
	image, err := dockerpg.PostgresImage()
	if err != nil {
		t.Fatal(err)
	}
	if output, err := dockerpg.Run("run", "-d", "--name", name, "-P", "-e", "POSTGRES_PASSWORD=harness", image); err != nil {
		t.Fatalf("docker run: %v\n%s", err, output)
	}
	port, err := dockerpg.DiscoverPublishedHostPort(name, "5432/tcp")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", fmt.Sprintf("postgres://postgres:harness@127.0.0.1:%s/postgres?sslmode=disable", port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := dockerpg.WaitReady(db, name); err != nil {
		t.Fatal(err)
	}
	schema, err := dbsql.Content.ReadFile("schema/navigator.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	return db
}
