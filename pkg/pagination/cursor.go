// Package pagination provides cursor-based pagination utilities for gRPC services.
// Cursors encode a stable position using timestamp + ID for keyset pagination.
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
// Uses timestamp + ID for keyset pagination (stable across inserts/deletes).
type Cursor struct {
	Timestamp time.Time
	ID        string
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

	// Parse "ts:{timestamp_ms}:id:{id}"
	if !strings.HasPrefix(raw, "ts:") {
		return nil, fmt.Errorf("invalid cursor format: missing ts prefix")
	}

	parts := strings.SplitN(raw[3:], ":id:", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid cursor format: missing id segment")
	}

	tsMs, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid cursor timestamp: %w", err)
	}

	return &Cursor{
		Timestamp: time.UnixMilli(tsMs),
		ID:        parts[1],
	}, nil
}

// EncodeCursor is a convenience function to create and encode a cursor.
func EncodeCursor(timestamp time.Time, id string) string {
	return Cursor{Timestamp: timestamp, ID: id}.Encode()
}

// EncodeIndexCursor creates an index-based cursor for legacy offset pagination.
// This is a temporary fallback during migration to keyset pagination.
// TODO: Remove once all resolvers are migrated to keyset pagination.
func EncodeIndexCursor(index int) string {
	raw := fmt.Sprintf("idx:%d", index)
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
