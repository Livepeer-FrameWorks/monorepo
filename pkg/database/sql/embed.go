package sql

import (
	"embed"
)

//go:embed schema/*.sql
//go:embed seeds/static/*.sql
//go:embed seeds/demo/*.sql
//go:embed clickhouse/*.sql
var Content embed.FS
