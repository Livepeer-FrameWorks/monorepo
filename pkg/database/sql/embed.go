package sql

import (
	"embed"
)

//go:embed schema/*.sql
//go:embed seeds/demo/*.sql
//go:embed clickhouse/*.sql
//go:embed all:migrations
//go:embed all:clickhouse/migrations
var Content embed.FS
