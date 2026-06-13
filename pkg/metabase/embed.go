// Package metabase holds the managed Metabase card specs. They are embedded
// so `frameworks cluster metabase sync` carries them in the binary and needs
// no monorepo checkout or --file path; each spec declares its own target
// dashboard.
package metabase

import (
	"embed"
)

//go:embed specs/*.yaml
var Content embed.FS
