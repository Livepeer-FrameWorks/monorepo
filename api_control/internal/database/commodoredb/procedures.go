package commodoredb

import "context"

const createUserStreamProcedure = `
SELECT stream_id, stream_key, playback_id, internal_name
FROM commodore.create_user_stream($1, $2, $3)
`

type CreateUserStreamProcedureParams struct {
	TenantID string
	UserID   string
	Title    string
}

type CreateUserStreamProcedureRow struct {
	StreamID     string
	StreamKey    string
	PlaybackID   string
	InternalName string
}

// CreateUserStreamProcedure is the typed boundary for the PostgreSQL
// set-returning function that sqlc cannot describe as a composite result.
func (q *Queries) CreateUserStreamProcedure(ctx context.Context, arg CreateUserStreamProcedureParams) (CreateUserStreamProcedureRow, error) {
	row := q.db.QueryRowContext(ctx, createUserStreamProcedure, arg.TenantID, arg.UserID, arg.Title)
	var result CreateUserStreamProcedureRow
	err := row.Scan(&result.StreamID, &result.StreamKey, &result.PlaybackID, &result.InternalName)
	return result, err
}
