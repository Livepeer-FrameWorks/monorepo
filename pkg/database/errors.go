package database

import (
	"errors"

	"github.com/lib/pq"
	"github.com/yugabyte/pgx/v5/pgconn"
)

// SQLState extracts the PostgreSQL SQLSTATE code from a database error,
// independent of the underlying driver. Runtime services use the YugabyteDB
// smart driver (errors surface as *pgconn.PgError); the *pq.Error branch keeps
// callers correct for any residual lib/pq path and during transition. Returns
// "" when the error is not a recognized Postgres server error.
func SQLState(err error) string {
	if err == nil {
		return ""
	}
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
		return pgErr.Code
	}
	if pqErr, ok := errors.AsType[*pq.Error](err); ok {
		return string(pqErr.Code)
	}
	return ""
}
