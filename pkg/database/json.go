package database

// JSONText adapts marshaled JSON ([]byte from json.Marshal / protojson.Marshal /
// json.RawMessage) for binding to json/jsonb parameters. The pgx stdlib driver
// wire-encodes []byte as bytea, which jsonb rejects ("invalid input syntax for
// type json"); strings are sent as text, which jsonb accepts. nil/empty input
// binds SQL NULL — the same as a nil []byte under lib/pq — so
// COALESCE($n::jsonb, ...) sites keep their NULL semantics. Do NOT use this for
// true BYTEA columns; those must keep binding []byte.
func JSONText(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}
