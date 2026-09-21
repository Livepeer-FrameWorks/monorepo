package ledger

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Arrays cross the driver boundary as text: a parameter is a PostgreSQL array
// literal cast in SQL with (($n)::text)::text[], and a column is read as
// array_to_json(...)::text. Both the pgx and lib/pq drivers pass text through
// unchanged, so the same SQL runs under either.

// arrayLiteral renders values as a PostgreSQL array literal with every element
// quoted.
func arrayLiteral(values []string) string {
	var b strings.Builder
	b.WriteByte('{')
	for i, v := range values {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('"')
		for _, r := range v {
			if r == '"' || r == '\\' {
				b.WriteByte('\\')
			}
			b.WriteRune(r)
		}
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}

// jsonStrings decodes an array_to_json(...)::text column.
func jsonStrings(raw string) ([]string, error) {
	if raw == "" {
		return nil, nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("decode text array: %w", err)
	}
	return out, nil
}
