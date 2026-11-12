package agent

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	pkgdns "github.com/Livepeer-FrameWorks/monorepo/pkg/dns"
	pkgingress "github.com/Livepeer-FrameWorks/monorepo/pkg/ingress"
	dnspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/dns"
)

type aliasCertificate struct {
	Subdomain string `json:"subdomain"`
	Host      string `json:"host"`
	Path      string `json:"path"`
}

func aliasLeaf(certPEM, keyPEM []byte, now time.Time) (*x509.Certificate, error) {
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("parse certificate pair: %w", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, err
	}
	if now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return nil, errors.New("certificate is outside its validity interval")
	}
	return leaf, nil
}

func (a *Agent) installAliasCertificate(tenant string, response *dnspb.GetTLSBundleResponse, now time.Time) (aliasCertificate, error) {
	if id := response.GetBundleId(); id != "" && id != "tenant:"+tenant {
		return aliasCertificate{}, errors.New("certificate bundle belongs to another tenant")
	}
	leaf, err := aliasLeaf([]byte(response.GetCertPem()), []byte(response.GetKeyPem()), now)
	if err != nil {
		return aliasCertificate{}, err
	}
	names := map[string]bool{}
	for _, name := range leaf.DNSNames {
		names[strings.ToLower(strings.TrimSuffix(name, "."))] = true
	}
	var sub, apex string
	for _, domain := range response.GetDomains() {
		domain = strings.ToLower(strings.TrimSuffix(domain, "."))
		label, root, ok := strings.Cut(domain, "."+pkgdns.TenantAliasZoneLabel+".")
		if !ok || !pkgingress.IsValidBundleID(label) || !validAliasDNSName(domain) || root == "" || !names[domain] || !names["*."+domain] {
			continue
		}
		if apex != "" && apex != domain {
			return aliasCertificate{}, errors.New("certificate contains multiple tenant alias authorities")
		}
		sub, apex = label, domain
	}
	if apex == "" {
		return aliasCertificate{}, errors.New("certificate lacks an authoritative tenant alias apex/wildcard pair")
	}
	label, ok := pkgdns.PublicSubdomain(foghornServiceType)
	if !ok || label == "" {
		return aliasCertificate{}, errors.New("foghorn public label is unavailable")
	}
	host := label + "." + apex
	if err := leaf.VerifyHostname(host); err != nil {
		return aliasCertificate{}, err
	}
	sum := sha256.Sum256([]byte(response.GetCertPem() + "\x00" + response.GetKeyPem()))
	path := filepath.ToSlash(filepath.Join("revisions", sub, hex.EncodeToString(sum[:])))
	dir := a.tenantAliasDir(path)
	if err := a.ensureAliasDirectory(dir); err != nil {
		return aliasCertificate{}, err
	}
	if err := a.writeAliasFile(filepath.Join(dir, "tls.crt"), []byte(response.GetCertPem()), 0o644); err != nil {
		return aliasCertificate{}, err
	}
	if err := a.writeAliasFile(filepath.Join(dir, "tls.key"), []byte(response.GetKeyPem()), 0o640); err != nil {
		return aliasCertificate{}, err
	}
	return aliasCertificate{Subdomain: sub, Host: host, Path: path}, nil
}

func validAliasDNSName(name string) bool {
	if len(name) > 253 {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if c != '-' && (c < 'a' || c > 'z') && (c < '0' || c > '9') {
				return false
			}
		}
	}
	return true
}

func (a *Agent) aliasCertificateUsable(material aliasCertificate, now time.Time) bool {
	parts := strings.Split(material.Path, "/")
	if len(parts) != 3 || parts[0] != "revisions" || parts[1] != material.Subdomain || !pkgingress.IsValidBundleID(material.Subdomain) || len(parts[2]) != 64 || !validAliasDNSName(material.Host) {
		return false
	}
	if _, err := hex.DecodeString(parts[2]); err != nil {
		return false
	}
	cert, err := os.ReadFile(filepath.Join(a.tenantAliasDir(material.Path), "tls.crt"))
	if err != nil {
		return false
	}
	key, err := os.ReadFile(filepath.Join(a.tenantAliasDir(material.Path), "tls.key"))
	if err != nil {
		return false
	}
	leaf, err := aliasLeaf(cert, key, now)
	return err == nil && leaf.VerifyHostname(material.Host) == nil
}

func (a *Agent) ensureAliasDirectory(dir string) error {
	root := filepath.Clean(a.ingressTLSRoot)
	relative, err := filepath.Rel(root, dir)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("alias directory escapes ingress TLS root")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	// Every ancestor must be traversable by the ingress worker, including paths
	// created before the dedicated reader group was introduced.
	current := root
	for _, part := range append([]string{""}, strings.Split(relative, string(filepath.Separator))...) {
		if part != "" {
			current = filepath.Join(current, part)
		}
		if err := os.Chmod(current, 0o750|os.ModeSetgid); err != nil {
			return err
		}
		if err := a.setAliasReaderGroup(current); err != nil {
			return err
		}
	}
	return nil
}
func (a *Agent) setAliasReaderGroup(path string) error {
	if a.ingressReaderGroup == "" {
		return nil
	}
	gid, found, err := lookupGroupID(a.ingressReaderGroup)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("ingress reader group %s is missing; converge the Privateer ingress permissions", a.ingressReaderGroup)
	}
	return chgrpIfNeeded(path, gid)
}

// Old workers may still reference their loaded generation after a reload.
// Keep immutable generations through certificate expiry; never accumulate
// expired generations or mutate the pair beneath an active map entry.
func (a *Agent) pruneExpiredAliasGenerations(active map[string]aliasCertificate, now time.Time) error {
	keep := map[string]bool{}
	for _, material := range active {
		keep[material.Path] = true
	}
	root := a.tenantAliasDir("revisions")
	tenants, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, tenant := range tenants {
		if !tenant.IsDir() {
			continue
		}
		generations, err := os.ReadDir(filepath.Join(root, tenant.Name()))
		if err != nil {
			return err
		}
		for _, generation := range generations {
			path := filepath.ToSlash(filepath.Join("revisions", tenant.Name(), generation.Name()))
			if keep[path] {
				continue
			}
			cert, certErr := os.ReadFile(filepath.Join(a.tenantAliasDir(path), "tls.crt"))
			key, keyErr := os.ReadFile(filepath.Join(a.tenantAliasDir(path), "tls.key"))
			if certErr == nil && keyErr == nil {
				if _, err := aliasLeaf(cert, key, now); err == nil {
					continue
				}
			}
			if err := os.RemoveAll(a.tenantAliasDir(path)); err != nil {
				return err
			}
		}
	}
	return nil
}
