// Package ingress holds platform-wide constants and helpers for cluster
// public-ingress TLS material managed by Privateer.
//
// Bundle IDs are used as path components beneath TLSRoot, so callers MUST
// validate them with IsValidBundleID before deriving paths or writing
// files. The validator's pattern is the canonical source of truth for
// what a bundle ID is allowed to look like; cluster_provision generates
// IDs that match this pattern and Privateer rejects any QM/manifest input
// that does not.
package ingress

import "regexp"

// TLSRoot is the on-host directory beneath which Privateer writes per-bundle
// TLS material. Per-bundle layout: <TLSRoot>/<bundle_id>/tls.{crt,key}.
const TLSRoot = "/etc/frameworks/ingress/tls"

// ReloadTrigger is the file Privateer touches after every successful public
// TLS sync that resulted in a write. A root-owned systemd path unit watches
// this file and runs `nginx -t && systemctl reload`.
const ReloadTrigger = "/etc/frameworks/ingress/reload.trigger"

// bundleIDPattern matches the IDs produced by cli/cmd/cluster_provision's
// tlsBundleID(): a lowercase ASCII letter or digit followed by up to 127
// more lowercase ASCII letters, digits, or hyphens. Anything containing
// "..", "/", whitespace, uppercase, or other separators is rejected so a
// poisoned manifest or Quartermaster row cannot escape TLSRoot.
var bundleIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,127}$`)

// IsValidBundleID reports whether s is safe to use as a bundle id (and as
// the per-bundle directory name beneath TLSRoot).
func IsValidBundleID(s string) bool {
	return bundleIDPattern.MatchString(s)
}

// TLSCertPath returns the canonical on-disk path for a bundle's certificate.
// The caller is responsible for validating bundleID with IsValidBundleID
// before passing it here; this helper does not.
func TLSCertPath(bundleID string) string {
	return TLSRoot + "/" + bundleID + "/tls.crt"
}

// TLSKeyPath returns the canonical on-disk path for a bundle's private key.
// See TLSCertPath for caller obligations.
func TLSKeyPath(bundleID string) string {
	return TLSRoot + "/" + bundleID + "/tls.key"
}

// TenantAliasDirName is the directory beneath TLSRoot where Privateer
// materializes per-tenant alias wildcard certs, keyed by the tenant's alias
// SUBDOMAIN (not the Navigator bundle ID "tenant:<uuid>" — the colon fails
// IsValidBundleID, and nginx can only derive the subdomain from
// $ssl_server_name at handshake time). Reserved as a bundle ID so a manifest
// bundle can never collide with this subtree.
const TenantAliasDirName = "tenant-alias"

// TenantAliasCertPath returns the canonical on-disk path for a tenant alias
// certificate. The caller must validate subdomain with IsValidBundleID
// (subdomains satisfy the same charset) before passing it here.
func TenantAliasCertPath(subdomain string) string {
	return TLSRoot + "/" + TenantAliasDirName + "/" + subdomain + "/tls.crt"
}

// TenantAliasKeyPath returns the canonical on-disk path for a tenant alias
// private key. See TenantAliasCertPath for caller obligations.
func TenantAliasKeyPath(subdomain string) string {
	return TLSRoot + "/" + TenantAliasDirName + "/" + subdomain + "/tls.key"
}
