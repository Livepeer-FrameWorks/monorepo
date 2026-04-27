package readiness

import (
	"context"
	"fmt"

	commodoreCli "frameworks/pkg/clients/commodore"
	purserclient "frameworks/pkg/clients/purser"
	"frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/logging"
)

// ClusterPricing declares that a cluster expects pricing config to exist
// in Purser. ControlPlaneReadiness will emit a warning per cluster where
// Purser has no matching pricing.
type ClusterPricing struct {
	ClusterID string
}

// ServiceEndpoint carries everything a gRPC client needs to dial a single
// service. GRPCAddr is what the client dials; ServerName is the SNI/cert-
// verification name (typically the service's mesh DNS or external IP when
// the dial address is a tunneled loopback). CACertPEM is an inline CA
// bundle, used during bootstrap when the operator holds the internal PKI
// material before the trust store is distributed.
type ServiceEndpoint struct {
	GRPCAddr      string
	ServerName    string
	AllowInsecure bool
	CACertPEM     string
}

// Empty reports whether the endpoint is unset enough that no dial should
// be attempted.
func (e ServiceEndpoint) Empty() bool {
	return e.GRPCAddr == ""
}

// ControlPlaneInputs is the resolved set of endpoints and state needed to
// evaluate control-plane readiness. The caller (typically cluster provision,
// doctor, or status) is responsible for extracting these from the manifest.
type ControlPlaneInputs struct {
	SystemTenantID   string
	ServiceToken     string
	Quartermaster    ServiceEndpoint
	Commodore        ServiceEndpoint
	Purser           ServiceEndpoint
	DeclaredPricings []ClusterPricing
}

// ControlPlaneReadiness checks whether a freshly-provisioned control plane
// is usable: Quartermaster has a default + platform-official cluster,
// Commodore reports at least one user in the system tenant, and Purser
// has pricing for clusters that declared it.
//
// Missing endpoints / tokens degrade to "cannot check" rather than error,
// so this is safe to run from read-only commands (status, doctor) that
// may not have full runtime data. Client-construction failures and probe
// errors surface as warnings — never silent.
func ControlPlaneReadiness(ctx context.Context, in ControlPlaneInputs) Report {
	var report Report

	if in.SystemTenantID == "" || in.ServiceToken == "" || in.Quartermaster.Empty() {
		// Missing auth inputs — return Checked=false so callers don't
		// render this as "healthy" when we skipped every check.
		return report
	}
	report.Checked = true

	qm, err := quartermaster.NewGRPCClient(quartermaster.GRPCConfig{
		GRPCAddr:      in.Quartermaster.GRPCAddr,
		Logger:        logging.NewLogger(),
		ServiceToken:  in.ServiceToken,
		AllowInsecure: in.Quartermaster.AllowInsecure,
		ServerName:    in.Quartermaster.ServerName,
		CACertPEM:     in.Quartermaster.CACertPEM,
	})
	if err != nil {
		report.Warnings = append(report.Warnings, Warning{
			Subject: "control-plane.quartermaster",
			Detail:  fmt.Sprintf("Could not connect to Quartermaster: %v", err),
		})
	} else {
		defer qm.Close()
		resp, lErr := qm.ListClusters(ctx, nil)
		switch {
		case lErr != nil:
			report.Warnings = append(report.Warnings, Warning{
				Subject: "control-plane.quartermaster",
				Detail:  fmt.Sprintf("Could not list clusters from Quartermaster: %v", lErr),
			})
		default:
			hasDefault, hasOfficial := false, false
			for _, c := range resp.GetClusters() {
				if c.GetIsDefaultCluster() {
					hasDefault = true
				}
				if c.GetIsPlatformOfficial() {
					hasOfficial = true
				}
			}
			if !hasDefault {
				report.Warnings = append(report.Warnings, Warning{
					Subject: "control-plane.default-cluster",
					Detail:  "No default cluster — new tenant signups will not auto-subscribe.",
				})
			}
			if !hasOfficial {
				report.Warnings = append(report.Warnings, Warning{
					Subject: "control-plane.platform-official-cluster",
					Detail:  "No platform-official cluster — billing tier access will not work.",
				})
			}
		}
	}

	if !in.Commodore.Empty() {
		c, cErr := commodoreCli.NewGRPCClient(commodoreCli.GRPCConfig{
			GRPCAddr:      in.Commodore.GRPCAddr,
			Logger:        logging.NewLogger(),
			ServiceToken:  in.ServiceToken,
			AllowInsecure: in.Commodore.AllowInsecure,
			ServerName:    in.Commodore.ServerName,
			CACertPEM:     in.Commodore.CACertPEM,
		})
		if cErr != nil {
			report.Warnings = append(report.Warnings, Warning{
				Subject: "control-plane.commodore",
				Detail:  fmt.Sprintf("Could not connect to Commodore: %v", cErr),
			})
		} else {
			defer c.Close()
			countResp, countErr := c.GetTenantUserCount(ctx, in.SystemTenantID)
			switch {
			case countErr != nil:
				report.Warnings = append(report.Warnings, Warning{
					Subject: "control-plane.operator-account",
					Detail:  fmt.Sprintf("Could not check platform tenant users: %v", countErr),
				})
			case countResp.GetActiveCount() == 0:
				report.Warnings = append(report.Warnings, Warning{
					Subject: "control-plane.operator-account",
					Detail:  "No users in platform tenant.",
					Remediation: Remediation{
						Cmd: "frameworks admin users create --email <you> --role owner",
						Why: "Create an initial operator account so you can log in to the platform.",
					},
				})
			}
		}
	}

	if !in.Purser.Empty() && len(in.DeclaredPricings) > 0 {
		p, pErr := purserclient.NewGRPCClient(purserclient.GRPCConfig{
			GRPCAddr:      in.Purser.GRPCAddr,
			Logger:        logging.NewLogger(),
			ServiceToken:  in.ServiceToken,
			AllowInsecure: in.Purser.AllowInsecure,
			ServerName:    in.Purser.ServerName,
			CACertPEM:     in.Purser.CACertPEM,
		})
		if pErr != nil {
			report.Warnings = append(report.Warnings, Warning{
				Subject: "control-plane.purser",
				Detail:  fmt.Sprintf("Could not connect to Purser: %v", pErr),
			})
		} else {
			defer p.Close()
			for _, dp := range in.DeclaredPricings {
				pricing, pricingErr := p.GetClusterPricing(ctx, dp.ClusterID)
				if pricingErr != nil || pricing == nil || pricing.GetPricingModel() == "" {
					report.Warnings = append(report.Warnings, Warning{
						Subject: fmt.Sprintf("control-plane.pricing.%s", dp.ClusterID),
						Detail:  fmt.Sprintf("No pricing config for cluster %s (manifest declares pricing but Purser has none).", dp.ClusterID),
					})
				}
			}
		}
	}

	return report
}
