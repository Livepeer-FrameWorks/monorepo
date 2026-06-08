package navigator

import (
	"context"
	dnspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/dns"
)

// Interface is the full method surface of the concrete client, extracted so
// that api_gateway can inject fakes for resolver real-path tests. The concrete
// client satisfies it (asserted below).
type Interface interface {
	Close() error
	SyncDNS(ctx context.Context, req *dnspb.SyncDNSRequest) (*dnspb.SyncDNSResponse, error)
	IssueCertificate(ctx context.Context, req *dnspb.IssueCertificateRequest) (*dnspb.IssueCertificateResponse, error)
	GetHealth(ctx context.Context) error
	GetCertificate(ctx context.Context, req *dnspb.GetCertificateRequest) (*dnspb.GetCertificateResponse, error)
	GetTLSBundle(ctx context.Context, req *dnspb.GetTLSBundleRequest) (*dnspb.GetTLSBundleResponse, error)
	GetCABundle(ctx context.Context, req *dnspb.GetCABundleRequest) (*dnspb.GetCABundleResponse, error)
	EnsureTenantAlias(ctx context.Context, req *dnspb.EnsureTenantAliasRequest) (*dnspb.EnsureTenantAliasResponse, error)
	RemoveTenantAlias(ctx context.Context, req *dnspb.RemoveTenantAliasRequest) (*dnspb.RemoveTenantAliasResponse, error)
	GetTenantAliasStatus(ctx context.Context, req *dnspb.GetTenantAliasStatusRequest) (*dnspb.GetTenantAliasStatusResponse, error)
	ReportConfigSeedApplyResult(ctx context.Context, req *dnspb.ReportConfigSeedApplyResultRequest) (*dnspb.ReportConfigSeedApplyResultResponse, error)
	EnsureCustomDomain(ctx context.Context, req *dnspb.EnsureCustomDomainRequest) (*dnspb.EnsureCustomDomainResponse, error)
	RemoveCustomDomain(ctx context.Context, req *dnspb.RemoveCustomDomainRequest) (*dnspb.RemoveCustomDomainResponse, error)
	GetCustomDomainStatus(ctx context.Context, req *dnspb.GetCustomDomainStatusRequest) (*dnspb.GetCustomDomainStatusResponse, error)
	RemoveTenantAliasCluster(ctx context.Context, req *dnspb.RemoveTenantAliasClusterRequest) (*dnspb.RemoveTenantAliasClusterResponse, error)
	RemoveTenantAliasSubdomain(ctx context.Context, req *dnspb.RemoveTenantAliasSubdomainRequest) (*dnspb.RemoveTenantAliasSubdomainResponse, error)
	IssueInternalCert(ctx context.Context, req *dnspb.IssueInternalCertRequest) (*dnspb.IssueInternalCertResponse, error)
}

var _ Interface = (*Client)(nil)
