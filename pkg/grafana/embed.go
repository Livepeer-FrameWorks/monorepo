// Package grafana holds the repo-owned Grafana dashboards and alert rules.
// Both are embedded so the CLI carries them in the binary and needs no
// monorepo checkout: `frameworks cluster grafana sync` pushes the dashboards,
// and vmalert provisioning renders the rules onto the vmalert host. Each
// dashboard must be classic-schema with a stable uid so the sync can push it
// idempotently.
package grafana

import (
	"embed"
)

//go:embed dashboards/*.json
var Content embed.FS

// Rules holds the vmalert rule files under rules/. Every rule expression must
// reference metrics the platform exports; cli/pkg/dashcheck enforces this.
//
//go:embed rules/*.yml
var Rules embed.FS
