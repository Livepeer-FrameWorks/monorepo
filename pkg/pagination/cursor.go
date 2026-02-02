// Package pagination provides cursor-based pagination utilities for gRPC services.
// Cursors encode a stable position using a timestamp (or explicit sort key) + ID for keyset pagination.
// Supports bidirectional pagination (forward with first/after, backward with last/before).
package pagination

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"

	pb "frameworks/pkg/proto"
)

const (
	// DefaultLimit is the default page size if not specified
	DefaultLimit = 50
	// MaxLimit is the maximum allowed page size
	MaxLimit = 500
)

// Cursor represents a stable pagination position.
// Uses timestamp (or an explicit sort key) + ID for keyset pagination.
type Cursor struct {
	Timestamp time.Time
	ID        string
	// SortKey holds raw int64 for sk: prefixed cursors (avoids time.UnixMilli overflow)
	SortKey   int64
	IsSortKey bool
}

// GetSortKey returns the sort key value. For sk: cursors, returns the raw int64.
// For ts: cursors, returns the timestamp as milliseconds (legacy behavior).
func (c *Cursor) GetSortKey() int64 {
	if c.IsSortKey {
		return c.SortKey
	}
	return c.Timestamp.UnixMilli()
}

// Encode serializes the cursor to an opaque string for clients.
// Format: base64("ts:{timestamp_ms}:id:{id}")
func (c Cursor) Encode() string {
	raw := fmt.Sprintf("ts:%d:id:%s", c.Timestamp.UnixMilli(), c.ID)
	return base64.StdEncoding.EncodeToString([]byte(raw))
}

// DecodeCursor parses an encoded cursor string.
// Returns an error if the cursor format is invalid.
func DecodeCursor(encoded string) (*Cursor, error) {
	if encoded == "" {
		return nil, nil
	}

	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("invalid cursor encoding: %w", err)
	}

	raw := string(data)

	// Parse "ts:{timestamp_ms}:id:{id}" or "sk:{sort_key}:id:{id}"
	isSortKey := false
	var prefix string
	switch {
	case strings.HasPrefix(raw, "ts:"):
		prefix = "ts:"
	case strings.HasPrefix(raw, "sk:"):
		prefix = "sk:"
		isSortKey = true
	default:
		return nil, fmt.Errorf("invalid cursor format: missing ts/sk prefix")
	}

	parts := strings.SplitN(raw[len(prefix):], ":id:", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid cursor format: missing id segment")
	}

	keyValue, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid cursor key: %w", err)
	}

	cursor := &Cursor{
		ID:        parts[1],
		IsSortKey: isSortKey,
	}

	if isSortKey {
		// Store raw int64 directly to avoid time.UnixMilli overflow for large values
		cursor.SortKey = keyValue
	} else {
		cursor.Timestamp = time.UnixMilli(keyValue)
	}

	return cursor, nil
}

// EncodeCursor is a convenience function to create and encode a cursor.
func EncodeCursor(timestamp time.Time, id string) string {
	return Cursor{Timestamp: timestamp, ID: id}.Encode()
}

// EncodeCursorWithSortKey creates a cursor using a non-time sort key.
// Format: base64("sk:{sort_key}:id:{id}")
func EncodeCursorWithSortKey(sortKey int64, id string) string {
	raw := fmt.Sprintf("sk:%d:id:%s", sortKey, id)
	return base64.StdEncoding.EncodeToString([]byte(raw))
}

// ClampLimit ensures limit is within valid bounds.
func ClampLimit(limit int) int {
	if limit <= 0 {
		return DefaultLimit
	}
	if limit > MaxLimit {
		return MaxLimit
	}
	return limit
}

// Direction indicates the pagination direction.
type Direction int

const (
	// Forward pagination: newest first, navigate to older items (first/after)
	Forward Direction = iota
	// Backward pagination: oldest first in query, then reverse (last/before)
	Backward
)

// Params holds parsed pagination parameters for bidirectional pagination.
type Params struct {
	Limit     int
	Cursor    *Cursor
	Direction Direction
}

// Parse parses a CursorPaginationRequest for bidirectional pagination.
// If both forward (first/after) and backward (last/before) are provided, backward takes precedence.
func Parse(req *pb.CursorPaginationRequest) (*Params, error) {
	params := &Params{
		Limit:     DefaultLimit,
		Direction: Forward,
	}

	if req == nil {
		return params, nil
	}

	// Check for backward pagination (last/before takes precedence)
	if req.Last > 0 {
		params.Direction = Backward
		params.Limit = ClampLimit(int(req.Last))
		if req.Before != nil && *req.Before != "" {
			cursor, err := DecodeCursor(*req.Before)
			if err != nil {
				return nil, fmt.Errorf("invalid before cursor: %w", err)
			}
			params.Cursor = cursor
		}
		return params, nil
	}

	// Forward pagination (first/after)
	if req.First > 0 {
		params.Limit = ClampLimit(int(req.First))
	}
	if req.After != nil && *req.After != "" {
		cursor, err := DecodeCursor(*req.After)
		if err != nil {
			return nil, fmt.Errorf("invalid after cursor: %w", err)
		}
		params.Cursor = cursor
	}

	return params, nil
}

// KeysetBuilder helps construct keyset pagination SQL queries.
// It generates WHERE conditions and ORDER BY clauses for both directions.
type KeysetBuilder struct {
	// TimestampColumn is the column name for the timestamp (e.g., "created_at")
	TimestampColumn string
	// IDColumn is the column name for the unique ID (e.g., "id", "internal_name")
	IDColumn string
}

// Condition returns a SQL WHERE clause fragment for keyset pagination.
// Returns empty string and nil args if no cursor is provided.
// The placeholder style uses $N for PostgreSQL.
func (b *KeysetBuilder) Condition(params *Params, startArgIdx int) (string, []interface{}) {
	if params.Cursor == nil {
		return "", nil
	}

	if params.Direction == Forward {
		// Forward: WHERE (ts, id) < ($cursor_ts, $cursor_id)
		// This fetches items BEFORE the cursor position (older items when sorted DESC)
		return fmt.Sprintf("(%s, %s) < ($%d, $%d)",
				b.TimestampColumn, b.IDColumn, startArgIdx, startArgIdx+1),
			[]interface{}{params.Cursor.Timestamp, params.Cursor.ID}
	}

	// Backward: WHERE (ts, id) > ($cursor_ts, $cursor_id)
	// This fetches items AFTER the cursor position (newer items when sorted ASC)
	return fmt.Sprintf("(%s, %s) > ($%d, $%d)",
			b.TimestampColumn, b.IDColumn, startArgIdx, startArgIdx+1),
		[]interface{}{params.Cursor.Timestamp, params.Cursor.ID}
}

// OrderBy returns a SQL ORDER BY clause for keyset pagination.
func (b *KeysetBuilder) OrderBy(params *Params) string {
	if params.Direction == Forward {
		// Forward: newest first
		return fmt.Sprintf("ORDER BY %s DESC, %s DESC", b.TimestampColumn, b.IDColumn)
	}
	// Backward: oldest first in query (will be reversed in application layer)
	return fmt.Sprintf("ORDER BY %s ASC, %s ASC", b.TimestampColumn, b.IDColumn)
}

// BuildResponse constructs a CursorPaginationResponse from query results.
// Parameters:
//   - resultsLen: slice length (after fetch, before trimming)
//   - limit: requested limit
//   - direction: pagination direction
//   - totalCount: total number of items (from COUNT query)
//   - startCursor: cursor of first item in trimmed results
//   - endCursor: cursor of last item in trimmed results
func BuildResponse(resultsLen, limit int, direction Direction, totalCount int32, startCursor, endCursor string) *pb.CursorPaginationResponse {
	hasMore := resultsLen > limit

	resp := &pb.CursorPaginationResponse{
		TotalCount: totalCount,
	}

	if startCursor != "" {
		resp.StartCursor = &startCursor
	}
	if endCursor != "" {
		resp.EndCursor = &endCursor
	}

	if direction == Forward {
		resp.HasNextPage = hasMore
		// HasPreviousPage is true if we have a cursor (not on first page)
		resp.HasPreviousPage = startCursor != "" && endCursor != ""
	} else {
		resp.HasPreviousPage = hasMore
		// HasNextPage is true if we have a cursor (not on last page)
		resp.HasNextPage = startCursor != "" && endCursor != ""
	}

	return resp
}
