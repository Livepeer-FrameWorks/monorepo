package cmd

import (
	"sort"

	"frameworks/cli/pkg/inventory"

	pkgdns "github.com/Livepeer-FrameWorks/monorepo/pkg/dns"
)

// privateerRegionalSignalmanDNSRecords returns signalman.<region> record names
// mapped to the Signalman hosts in that region, for hosts whose local services
// resolve Signalman. The records exist so a Bridge still running the previous
// release, which dials the stream origin's regional Signalman, keeps resolving
// it while `release apply` converges seeds ahead of the service upgrades.
func privateerRegionalSignalmanDNSRecords(manifest *inventory.Manifest, selfHostName string) map[string][]string {
	if manifest == nil || selfHostName == "" {
		return nil
	}
	if _, ok := privateerDNSAliasesForHost(manifest, selfHostName)["signalman"]; !ok {
		return nil
	}
	svc, ok := manifest.Services["signalman"]
	if !ok || !svc.Enabled {
		return nil
	}
	out := map[string][]string{}
	for _, hostName := range serviceHosts(svc) {
		region := pkgdns.SanitizeLabel(privateerHostRegion(manifest, hostName))
		if region == "" {
			continue
		}
		recordName := "signalman." + region
		out[recordName] = append(out[recordName], hostName)
	}
	for recordName := range out {
		sort.Strings(out[recordName])
	}
	return out
}
