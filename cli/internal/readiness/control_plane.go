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

// ControlPlaneInputs is the resolved set of endpoints and state needed to
// evaluate control-plane readiness. The caller (typically cluster provision,
// doctor, or status) is responsible for extracting these from the manifest.
type ControlPlaneInputs struct {
	SystemTenantID    string
	ServiceToken      string
	QuartermasterAddr string
	CommodoreAddr     string
	PurserAddr        string
	AllowInsecure     bool
	DeclaredPricings  []ClusterPricing
}

// ControlPlaneReadiness checks whether a freshly-provisioned control plane
// is usable: Quartermaster has a default + platform-official cluster,
// Commodore reports at least one user in the system tenant, and Purser
// has pricing for clusters that declared it.
//
// Missing endpoints / tokens degrade to "cannot check" rather than error,
// so this is safe to run from read-only commands (status, doctor) that
// may not have full runtime data.
func ControlPlaneReadiness(ctx context.Context, in ControlPlaneInputs) Report {
	var report Report

	if in.SystemTenantID == "" || in.ServiceToken == "" || in.QuartermasterAddr == "" {
		// Missing auth inputs — return Checked=false so callers don't
		// render this as "healthy" when we skipped every check.
		return report
	}
	report.Checked = true

	qm, err := quartermaster.NewGRPCClient(quartermaster.GRPCConfig{
		GRPCAddr:      in.QuartermasterAddr,
		Logger:        logging.NewLogger(),
		ServiceToken:  in.ServiceToken,
		AllowInsecure: in.AllowInsecure,
	})
	if err == nil {
		defer qm.Close()
		resp, lErr := qm.ListClusters(ctx, nil)
		if lErr == nil {
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

	if in.CommodoreAddr != "" {
		c, cErr := commodoreCli.NewGRPCClient(commodoreCli.GRPCConfig{
			GRPCAddr:      in.CommodoreAddr,
			Logger:        logging.NewLogger(),
			ServiceToken:  in.ServiceToken,
			AllowInsecure: in.AllowInsecure,
		})
		if cErr == nil {
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

	if in.PurserAddr != "" && len(in.DeclaredPricings) > 0 {
		p, pErr := purserclient.NewGRPCClient(purserclient.GRPCConfig{
			GRPCAddr:      in.PurserAddr,
			Logger:        logging.NewLogger(),
			ServiceToken:  in.ServiceToken,
			AllowInsecure: in.AllowInsecure,
		})
		if pErr == nil {
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
