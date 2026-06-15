package database

import (
	"database/sql"
	"sync"

	"github.com/yugabyte/pgx/v5/pgtype"
)

// pgtype.Map memoizes scan plans on first use and is therefore not safe for
// concurrent modification. Each scan borrows an exclusively-owned map from the
// pool for the duration of its Scan call, then returns it.
var pgTypeMapPool = sync.Pool{New: func() any { return pgtype.NewMap() }}

// ArrayScan returns an sql.Scanner that decodes a PostgreSQL array column into
// dest, a pointer to a Go slice (e.g. *[]string, *[]int64). It is required
// because the pgx stdlib driver hands array columns to database/sql in a form
// that lib/pq's array types and bare slice pointers cannot Scan (jackc/pgx
// #1556). Use it at every former `pq.Array(&slice)` / `pq.StringArray` scan
// site: rows.Scan(..., database.ArrayScan(&mySlice)).
//
// BINDING is unchanged and intentionally still uses `pq.Array(value)` /
// `pq.StringArray{...}` literals: those are driver.Valuers emitting the Postgres
// `{...}` text form, which the pgx stdlib driver accepts (it passes driver.Valuer
// args through) AND which go-sqlmock matches in tests. Only scanning needed a
// wrapper.
func ArrayScan(dest any) sql.Scanner {
	m, ok := pgTypeMapPool.Get().(*pgtype.Map)
	if !ok {
		m = pgtype.NewMap()
	}
	return &pooledArrayScanner{m: m, inner: m.SQLScanner(dest)}
}

type pooledArrayScanner struct {
	m     *pgtype.Map
	inner sql.Scanner
	done  bool
}

func (s *pooledArrayScanner) Scan(src any) error {
	// database/sql invokes Scan once per scanner; return the map to the pool
	// after that single use so it is never shared while in flight.
	if !s.done {
		s.done = true
		defer pgTypeMapPool.Put(s.m)
	}
	return s.inner.Scan(src)
}
