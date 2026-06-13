// Package grafana holds the repo-owned Grafana dashboards. They are embedded
// so `frameworks cluster grafana sync` carries them in the binary and needs
// no monorepo checkout; each dashboard must be classic-schema with a stable
// uid so the sync can push it idempotently.
package grafana

import (
	"embed"
)

//go:embed dashboards/*.json
var Content embed.FS
