package pagination

import (
	"testing"
	"time"

	pb "frameworks/pkg/proto"
)

func TestCursorEncodeDecode(t *testing.T) {
	tests := []struct {
		name      string
		timestamp time.Time
		id        string
	}{
		{
			name:      "basic cursor",
			timestamp: time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
			id:        "abc123",
		},
		{
			name:      "cursor with uuid",
			timestamp: time.Date(2024, 12, 7, 0, 55, 0, 0, time.UTC),
			id:        "550e8400-e29b-41d4-a716-446655440000",
		},
		{
			name:      "cursor with special chars in id",
			timestamp: time.Now().Truncate(time.Millisecond),
			id:        "stream_key_123",
		},
		{
			name:      "zero time",
			timestamp: time.Time{},
			id:        "id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Encode
			encoded := EncodeCursor(tt.timestamp, tt.id)
			if encoded == "" {
				t.Fatal("encoded cursor should not be empty")
			}

			// Decode
			cursor, err := DecodeCursor(encoded)
			if err != nil {
				t.Fatalf("failed to decode cursor: %v", err)
			}

			// Verify
			if !cursor.Timestamp.Equal(tt.timestamp) {
				t.Errorf("timestamp mismatch: got %v, want %v", cursor.Timestamp, tt.timestamp)
			}
			if cursor.ID != tt.id {
				t.Errorf("id mismatch: got %q, want %q", cursor.ID, tt.id)
			}
		})
	}
}

func TestCursorEncodeDecodeSortKey(t *testing.T) {
	sortKey := int64(5)
	id := "tier-basic"

	encoded := EncodeCursorWithSortKey(sortKey, id)
	if encoded == "" {
		t.Fatal("encoded cursor should not be empty")
	}

	cursor, err := DecodeCursor(encoded)
	if err != nil {
		t.Fatalf("failed to decode cursor: %v", err)
	}

	if got := cursor.GetSortKey(); got != sortKey {
		t.Errorf("sort key mismatch: got %d, want %d", got, sortKey)
	}
	if cursor.ID != id {
		t.Errorf("id mismatch: got %q, want %q", cursor.ID, id)
	}
}

func TestDecodeCursorErrors(t *testing.T) {
	tests := []struct {
		name    string
		encoded string
		wantErr bool
	}{
		{
			name:    "empty cursor",
			encoded: "",
			wantErr: false, // nil cursor, no error
		},
		{
			name:    "invalid base64",
			encoded: "not-valid-base64!!!",
			wantErr: true,
		},
		{
			name:    "wrong format - no ts prefix",
			encoded: "aWQ6YWJjMTIz", // base64("id:abc123")
			wantErr: true,
		},
		{
			name:    "wrong format - no id segment",
			encoded: "dHM6MTcwNDI3MzgwMDAwMA==", // base64("ts:1704273800000")
			wantErr: true,
		},
		{
			name:    "invalid timestamp",
			encoded: "dHM6bm90YW51bWJlcjppZDphYmM=", // base64("ts:notanumber:id:abc")
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cursor, err := DecodeCursor(tt.encoded)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if tt.encoded == "" && cursor != nil {
					t.Error("empty input should return nil cursor")
				}
			}
		})
	}
}

func TestClampLimit(t *testing.T) {
	tests := []struct {
		input    int
		expected int
	}{
		{0, DefaultLimit},
		{-1, DefaultLimit},
		{1, 1},
		{50, 50},
		{500, 500},
		{501, MaxLimit},
		{1000, MaxLimit},
	}

	for _, tt := range tests {
		result := ClampLimit(tt.input)
		if result != tt.expected {
			t.Errorf("ClampLimit(%d) = %d, want %d", tt.input, result, tt.expected)
		}
	}
}

func TestParse(t *testing.T) {
	validCursor := EncodeCursor(time.Now(), "test-id")

	tests := []struct {
		name      string
		req       *pb.CursorPaginationRequest
		wantLimit int
		wantDir   Direction
		wantErr   bool
	}{
		{
			name:      "nil request",
			req:       nil,
			wantLimit: DefaultLimit,
			wantDir:   Forward,
		},
		{
			name:      "default limit, no cursor",
			req:       &pb.CursorPaginationRequest{First: 0},
			wantLimit: DefaultLimit,
			wantDir:   Forward,
		},
		{
			name:      "custom limit, no cursor",
			req:       &pb.CursorPaginationRequest{First: 25},
			wantLimit: 25,
			wantDir:   Forward,
		},
		{
			name:      "with valid cursor",
			req:       &pb.CursorPaginationRequest{First: 10, After: &validCursor},
			wantLimit: 10,
			wantDir:   Forward,
		},
		{
			name:      "with invalid cursor",
			req:       &pb.CursorPaginationRequest{First: 10, After: strPtr("invalid-cursor")},
			wantLimit: 0,
			wantErr:   true,
		},
		{
			name:      "limit over max",
			req:       &pb.CursorPaginationRequest{First: 1000},
			wantLimit: MaxLimit,
			wantDir:   Forward,
		},
		{
			name:      "backward pagination with last",
			req:       &pb.CursorPaginationRequest{Last: 20},
			wantLimit: 20,
			wantDir:   Backward,
		},
		{
			name:      "backward pagination with before cursor",
			req:       &pb.CursorPaginationRequest{Last: 15, Before: &validCursor},
			wantLimit: 15,
			wantDir:   Backward,
		},
		{
			name:      "backward takes precedence over forward",
			req:       &pb.CursorPaginationRequest{First: 10, Last: 20},
			wantLimit: 20,
			wantDir:   Backward,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params, err := Parse(tt.req)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if params.Limit != tt.wantLimit {
				t.Errorf("limit = %d, want %d", params.Limit, tt.wantLimit)
			}
			if params.Direction != tt.wantDir {
				t.Errorf("direction = %d, want %d", params.Direction, tt.wantDir)
			}
		})
	}
}

func strPtr(s string) *string {
	return &s
}

func TestParseBidirectional(t *testing.T) {
	validCursor := EncodeCursor(time.Now(), "test-id")

	tests := []struct {
		name          string
		first         int32
		after         *string
		last          int32
		before        *string
		wantLimit     int
		wantDirection Direction
		wantCursor    bool
		wantErr       bool
	}{
		{
			name:          "nil request",
			wantLimit:     DefaultLimit,
			wantDirection: Forward,
		},
		{
			name:          "forward pagination - first only",
			first:         25,
			wantLimit:     25,
			wantDirection: Forward,
		},
		{
			name:          "forward pagination - first and after",
			first:         10,
			after:         &validCursor,
			wantLimit:     10,
			wantDirection: Forward,
			wantCursor:    true,
		},
		{
			name:          "backward pagination - last only",
			last:          20,
			wantLimit:     20,
			wantDirection: Backward,
		},
		{
			name:          "backward pagination - last and before",
			last:          15,
			before:        &validCursor,
			wantLimit:     15,
			wantDirection: Backward,
			wantCursor:    true,
		},
		{
			name:          "backward takes precedence over forward",
			first:         10,
			after:         &validCursor,
			last:          20,
			before:        &validCursor,
			wantLimit:     20,
			wantDirection: Backward,
			wantCursor:    true,
		},
		{
			name:    "invalid after cursor",
			first:   10,
			after:   strPtr("invalid"),
			wantErr: true,
		},
		{
			name:    "invalid before cursor",
			last:    10,
			before:  strPtr("invalid"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req *pb.CursorPaginationRequest
			if tt.first > 0 || tt.after != nil || tt.last > 0 || tt.before != nil {
				req = &pb.CursorPaginationRequest{
					First:  tt.first,
					After:  tt.after,
					Last:   tt.last,
					Before: tt.before,
				}
			}

			params, err := Parse(req)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if params.Limit != tt.wantLimit {
				t.Errorf("limit = %d, want %d", params.Limit, tt.wantLimit)
			}
			if params.Direction != tt.wantDirection {
				t.Errorf("direction = %v, want %v", params.Direction, tt.wantDirection)
			}
			if tt.wantCursor && params.Cursor == nil {
				t.Error("expected cursor, got nil")
			}
			if !tt.wantCursor && params.Cursor != nil {
				t.Error("expected nil cursor, got non-nil")
			}
		})
	}
}

func TestKeysetBuilder(t *testing.T) {
	builder := &KeysetBuilder{
		TimestampColumn: "created_at",
		IDColumn:        "id",
	}

	cursor := &Cursor{
		Timestamp: time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
		ID:        "abc123",
	}

	t.Run("forward condition", func(t *testing.T) {
		params := &Params{Direction: Forward, Cursor: cursor}
		condition, args := builder.Condition(params, 3)

		if condition != "(created_at, id) < ($3, $4)" {
			t.Errorf("condition = %q, want %q", condition, "(created_at, id) < ($3, $4)")
		}
		if len(args) != 2 {
			t.Errorf("args len = %d, want 2", len(args))
		}
	})

	t.Run("backward condition", func(t *testing.T) {
		params := &Params{Direction: Backward, Cursor: cursor}
		condition, args := builder.Condition(params, 1)

		if condition != "(created_at, id) > ($1, $2)" {
			t.Errorf("condition = %q, want %q", condition, "(created_at, id) > ($1, $2)")
		}
		if len(args) != 2 {
			t.Errorf("args len = %d, want 2", len(args))
		}
	})

	t.Run("nil cursor", func(t *testing.T) {
		params := &Params{Direction: Forward, Cursor: nil}
		condition, args := builder.Condition(params, 1)

		if condition != "" {
			t.Errorf("condition should be empty for nil cursor, got %q", condition)
		}
		if args != nil {
			t.Errorf("args should be nil for nil cursor")
		}
	})

	t.Run("forward order by", func(t *testing.T) {
		params := &Params{Direction: Forward}
		orderBy := builder.OrderBy(params)

		if orderBy != "ORDER BY created_at DESC, id DESC" {
			t.Errorf("orderBy = %q", orderBy)
		}
	})

	t.Run("backward order by", func(t *testing.T) {
		params := &Params{Direction: Backward}
		orderBy := builder.OrderBy(params)

		if orderBy != "ORDER BY created_at ASC, id ASC" {
			t.Errorf("orderBy = %q", orderBy)
		}
	})
}
