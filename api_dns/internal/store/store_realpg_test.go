//go:build schema_verify

package store

import (
	"context"
	"database/sql"
	"errors"
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
	verifyNavigatorStoreQueryPack(t, startNavigatorStoreRealPG(t))
}

func TestNavigatorStoreQueryPack_RealYugabyte(t *testing.T) {
	verifyNavigatorStoreQueryPack(t, startNavigatorStoreRealYugabyte(t))
}

func verifyNavigatorStoreQueryPack(t *testing.T, db *sql.DB) {
	t.Helper()
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
	authorityDomain, err := store.EnsureTenantCustomDomain(ctx, tenantID, tenantCert.Domain, "certificate-authority")
	if err != nil {
		t.Fatal(err)
	}
	if transitioned, err := store.SetTenantCustomDomainStatus(ctx, tenantID, authorityDomain.Domain, authorityDomain.Status, "cert_issuing", ""); err != nil || !transitioned {
		t.Fatalf("authorize tenant certificate transitioned=%v err=%v", transitioned, err)
	}
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
	if transitioned, err := store.SetTenantCustomDomainStatus(ctx, tenantID, authorityDomain.Domain, "", "tearing_down", ""); err != nil || !transitioned {
		t.Fatalf("mark authority domain teardown transitioned=%v err=%v", transitioned, err)
	}
	if finalized, err := store.FinalizeTenantCustomDomainRemoval(ctx, tenantID, authorityDomain.Domain); err != nil || !finalized {
		t.Fatalf("finalize authority domain finalized=%v err=%v", finalized, err)
	}
	if _, err := store.GetACMEAccount(ctx, tenantID, account.Email, account.CA); err != ErrNotFound {
		t.Fatalf("last custom-domain ACME account survived teardown: %v", err)
	}
	if err := store.SaveACMEAccount(ctx, tenantID, account); !errors.Is(err, ErrAuthorityLost) {
		t.Fatalf("ACME account write without custom-domain authority = %v, want ErrAuthorityLost", err)
	}
	if err := store.SaveCertificate(ctx, tenantID, tenantCert); !errors.Is(err, ErrAuthorityLost) {
		t.Fatalf("certificate write without custom-domain authority = %v, want ErrAuthorityLost", err)
	}

	activeAccountDomain, err := store.EnsureTenantCustomDomain(ctx, tenantID, "active-account.example.test", "account-active")
	if err != nil {
		t.Fatal(err)
	}
	firstRetiringDomain, err := store.EnsureTenantCustomDomain(ctx, tenantID, "first-retiring.example.test", "account-retiring-one")
	if err != nil {
		t.Fatal(err)
	}
	secondRetiringDomain, err := store.EnsureTenantCustomDomain(ctx, tenantID, "second-retiring.example.test", "account-retiring-two")
	if err != nil {
		t.Fatal(err)
	}
	for _, customDomain := range []*TenantCustomDomain{activeAccountDomain, firstRetiringDomain, secondRetiringDomain} {
		if transitioned, transitionErr := store.SetTenantCustomDomainStatus(ctx, tenantID, customDomain.Domain, "pending_verification", "verified", ""); transitionErr != nil || !transitioned {
			t.Fatalf("authorize custom domain %s transitioned=%v err=%v", customDomain.Domain, transitioned, transitionErr)
		}
	}
	if err := store.SaveACMEAccount(ctx, tenantID, account); err != nil {
		t.Fatalf("restore tenant ACME account: %v", err)
	}
	for _, customDomain := range []*TenantCustomDomain{firstRetiringDomain, secondRetiringDomain} {
		if transitioned, transitionErr := store.SetTenantCustomDomainStatus(ctx, tenantID, customDomain.Domain, "", "tearing_down", ""); transitionErr != nil || !transitioned {
			t.Fatalf("mark custom domain %s tearing down transitioned=%v err=%v", customDomain.Domain, transitioned, transitionErr)
		}
	}
	if finalized, finalizeErr := store.FinalizeTenantCustomDomainRemoval(ctx, tenantID, firstRetiringDomain.Domain); finalizeErr != nil || !finalized {
		t.Fatalf("finalize first retiring domain finalized=%v err=%v", finalized, finalizeErr)
	}
	if _, err := store.GetACMEAccount(ctx, tenantID, account.Email, account.CA); err != nil {
		t.Fatalf("active custom-domain authority did not preserve tenant ACME account: %v", err)
	}
	if transitioned, transitionErr := store.SetTenantCustomDomainStatus(ctx, tenantID, activeAccountDomain.Domain, "", "tearing_down", ""); transitionErr != nil || !transitioned {
		t.Fatalf("mark final active custom domain tearing down transitioned=%v err=%v", transitioned, transitionErr)
	}
	if finalized, finalizeErr := store.FinalizeTenantCustomDomainRemoval(ctx, tenantID, activeAccountDomain.Domain); finalizeErr != nil || !finalized {
		t.Fatalf("finalize last active domain finalized=%v err=%v", finalized, finalizeErr)
	}
	if _, err := store.GetACMEAccount(ctx, tenantID, account.Email, account.CA); err != ErrNotFound {
		t.Fatalf("tenant ACME account survived with only tearing-down custom domains: %v", err)
	}
	if finalized, finalizeErr := store.FinalizeTenantCustomDomainRemoval(ctx, tenantID, secondRetiringDomain.Domain); finalizeErr != nil || !finalized {
		t.Fatalf("finalize remaining teardown domain finalized=%v err=%v", finalized, finalizeErr)
	}

	bundleDomain, err := store.EnsureTenantCustomDomain(ctx, tenantID, "bundle-custom.example.test", "bundle-challenge")
	if err != nil {
		t.Fatal(err)
	}
	if transitioned, transitionErr := store.SetTenantCustomDomainStatus(ctx, tenantID, bundleDomain.Domain, bundleDomain.Status, "cert_issuing", ""); transitionErr != nil || !transitioned {
		t.Fatalf("authorize bundle custom domain transitioned=%v err=%v", transitioned, transitionErr)
	}
	alias, err := store.EnsureTenantAlias(ctx, tenantID, "acme")
	if err != nil || alias.Status != "cert_issuing" {
		t.Fatalf("ensure alias = %#v, err = %v", alias, err)
	}
	bundle := &TLSBundle{BundleID: "tenant:" + tenantID, Domains: []string{"acme.example.test", "*.acme.example.test", bundleDomain.Domain}, CertPEM: "bundle-cert", KeyPEM: "bundle-key", ExpiresAt: now.Add(8 * time.Hour), IssuerCA: "google-trust", AuthoritySubdomain: "acme", AuthorityVersion: alias.AuthorityVersion}
	if err := store.SaveTLSBundle(ctx, bundle); err != nil {
		t.Fatalf("save TLS bundle: %v", err)
	}
	if got, err := store.GetTLSBundleForIssuance(ctx, bundle.BundleID); err != nil || got.Version == "" {
		t.Fatalf("issuance cache bundle = %#v, err = %v", got, err)
	}
	// SaveTLSBundle publishes the bundle and cert_issued transition atomically,
	// closing the crash window between persistence and lifecycle completion.
	if transitioned, err := store.SetTenantAliasStatus(ctx, tenantID, "acme", alias.AuthorityVersion, "cert_issued", ""); err != nil || !transitioned {
		t.Fatalf("mark alias certificate issued transitioned=%v err=%v", transitioned, err)
	}
	gotBundle, err := store.GetTLSBundle(ctx, bundle.BundleID)
	if err != nil || len(gotBundle.Domains) != 3 || gotBundle.Domains[0] != "*.acme.example.test" || gotBundle.KeyPEM != bundle.KeyPEM {
		t.Fatalf("TLS bundle = %#v, err = %v", gotBundle, err)
	}
	bundleDomain, err = store.GetTenantCustomDomain(ctx, tenantID, bundleDomain.Domain)
	if err != nil || bundleDomain.IssuerID.String != bundle.IssuerCA || !bundleDomain.CertExpiresAt.Valid || !bundleDomain.CertExpiresAt.Time.Equal(bundle.ExpiresAt) {
		t.Fatalf("bundle save did not atomically update custom-domain metadata: domain=%#v err=%v", bundleDomain, err)
	}
	if bundles, err := store.ListExpiringTLSBundles(ctx, 12*time.Hour); err != nil || len(bundles) != 1 {
		t.Fatalf("expiring TLS bundles = %#v, err = %v", bundles, err)
	}
	if acquired, err := store.TryAcquireCertificateIssuanceLease(ctx, "tls-bundle:"+bundle.BundleID, "replica-a", time.Minute); err != nil || !acquired {
		t.Fatalf("replica-a issuance lease acquired=%v err=%v", acquired, err)
	}
	if renewed, err := store.RenewCertificateIssuanceLease(ctx, "tls-bundle:"+bundle.BundleID, "replica-a", 2*time.Minute); err != nil || !renewed {
		t.Fatalf("replica-a issuance lease renewed=%v err=%v", renewed, err)
	}
	if renewed, err := store.RenewCertificateIssuanceLease(ctx, "tls-bundle:"+bundle.BundleID, "replica-b", 2*time.Minute); err != nil || renewed {
		t.Fatalf("replica-b renewed another owner's issuance lease renewed=%v err=%v", renewed, err)
	}
	if acquired, err := store.TryAcquireCertificateIssuanceLease(ctx, "tls-bundle:"+bundle.BundleID, "replica-b", time.Minute); err != nil || acquired {
		t.Fatalf("replica-b crossed live issuance lease acquired=%v err=%v", acquired, err)
	}
	if err := store.ReleaseCertificateIssuanceLease(ctx, "tls-bundle:"+bundle.BundleID, "replica-b"); err != nil {
		t.Fatal(err)
	}
	if acquired, err := store.TryAcquireCertificateIssuanceLease(ctx, "tls-bundle:"+bundle.BundleID, "replica-b", time.Minute); err != nil || acquired {
		t.Fatalf("wrong-owner release removed lease acquired=%v err=%v", acquired, err)
	}
	if err := store.ReleaseCertificateIssuanceLease(ctx, "tls-bundle:"+bundle.BundleID, "replica-a"); err != nil {
		t.Fatal(err)
	}
	if acquired, err := store.TryAcquireCertificateIssuanceLease(ctx, "tls-bundle:"+bundle.BundleID, "replica-b", time.Minute); err != nil || !acquired {
		t.Fatalf("replica-b did not acquire released lease acquired=%v err=%v", acquired, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE navigator.certificate_issuance_leases SET lease_until = NOW() - INTERVAL '1 second' WHERE lease_key = $1`, "tls-bundle:"+bundle.BundleID); err != nil {
		t.Fatal(err)
	}
	if renewed, err := store.RenewCertificateIssuanceLease(ctx, "tls-bundle:"+bundle.BundleID, "replica-b", time.Minute); err != nil || renewed {
		t.Fatalf("expired issuance lease renewed=%v err=%v", renewed, err)
	}
	if deleted, err := store.DeleteExpiredCertificateIssuanceLeases(ctx); err != nil || deleted != 1 {
		t.Fatalf("expired issuance lease cleanup deleted=%d err=%v", deleted, err)
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

	alias, err = store.EnsureTenantAlias(ctx, tenantID, "acme")
	if err != nil || alias.Status != "cert_issued" || !alias.CertIssuedAt.Valid {
		t.Fatalf("idempotent alias = %#v, err = %v", alias, err)
	}
	if transitioned, err := store.SetTenantAliasStatus(ctx, tenantID, "", 0, "tearing_down", "retiring"); err != nil || !transitioned {
		t.Fatal(err)
	}
	if transitioned, err := store.SetTenantAliasStatus(ctx, tenantID, "acme", alias.AuthorityVersion, "cert_issued", ""); err != nil || transitioned {
		t.Fatalf("late issuance crossed teardown fence transitioned=%v err=%v", transitioned, err)
	}
	alias, err = store.EnsureTenantAlias(ctx, tenantID, "acme")
	if err != nil || alias.Status != "cert_issuing" || alias.CertIssuedAt.Valid || alias.LastError.Valid {
		t.Fatalf("same-label reactivated alias = %#v, err = %v", alias, err)
	}
	if alias.AuthorityVersion <= bundle.AuthorityVersion {
		t.Fatalf("same-label reactivation authority version=%d, want > %d", alias.AuthorityVersion, bundle.AuthorityVersion)
	}
	staleSameLabelBundle := *bundle
	staleSameLabelBundle.CertPEM = "stale-pre-reactivation-cert"
	if err := store.SaveTLSBundle(ctx, &staleSameLabelBundle); !errors.Is(err, ErrAuthorityLost) {
		t.Fatalf("pre-reactivation bundle crossed same-label ABA fence: %v", err)
	}
	if transitioned, err := store.SetTenantAliasStatus(ctx, tenantID, "acme", bundle.AuthorityVersion, "cert_failed", "stale issuance"); err != nil || transitioned {
		t.Fatalf("pre-reactivation failure crossed same-label ABA fence transitioned=%v err=%v", transitioned, err)
	}
	bundle.AuthorityVersion = alias.AuthorityVersion
	if deleted, err := store.DeleteTenantAlias(ctx, tenantID); err != nil || deleted {
		t.Fatalf("stale teardown after reactivation deleted=%v err=%v", deleted, err)
	}
	if transitioned, err := store.SetTenantAliasStatus(ctx, tenantID, "acme", alias.AuthorityVersion, "cert_issued", ""); err != nil || !transitioned {
		t.Fatal(err)
	}
	if applied, err := store.GrantTenantAliasClusterAuthority(ctx, tenantID, "cluster-1", 10); err != nil || !applied {
		t.Fatalf("grant cluster-1 authority applied=%v err=%v", applied, err)
	}
	oldBundleVersion := bundle.Version
	preRenameEdge := &TenantEdgeApplyState{
		TenantID: tenantID, ClusterID: "cluster-1", NodeID: "node-1", BundleID: bundle.BundleID,
		BundleVersion: oldBundleVersion, State: "applied", LastSeedVersion: sql.NullInt64{Valid: true, Int64: 1},
	}
	if outcome, err := store.UpsertTenantEdgeApplyAck(ctx, preRenameEdge); err != nil || outcome != TenantEdgeApplyAckAccepted {
		t.Fatalf("seed pre-rename edge outcome=%q err=%v", outcome, err)
	}
	if advanced, err := store.MarkTenantEdgeInDNS(ctx, preRenameEdge); err != nil || !advanced {
		t.Fatalf("publish pre-rename edge advanced=%v err=%v", advanced, err)
	}
	alias, err = store.EnsureTenantAlias(ctx, tenantID, "renamed")
	if err != nil || alias.Status != "cert_issuing" || alias.CertIssuedAt.Valid {
		t.Fatalf("renamed alias = %#v, err = %v", alias, err)
	}
	if got, err := store.GetTLSBundle(ctx, bundle.BundleID); err != nil || got.Domains[0] != "*.acme.example.test" {
		t.Fatalf("last-good TLS bundle was not preserved during alias re-issuance: bundle=%#v err=%v", got, err)
	}
	if transitioned, err := store.SetTenantAliasStatus(ctx, tenantID, "acme", alias.AuthorityVersion, "cert_issued", ""); err != nil || transitioned {
		t.Fatalf("old-label issuance crossed rename fence transitioned=%v err=%v", transitioned, err)
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
	if rows, err := store.ListTenantEdgeApplyState(ctx, tenantID, ""); err != nil || len(rows) != 1 || rows[0].State != "pending_distribute" || rows[0].InDNSAt.Valid {
		t.Fatalf("alias rename did not invalidate prior edge authority: rows=%#v err=%v", rows, err)
	}
	renamedBundle := *bundle
	renamedBundle.Domains = []string{"renamed.example.test", "*.renamed.example.test"}
	renamedBundle.CertPEM = "renamed-bundle-cert"
	renamedBundle.KeyPEM = "renamed-bundle-key"
	renamedBundle.Version = ""
	renamedBundle.AuthoritySubdomain = "renamed"
	renamedBundle.AuthorityVersion = alias.AuthorityVersion
	if err := store.SaveTLSBundle(ctx, &renamedBundle); err != nil {
		t.Fatalf("save renamed TLS bundle: %v", err)
	}
	bundle = &renamedBundle
	lateOldBundleACK := *preRenameEdge
	lateOldBundleACK.LastSeedVersion = sql.NullInt64{Valid: true, Int64: 999}
	lateOldBundleACK.LastDeliverySequence = 999
	if outcome, err := store.UpsertTenantEdgeApplyAck(ctx, &lateOldBundleACK); err != nil || outcome != TenantEdgeApplyAckStale {
		t.Fatalf("old bundle ACK after rename outcome=%q err=%v, want stale", outcome, err)
	}
	if hasDNS, err := store.TenantAliasHasDNS(ctx, tenantID); err != nil || hasDNS {
		t.Fatalf("old bundle ACK made replacement DNS-ready=%v err=%v", hasDNS, err)
	}
	initialEdge := &TenantEdgeApplyState{
		TenantID: tenantID, ClusterID: "cluster-1", NodeID: "node-1", BundleID: bundle.BundleID, BundleVersion: bundle.Version,
		State: "in_dns", LastSeedVersion: sql.NullInt64{Valid: true, Int64: 2}, LastDeliverySequence: 0,
	}
	initialEdge.State = "applied"
	if outcome, err := store.UpsertTenantEdgeApplyAck(ctx, initialEdge); err != nil || outcome != TenantEdgeApplyAckAccepted {
		t.Fatal(err)
	}
	if advanced, err := store.MarkTenantEdgeInDNS(ctx, initialEdge); err != nil || !advanced {
		t.Fatalf("mark initial edge in DNS: advanced=%v err=%v", advanced, err)
	}
	if hasDNS, err := store.TenantAliasHasDNS(ctx, tenantID); err != nil || !hasDNS {
		t.Fatalf("alias has DNS = %v, err = %v", hasDNS, err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE navigator.tenant_edge_apply_state
		SET bundle_version = ''
		WHERE tenant_id = $1::uuid AND node_id = $2
	`, tenantID, initialEdge.NodeID); err != nil {
		t.Fatal(err)
	}
	if hasDNS, err := store.TenantAliasHasDNS(ctx, tenantID); err != nil || hasDNS {
		t.Fatalf("grandfathered versionless DNS continuity reported replacement readiness=%v err=%v", hasDNS, err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE navigator.tenant_edge_apply_state
		SET bundle_version = $1
		WHERE tenant_id = $2::uuid AND node_id = $3
	`, bundle.Version, tenantID, initialEdge.NodeID); err != nil {
		t.Fatal(err)
	}
	wrongAuthorityBundle := &TLSBundle{
		BundleID: "platform-wrong-authority", Domains: []string{"platform.example.test"},
		CertPEM: "platform-wrong-cert", KeyPEM: "platform-wrong-key", ExpiresAt: now.Add(24 * time.Hour),
	}
	if err := store.SaveTLSBundle(ctx, wrongAuthorityBundle); err != nil {
		t.Fatalf("save wrong-authority bundle: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE navigator.tenant_edge_apply_state
		SET bundle_id = $1, bundle_version = $2
		WHERE tenant_id = $3::uuid AND node_id = $4
	`, wrongAuthorityBundle.BundleID, wrongAuthorityBundle.Version, tenantID, initialEdge.NodeID); err != nil {
		t.Fatalf("replace edge bundle identity: %v", err)
	}
	if hasDNS, err := store.TenantAliasHasDNS(ctx, tenantID); err != nil || hasDNS {
		t.Fatalf("foreign bundle identity made tenant DNS-ready=%v err=%v", hasDNS, err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE navigator.tenant_edge_apply_state
		SET bundle_id = $1, bundle_version = $2
		WHERE tenant_id = $3::uuid AND node_id = $4
	`, bundle.BundleID, bundle.Version, tenantID, initialEdge.NodeID); err != nil {
		t.Fatalf("restore tenant bundle identity: %v", err)
	}
	if rows, err := store.ListTenantEdgeApplyState(ctx, tenantID, "in_dns"); err != nil || len(rows) != 1 {
		t.Fatalf("filtered edge state = %#v, err = %v", rows, err)
	}
	if rows, err := store.ListTenantEdgeApplyState(ctx, tenantID, ""); err != nil || len(rows) != 1 {
		t.Fatalf("all edge state = %#v, err = %v", rows, err)
	}
	if state, err := store.TenantAliasClusterAuthorityState(ctx, tenantID, "cluster-1"); err != nil || state != "active" {
		t.Fatalf("established tenant-cluster authority state=%q err=%v", state, err)
	}
	seedVersion := sql.NullInt64{Valid: true, Int64: 7}
	ackAt := sql.NullTime{Valid: true, Time: now}
	ack := &TenantEdgeApplyState{
		TenantID: tenantID, ClusterID: "cluster-1", NodeID: "node-1", BundleID: bundle.BundleID,
		BundleVersion: bundle.Version, State: "applied", LastSeedVersion: seedVersion, LastAckAt: ackAt,
	}
	if outcome, err := store.UpsertTenantEdgeApplyAck(ctx, ack); err != nil || outcome != TenantEdgeApplyAckAccepted {
		t.Fatal(err)
	}
	publishedRows, err := store.ListTenantEdgeApplyState(ctx, tenantID, "")
	if err != nil || len(publishedRows) != 1 {
		t.Fatalf("reload accepted ACK snapshot: rows=%#v err=%v", publishedRows, err)
	}
	published := publishedRows[0]
	if advanced, err := store.MarkTenantEdgeInDNS(ctx, &published); err != nil || !advanced {
		t.Fatalf("mark ACK snapshot in DNS: advanced=%v err=%v", advanced, err)
	}
	if outcome, err := store.UpsertTenantEdgeApplyAck(ctx, ack); err != nil || outcome != TenantEdgeApplyAckStale {
		t.Fatal(err)
	}
	rows, err := store.ListTenantEdgeApplyState(ctx, tenantID, "")
	if err != nil || len(rows) != 1 || rows[0].State != "in_dns" || !rows[0].InDNSAt.Valid {
		t.Fatalf("same-version ACK replay regressed DNS state: rows=%#v err=%v", rows, err)
	}
	failed := *ack
	failed.State = "pending_apply"
	failed.LastAckAt = sql.NullTime{Valid: true, Time: now.Add(2 * time.Second)}
	if outcome, err := store.UpsertTenantEdgeApplyAck(ctx, &failed); err != nil || outcome != TenantEdgeApplyAckAccepted {
		t.Fatal(err)
	}
	rows, err = store.ListTenantEdgeApplyState(ctx, tenantID, "")
	if err != nil || len(rows) != 1 || rows[0].State != "pending_apply" || rows[0].InDNSAt.Valid {
		t.Fatalf("same-version failed ACK did not demote DNS state: rows=%#v err=%v", rows, err)
	}
	hasDNS, err := store.TenantAliasHasDNS(ctx, tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if hasDNS {
		t.Fatal("same-version failed ACK left tenant DNS-eligible")
	}
	invalid := *ack
	invalid.State = "acknowledged"
	if _, err := store.UpsertTenantEdgeApplyAck(ctx, &invalid); err == nil {
		t.Fatal("tenant edge apply state accepted an illegal lifecycle value")
	}
	recovered := *ack
	recovered.LastAckAt = sql.NullTime{Valid: true, Time: now.Add(3 * time.Second)}
	if outcome, err := store.UpsertTenantEdgeApplyAck(ctx, &recovered); err != nil || outcome != TenantEdgeApplyAckAccepted {
		t.Fatalf("same-version recovery outcome=%q err=%v", outcome, err)
	}
	rows, err = store.ListTenantEdgeApplyState(ctx, tenantID, "")
	if err != nil || len(rows) != 1 || rows[0].State != "applied" || rows[0].InDNSAt.Valid {
		t.Fatalf("same-version recovery did not restore applied state: rows=%#v err=%v", rows, err)
	}
	olderFailure := failed
	olderFailure.LastSeedVersion = sql.NullInt64{Valid: true, Int64: 6}
	if outcome, err := store.UpsertTenantEdgeApplyAck(ctx, &olderFailure); err != nil || outcome != TenantEdgeApplyAckStale {
		t.Fatalf("older failed ACK outcome=%q err=%v", outcome, err)
	}
	newerFailure := failed
	newerFailure.LastSeedVersion = sql.NullInt64{Valid: true, Int64: 8}
	if outcome, err := store.UpsertTenantEdgeApplyAck(ctx, &newerFailure); err != nil || outcome != TenantEdgeApplyAckAccepted {
		t.Fatalf("newer failed ACK outcome=%q err=%v", outcome, err)
	}
	if advanced, err := store.MarkTenantEdgeInDNS(ctx, &recovered); err != nil || advanced {
		t.Fatalf("stale DNS write-back advanced=%v err=%v", advanced, err)
	}
	rows, err = store.ListTenantEdgeApplyState(ctx, tenantID, "")
	if err != nil || len(rows) != 1 || rows[0].State != "pending_apply" || rows[0].LastSeedVersion.Int64 != 8 {
		t.Fatalf("stale DNS write-back replayed an older ACK snapshot: rows=%#v err=%v", rows, err)
	}
	newerRecovery := recovered
	newerRecovery.LastSeedVersion = sql.NullInt64{Valid: true, Int64: 9}
	newerRecovery.LastDeliverySequence = 100
	if outcome, err := store.UpsertTenantEdgeApplyAck(ctx, &newerRecovery); err != nil || outcome != TenantEdgeApplyAckAccepted {
		t.Fatalf("newer recovered ACK outcome=%q err=%v", outcome, err)
	}
	newerPublished := newerRecovery
	if advanced, err := store.MarkTenantEdgeInDNS(ctx, &newerPublished); err != nil || !advanced {
		t.Fatalf("mark newer ACK snapshot in DNS: advanced=%v err=%v", advanced, err)
	}
	sequencedReplay := newerRecovery
	sequencedReplay.LastDeliverySequence = 101
	sequencedReplay.LastAckAt = sql.NullTime{Valid: true, Time: now.Add(5 * time.Second)}
	if outcome, err := store.UpsertTenantEdgeApplyAck(ctx, &sequencedReplay); err != nil || outcome != TenantEdgeApplyAckAccepted {
		t.Fatalf("sequenced in-DNS replay outcome=%q err=%v", outcome, err)
	}
	legacyFailure := sequencedReplay
	legacyFailure.BundleVersion = ""
	legacyFailure.State = "pending_apply"
	legacyFailure.LastDeliverySequence = 0
	if outcome, err := store.UpsertTenantEdgeApplyAck(ctx, &legacyFailure); err != nil || outcome != TenantEdgeApplyAckStale {
		t.Fatalf("versionless zero-sequence failure crossed sequenced fence outcome=%q err=%v", outcome, err)
	}
	rows, err = store.ListTenantEdgeApplyState(ctx, tenantID, "")
	if err != nil || len(rows) != 1 || rows[0].State != "in_dns" || rows[0].BundleVersion != sequencedReplay.BundleVersion || !rows[0].InDNSAt.Valid {
		t.Fatalf("versionless zero-sequence failure regressed sequenced authority: rows=%#v err=%v", rows, err)
	}
	// Model the expand migration's unknowable legacy revision. Such a row may
	// continue serving, and a legacy failure may demote it, but it cannot be
	// promoted or claim readiness again without a revision-bearing ACK.
	if _, err := db.ExecContext(ctx, `
		UPDATE navigator.tenant_edge_apply_state
		SET bundle_version = '', last_delivery_sequence = 0
		WHERE tenant_id = $1::uuid AND node_id = $2
	`, tenantID, sequencedReplay.NodeID); err != nil {
		t.Fatal(err)
	}
	legacySuccess := legacyFailure
	legacySuccess.State = "applied"
	legacySuccess.LastSeedVersion = sql.NullInt64{Valid: true, Int64: 10}
	if outcome, err := store.UpsertTenantEdgeApplyAck(ctx, &legacySuccess); err != nil || outcome != TenantEdgeApplyAckAccepted {
		t.Fatalf("newer legacy-only success outcome=%q err=%v", outcome, err)
	}
	rows, err = store.ListTenantEdgeApplyState(ctx, tenantID, "")
	if err != nil || len(rows) != 1 || rows[0].State != "in_dns" || rows[0].BundleVersion != "" || !rows[0].InDNSAt.Valid {
		t.Fatalf("newer legacy-only success lost established continuity: rows=%#v err=%v", rows, err)
	}
	legacyFailure.LastSeedVersion = legacySuccess.LastSeedVersion
	if outcome, err := store.UpsertTenantEdgeApplyAck(ctx, &legacyFailure); err != nil || outcome != TenantEdgeApplyAckAccepted {
		t.Fatalf("legacy-only failure outcome=%q err=%v", outcome, err)
	}
	legacyRecovery := legacyFailure
	legacyRecovery.State = "applied"
	if outcome, err := store.UpsertTenantEdgeApplyAck(ctx, &legacyRecovery); err != nil || outcome != TenantEdgeApplyAckAccepted {
		t.Fatalf("legacy-only recovery outcome=%q err=%v", outcome, err)
	}
	versionedRecovery := sequencedReplay
	versionedRecovery.LastSeedVersion = sql.NullInt64{Valid: true, Int64: 11}
	versionedRecovery.LastDeliverySequence = 102
	if outcome, err := store.UpsertTenantEdgeApplyAck(ctx, &versionedRecovery); err != nil || outcome != TenantEdgeApplyAckAccepted {
		t.Fatalf("versioned recovery after legacy failure outcome=%q err=%v", outcome, err)
	}
	if advanced, err := store.MarkTenantEdgeInDNS(ctx, &legacyRecovery); err != nil || advanced {
		t.Fatalf("versionless recovery entered DNS advanced=%v err=%v", advanced, err)
	}
	if advanced, err := store.MarkTenantEdgeInDNS(ctx, &versionedRecovery); err != nil || !advanced {
		t.Fatalf("versioned recovery did not re-enter DNS advanced=%v err=%v", advanced, err)
	}
	orderedVersionlessFailure := legacyFailure
	orderedVersionlessFailure.LastDeliverySequence = 101
	if outcome, err := store.UpsertTenantEdgeApplyAck(ctx, &orderedVersionlessFailure); err != nil || outcome != TenantEdgeApplyAckStale {
		t.Fatalf("older sequenced versionless failure outcome=%q err=%v, want stale", outcome, err)
	}
	lateSameSeedFailure := failed
	lateSameSeedFailure.LastSeedVersion = newerRecovery.LastSeedVersion
	lateSameSeedFailure.LastDeliverySequence = 101
	if outcome, err := store.UpsertTenantEdgeApplyAck(ctx, &lateSameSeedFailure); err != nil || outcome != TenantEdgeApplyAckStale {
		t.Fatalf("late same-seed failure outcome=%q err=%v", outcome, err)
	}
	rows, err = store.ListTenantEdgeApplyState(ctx, tenantID, "")
	if err != nil || len(rows) != 1 || rows[0].State != "in_dns" || rows[0].LastDeliverySequence != 102 || !rows[0].InDNSAt.Valid {
		t.Fatalf("late same-seed failure regressed ordered ACK: rows=%#v err=%v", rows, err)
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
	if transitioned, err := store.SetTenantCustomDomainStatus(ctx, tenantID, domain.Domain, "pending_verification", "verified", ""); err != nil || !transitioned {
		t.Fatal(err)
	}
	if written, err := store.SetTenantCustomDomainCertMetadata(ctx, tenantID, domain.Domain, "verified", "letsencrypt", sql.NullTime{Time: now.Add(90 * 24 * time.Hour), Valid: true}); err != nil || !written {
		t.Fatal(err)
	}
	domain, err = store.GetTenantCustomDomain(ctx, tenantID, domain.Domain)
	if err != nil || !domain.LastVerifiedAt.Valid || domain.IssuerID.String != "letsencrypt" || !domain.CertExpiresAt.Valid {
		t.Fatalf("custom domain metadata = %#v, err = %v", domain, err)
	}
	if rows, err := store.ListTenantCustomDomains(ctx, tenantID); err != nil || len(rows) != 2 {
		t.Fatalf("tenant custom domains = %#v, err = %v", rows, err)
	}
	if rows, err := store.ListTenantCustomDomainsByStatus(ctx, []string{"verified"}); err != nil || len(rows) != 1 {
		t.Fatalf("tenant custom domains by status = %#v, err = %v", rows, err)
	}
	if transitioned, err := store.SetTenantCustomDomainStatus(ctx, tenantID, domain.Domain, "", "tearing_down", ""); err != nil || !transitioned {
		t.Fatalf("mark custom domain teardown transitioned=%v err=%v", transitioned, err)
	}
	domain, err = store.EnsureTenantCustomDomain(ctx, tenantID, domain.Domain, domain.AcmeDNSSubdomain)
	if err != nil || domain.Status != "pending_verification" || domain.CertExpiresAt.Valid || domain.IssuerID.Valid {
		t.Fatalf("reactivated custom domain=%#v err=%v", domain, err)
	}
	if finalized, err := store.FinalizeTenantCustomDomainRemoval(ctx, tenantID, domain.Domain); err != nil || finalized {
		t.Fatalf("stale custom-domain teardown finalized=%v err=%v", finalized, err)
	}
	if transitioned, err := store.SetTenantCustomDomainStatus(ctx, tenantID, "missing.example.test", "pending_verification", "verified", ""); err != nil || transitioned {
		t.Fatalf("missing custom domain transitioned=%v error=%v", transitioned, err)
	}
	if err := store.DeleteTenantCustomDomain(ctx, tenantID, domain.Domain); err != nil {
		t.Fatal(err)
	}
	preservedDomain, err := store.EnsureTenantCustomDomain(ctx, tenantID, "preserved.example.test", "challenge-two")
	if err != nil {
		t.Fatal(err)
	}
	if transitioned, err := store.SetTenantCustomDomainStatus(ctx, tenantID, preservedDomain.Domain, "pending_verification", "cert_failed", "retrying"); err != nil || !transitioned {
		t.Fatal(err)
	}
	preservedCert := &Certificate{Domain: preservedDomain.Domain, CertPEM: "custom-cert", KeyPEM: "custom-key", ExpiresAt: now.Add(90 * 24 * time.Hour)}
	if err := store.SaveCertificate(ctx, tenantID, preservedCert); err != nil {
		t.Fatal(err)
	}
	currentBundle, err := store.GetTLSBundle(ctx, bundle.BundleID)
	if err != nil {
		t.Fatal(err)
	}
	preRevokeACK := &TenantEdgeApplyState{
		TenantID: tenantID, ClusterID: "cluster-1", NodeID: "node-1", BundleID: bundle.BundleID,
		BundleVersion: currentBundle.Version, State: "applied",
		LastSeedVersion: sql.NullInt64{Valid: true, Int64: 5000}, LastDeliverySequence: 5000,
	}
	if outcome, err := store.UpsertTenantEdgeApplyAck(ctx, preRevokeACK); err != nil || outcome != TenantEdgeApplyAckAccepted {
		t.Fatalf("seed pre-revocation edge outcome=%q err=%v", outcome, err)
	}
	if applied, err := store.RevokeTenantAliasClusterAuthority(ctx, tenantID, "cluster-1", 20); err != nil || !applied {
		t.Fatalf("revoke cluster-1 authority applied=%v err=%v", applied, err)
	}
	if state, err := store.TenantAliasClusterAuthorityState(ctx, tenantID, "cluster-1"); err != nil || state != "revoked" {
		t.Fatalf("removed tenant-cluster authority state=%q err=%v", state, err)
	}
	var clusterOneRows int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM navigator.tenant_edge_apply_state
WHERE tenant_id=$1::uuid AND cluster_id='cluster-1'`, tenantID).Scan(&clusterOneRows); err != nil {
		t.Fatal(err)
	}
	if clusterOneRows != 0 {
		t.Fatalf("revocation left %d cluster-1 edge rows", clusterOneRows)
	}
	// A residual row beneath the tombstone must not be promotable: the
	// promotion writer enforces active authority itself, independent of the
	// publisher's filtered list.
	if _, err := db.ExecContext(ctx, `
INSERT INTO navigator.tenant_edge_apply_state (
    tenant_id, cluster_id, node_id, bundle_id, bundle_version, state, last_seed_version, last_delivery_sequence
) VALUES ($1::uuid, 'cluster-1', 'residual-node', $2, $3, 'applied', 5001, 5001)`, tenantID, bundle.BundleID, currentBundle.Version); err != nil {
		t.Fatal(err)
	}
	residual := &TenantEdgeApplyState{
		TenantID: tenantID, ClusterID: "cluster-1", NodeID: "residual-node", BundleID: bundle.BundleID,
		BundleVersion: currentBundle.Version, State: "applied",
		LastSeedVersion: sql.NullInt64{Valid: true, Int64: 5001}, LastDeliverySequence: 5001,
	}
	if advanced, err := store.MarkTenantEdgeInDNS(ctx, residual); err != nil || advanced {
		t.Fatalf("residual row beneath tombstone was promotable advanced=%v err=%v", advanced, err)
	}
	if _, err := db.ExecContext(ctx, `
DELETE FROM navigator.tenant_edge_apply_state
WHERE tenant_id=$1::uuid AND node_id='residual-node'`, tenantID); err != nil {
		t.Fatal(err)
	}
	lateRevokedACK := *ack
	lateRevokedACK.LastSeedVersion = sql.NullInt64{Valid: true, Int64: 999}
	lateRevokedACK.LastDeliverySequence = 999
	if outcome, err := store.UpsertTenantEdgeApplyAck(ctx, &lateRevokedACK); err != nil || outcome != TenantEdgeApplyAckRevoked {
		t.Fatalf("late ACK crossed cluster revocation outcome=%q err=%v, want revoked", outcome, err)
	}
	if applied, err := store.GrantTenantAliasClusterAuthority(ctx, tenantID, "cluster-1", 0); err != nil || applied {
		t.Fatalf("legacy grant crossed sequenced revocation applied=%v err=%v", applied, err)
	}
	if applied, err := store.GrantTenantAliasClusterAuthority(ctx, tenantID, "cluster-2", 21); err != nil || !applied {
		t.Fatalf("grant cluster-2 authority applied=%v err=%v", applied, err)
	}
	clusterTwo := &TenantEdgeApplyState{
		TenantID: tenantID, ClusterID: "cluster-2", NodeID: "node-2", BundleID: bundle.BundleID, BundleVersion: bundle.Version, State: "applied",
	}
	if outcome, err := store.UpsertTenantEdgeApplyAck(ctx, clusterTwo); err != nil || outcome != TenantEdgeApplyAckAccepted {
		t.Fatal(err)
	}
	if transitioned, err := store.SetTenantAliasStatus(ctx, tenantID, "", 0, "tearing_down", ""); err != nil || !transitioned {
		t.Fatal(err)
	}
	blockedBundle := *bundle
	blockedBundle.CertPEM = "must-not-persist"
	if err := store.SaveTLSBundle(ctx, &blockedBundle); !errors.Is(err, ErrAuthorityLost) {
		t.Fatalf("tenant bundle write after teardown authority = %v, want ErrAuthorityLost", err)
	}
	if got, err := store.GetTLSBundle(ctx, bundle.BundleID); err != nil || got.Version != bundle.Version {
		t.Fatalf("last-good tenant bundle was not served until teardown deletion: bundle=%#v err=%v", got, err)
	}
	if deleted, err := store.DeleteTenantAlias(ctx, tenantID); err != nil || !deleted {
		t.Fatal(err)
	}
	if err := store.SaveTLSBundle(ctx, &blockedBundle); !errors.Is(err, ErrAuthorityLost) {
		t.Fatalf("tenant bundle write without alias authority = %v, want ErrAuthorityLost", err)
	}
	if _, err := store.GetTLSBundle(ctx, bundle.BundleID); err != ErrNotFound {
		t.Fatalf("tenant alias bundle survived teardown: %v", err)
	}
	if _, err := store.GetCertificate(ctx, tenantID, tenantCert.Domain); err != ErrNotFound {
		t.Fatalf("alias-only certificate survived teardown: %v", err)
	}
	if got, err := store.GetCertificate(ctx, tenantID, preservedCert.Domain); err != nil || got.Domain != preservedCert.Domain {
		t.Fatalf("active custom-domain certificate removed with alias: cert=%#v err=%v", got, err)
	}
	if _, err := store.GetCertificate(ctx, "", platformCert.Domain); err != nil {
		t.Fatalf("platform certificate removed with tenant alias: %v", err)
	}
	lateAfterTeardown := *clusterTwo
	lateAfterTeardown.LastSeedVersion = sql.NullInt64{Int64: 10, Valid: true}
	lateAfterTeardown.LastDeliverySequence = 102
	if outcome, err := store.UpsertTenantEdgeApplyAck(ctx, &lateAfterTeardown); err != nil || outcome != TenantEdgeApplyAckMissingParent {
		t.Fatalf("late ACK after teardown outcome=%q err=%v", outcome, err)
	}
	if rows, err := store.ListTenantEdgeApplyState(ctx, tenantID, ""); err != nil || len(rows) != 0 {
		t.Fatalf("late ACK resurrected orphan apply state: rows=%#v err=%v", rows, err)
	}
	if err := store.DeleteCertificate(ctx, tenantID, preservedCert.Domain); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteTenantCustomDomain(ctx, tenantID, preservedDomain.Domain); err != nil {
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

func startNavigatorStoreRealYugabyte(t *testing.T) *sql.DB {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-navigator-store-realyb-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = dockerpg.CLI("rm", "-fv", name) })
	image, err := dockerpg.YugabyteImage()
	if err != nil {
		t.Fatal(err)
	}
	if output, err := dockerpg.Run("run", "-d", "--name", name, "-P", "--hostname", name, image, "bash", "-c", `exec bin/yugabyted start --background=false --advertise_address="$(hostname -i)"`); err != nil {
		t.Fatalf("docker run: %v\n%s", err, output)
	}
	port, err := dockerpg.DiscoverPublishedHostPort(name, "5433/tcp")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", fmt.Sprintf("postgres://yugabyte@127.0.0.1:%s/yugabyte?sslmode=disable", port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := dockerpg.WaitReadyFor(db, name, 3*time.Minute); err != nil {
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
