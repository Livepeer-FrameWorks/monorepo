package control

import (
	"reflect"
	"testing"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/proto"
)

func TestTenantBundleSiteAddressesUsesCertificateAuthorityDuringRename(t *testing.T) {
	domains := []string{
		"media.customer.example",
		"*.old-label.cdn.frameworks.network",
		"old-label.cdn.frameworks.network",
	}
	got, err := tenantBundleSiteAddresses(domains, "cdn", "frameworks.network")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"old-label.cdn.frameworks.network", "*.old-label.cdn.frameworks.network", "media.customer.example"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("site addresses = %v, want %v", got, want)
	}
}

func TestTLSBundleSetStateIncludesRevisionAndSiteAddresses(t *testing.T) {
	base := &ipcpb.TLSCertBundle{
		BundleId: "tenant:one", CertPem: "cert", KeyPem: "key", Domain: "old.cdn.example",
		ExpiresAt: 1234, Version: "revision-1", SiteAddresses: []string{"old.cdn.example", "*.old.cdn.example"},
	}
	baseState := tlsBundleSetState([]*ipcpb.TLSCertBundle{base}, nil)

	revisionChanged := proto.Clone(base).(*ipcpb.TLSCertBundle)
	revisionChanged.Version = "revision-2"
	if got := tlsBundleSetState([]*ipcpb.TLSCertBundle{revisionChanged}, nil); got == baseState {
		t.Fatal("revision-only change did not advance TLS bundle state")
	}

	addressesChanged := proto.Clone(base).(*ipcpb.TLSCertBundle)
	addressesChanged.SiteAddresses = append(addressesChanged.SiteAddresses, "media.customer.example")
	if got := tlsBundleSetState([]*ipcpb.TLSCertBundle{addressesChanged}, nil); got == baseState {
		t.Fatal("site-address-only change did not advance TLS bundle state")
	}
}

func TestTenantBundleSiteAddressesRejectsMissingOrAmbiguousAliasAuthority(t *testing.T) {
	tests := map[string][]string{
		"missing wildcard": {"old-label.cdn.frameworks.network"},
		"custom only":      {"media.customer.example"},
		"nested label": {
			"nested.old-label.cdn.frameworks.network", "*.nested.old-label.cdn.frameworks.network",
		},
		"ambiguous": {
			"old-label.cdn.frameworks.network", "*.old-label.cdn.frameworks.network",
			"new-label.cdn.frameworks.network", "*.new-label.cdn.frameworks.network",
		},
	}
	for name, domains := range tests {
		t.Run(name, func(t *testing.T) {
			if got, err := tenantBundleSiteAddresses(domains, "cdn", "frameworks.network"); err == nil {
				t.Fatalf("site addresses = %v, want error", got)
			}
		})
	}
	if got, err := tenantBundleSiteAddresses([]string{"old-label.cdn.frameworks.network", "*.old-label.cdn.frameworks.network"}, "cdn", ""); err == nil {
		t.Fatalf("empty root returned site addresses %v", got)
	}
}
