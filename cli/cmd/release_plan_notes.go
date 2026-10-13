package cmd

import (
	"fmt"
	"io"
	"strings"

	"frameworks/cli/pkg/inventory"
)

// Plan lines for release steps whose behavior lives outside cluster_release.go.
const (
	// releasePlanServiceDatabasesStep is step 2 of `release plan`.
	releasePlanServiceDatabasesStep = "2. service databases (created with roles and current baseline when missing on the cluster); runtime-role session limits on every SQL deployment and Postgres instance"
	// releaseApplyServiceDatabasesStep is step 2 of the plan `release apply` prints.
	releaseApplyServiceDatabasesStep = "  2. service databases missing on the cluster (created with roles and current baseline), runtime-role session limits on every SQL deployment and Postgres instance, then expand migrations"
)

// writeFoghornCellGatePlan describes the per-cell Foghorn rollout when services
// upgrades a Foghorn deploy.
func writeFoghornCellGatePlan(out io.Writer, manifest *inventory.Manifest, services []string) {
	var foghorns []string
	for _, service := range services {
		if releaseDeployName(manifest, service) == "foghorn" {
			foghorns = append(foghorns, service)
		}
	}
	if len(foghorns) == 0 {
		return
	}
	fmt.Fprintf(out, "     · %s: one cell at a time; after each cell, wait (up to %s) until it holds no tenant on the legacy (schema 1) authority before the next cell\n",
		strings.Join(foghorns, ", "), mediaCellGateTimeout)
}
