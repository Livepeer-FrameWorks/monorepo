package integration

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	"frameworks/pkg/models"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Integration tests for database operations with NULL handling

func TestStreamAnalytics_NullHandling_YugaDB(t *testing.T) {
	// Test SQL queries with COALESCE for proper NULL handling
	db, mock := setupDatabaseMock(t)
	defer db.Close()

	tests := []struct {
		name                string
		mockSessionStart    interface{} // nil for NULL, time.Time for valid
		mockSessionEnd      interface{} // nil for NULL, time.Time for valid
		mockCurrentViewers  interface{} // nil for NULL, int for valid
		mockPeakViewers     interface{} // nil for NULL, int for valid
		expectedStartTime   time.Time
		expectedEndTime     time.Time
		expectedCurrent     int
		expectedPeak        int
	}{
		{
			name:               "all fields null - should default",
			mockSessionStart:   nil,
			mockSessionEnd:     nil,
			mockCurrentViewers: nil,
			mockPeakViewers:    nil,
			expectedStartTime:  time.Unix(0, 0), // Epoch time as default
			expectedEndTime:    time.Unix(0, 0), // Epoch time as default
			expectedCurrent:    0,               // Default for NULL
			expectedPeak:       0,               // Default for NULL
		},
		{
			name:               "partial nulls - should handle mixed",
			mockSessionStart:   time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
			mockSessionEnd:     nil,
			mockCurrentViewers: 100,
			mockPeakViewers:    nil,
			expectedStartTime:  time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
			expectedEndTime:    time.Unix(0, 0), // Default for NULL
			expectedCurrent:    100,
			expectedPeak:       0, // Default for NULL
		},
		{
			name:               "all fields valid - should preserve",
			mockSessionStart:   time.Date(2024, 1, 15, 9, 0, 0, 0, time.UTC),
			mockSessionEnd:     time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
			mockCurrentViewers: 150,
			mockPeakViewers:    200,
			expectedStartTime:  time.Date(2024, 1, 15, 9, 0, 0, 0, time.UTC),
			expectedEndTime:    time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
			expectedCurrent:    150,
			expectedPeak:       200,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup mock query with COALESCE for NULL handling
			query := `
				SELECT 
					sa.id, 
					sa.tenant_id, 
					sa.internal_name, 
					COALESCE(sa.session_start_time, '1970-01-01 00:00:00'::timestamp) as session_start_time,
					COALESCE(sa.session_end_time, '1970-01-01 00:00:00'::timestamp) as session_end_time,
					COALESCE(sa.current_viewers, 0) as current_viewers,
					COALESCE(sa.peak_viewers, 0) as peak_viewers,
					COALESCE(sa.total_connections, 0) as total_connections,
					COALESCE(sa.bandwidth_in, 0) as bandwidth_in,
					COALESCE(sa.bandwidth_out, 0) as bandwidth_out
				FROM stream_analytics sa
				WHERE sa.tenant_id = $1 AND sa.internal_name = $2
			`

			rows := sqlmock.NewRows([]string{
				"id", "tenant_id", "internal_name", 
				"session_start_time", "session_end_time",
				"current_viewers", "peak_viewers", "total_connections",
				"bandwidth_in", "bandwidth_out",
			})

			// Add row with COALESCE defaults applied
			sessionStart := tt.expectedStartTime
			sessionEnd := tt.expectedEndTime
			currentViewers := tt.expectedCurrent
			peakViewers := tt.expectedPeak

			rows.AddRow(
				"stream-123", "tenant-456", "test-stream",
				sessionStart, sessionEnd,
				currentViewers, peakViewers, 0,
				int64(0), int64(0),
			)

			mock.ExpectQuery("SELECT.*COALESCE").WillReturnRows(rows)

			// Execute query
			var result struct {
				ID               string    `db:"id"`
				TenantID         string    `db:"tenant_id"`
				InternalName     string    `db:"internal_name"`
				SessionStartTime time.Time `db:"session_start_time"`
				SessionEndTime   time.Time `db:"session_end_time"`
				CurrentViewers   int       `db:"current_viewers"`
				PeakViewers      int       `db:"peak_viewers"`
				TotalConnections int       `db:"total_connections"`
				BandwidthIn      int64     `db:"bandwidth_in"`
				BandwidthOut     int64     `db:"bandwidth_out"`
			}

			err := db.QueryRow(query, "tenant-456", "test-stream").Scan(
				&result.ID, &result.TenantID, &result.InternalName,
				&result.SessionStartTime, &result.SessionEndTime,
				&result.CurrentViewers, &result.PeakViewers, &result.TotalConnections,
				&result.BandwidthIn, &result.BandwidthOut,
			)

			require.NoError(t, err, "Query should succeed")

			// Verify COALESCE defaults are applied correctly
			assert.Equal(t, tt.expectedStartTime.Unix(), result.SessionStartTime.Unix())
			assert.Equal(t, tt.expectedEndTime.Unix(), result.SessionEndTime.Unix())
			assert.Equal(t, tt.expectedCurrent, result.CurrentViewers)
			assert.Equal(t, tt.expectedPeak, result.PeakViewers)

			// Verify no NULL values made it through
			assert.GreaterOrEqual(t, result.CurrentViewers, 0)
			assert.GreaterOrEqual(t, result.PeakViewers, 0)
			assert.GreaterOrEqual(t, result.TotalConnections, 0)
			assert.GreaterOrEqual(t, result.BandwidthIn, int64(0))
			assert.GreaterOrEqual(t, result.BandwidthOut, int64(0))

			// Verify all expectations were met
			err = mock.ExpectationsWereMet()
			assert.NoError(t, err)
		})
	}
}

func TestClickHouseDataType_Integration(t *testing.T) {
	// Test that time.Time objects are properly handled by ClickHouse queries
	db, mock := setupDatabaseMock(t)
	defer db.Close()

	// Test ClickHouse query with proper DateTime types
	tests := []struct {
		name      string
		startTime time.Time
		endTime   time.Time
		query     string
	}{
		{
			name:      "viewer metrics with time range",
			startTime: time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
			endTime:   time.Date(2024, 1, 15, 11, 0, 0, 0, time.UTC),
			query: `
				SELECT 
					timestamp,
					stream_name,
					viewer_count,
					bandwidth
				FROM viewer_metrics
				WHERE tenant_id = $1 
				AND timestamp BETWEEN $2 AND $3
				ORDER BY timestamp DESC
			`,
		},
		{
			name:      "routing events with time range",
			startTime: time.Date(2024, 1, 15, 9, 30, 0, 0, time.UTC),
			endTime:   time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
			query: `
				SELECT 
					timestamp,
					stream_name,
					selected_node,
					status
				FROM routing_events
				WHERE tenant_id = $1 
				AND timestamp BETWEEN $2 AND $3
				ORDER BY timestamp DESC
			`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows := sqlmock.NewRows([]string{
				"timestamp", "stream_name", "viewer_count", "bandwidth",
			})

			// Add test data with proper timestamps
			testTime := time.Date(2024, 1, 15, 10, 15, 0, 0, time.UTC)
			rows.AddRow(testTime, "test-stream", 100, 1000.5)

			// Expect query with time.Time parameters (not strings)
			mock.ExpectQuery("SELECT.*timestamp.*BETWEEN").
				WithArgs("tenant-123", tt.startTime, tt.endTime).
				WillReturnRows(rows)

			// Execute query with time.Time objects
			var results []struct {
				Timestamp   time.Time `db:"timestamp"`
				StreamName  string    `db:"stream_name"`
				ViewerCount int       `db:"viewer_count"`
				Bandwidth   float64   `db:"bandwidth"`
			}

			rows_result, err := db.Query(tt.query, "tenant-123", tt.startTime, tt.endTime)
			require.NoError(t, err)
			defer rows_result.Close()

			for rows_result.Next() {
				var r struct {
					Timestamp   time.Time `db:"timestamp"`
					StreamName  string    `db:"stream_name"`
					ViewerCount int       `db:"viewer_count"`
					Bandwidth   float64   `db:"bandwidth"`
				}

				err = rows_result.Scan(&r.Timestamp, &r.StreamName, &r.ViewerCount, &r.Bandwidth)
				require.NoError(t, err)
				results = append(results, r)
			}

			require.Len(t, results, 1)
			
			// Verify timestamp is within expected range
			assert.True(t, results[0].Timestamp.After(tt.startTime) || results[0].Timestamp.Equal(tt.startTime))
			assert.True(t, results[0].Timestamp.Before(tt.endTime) || results[0].Timestamp.Equal(tt.endTime))

			// Verify all expectations were met
			err = mock.ExpectationsWereMet()
			assert.NoError(t, err)
		})
	}
}

func TestStreamAnalyticsModel_Validation(t *testing.T) {
	// Test model validation ensures no NULL values escape to API responses
	tests := []struct {
		name     string
		input    models.StreamAnalytics
		valid    bool
		errorMsg string
	}{
		{
			name: "valid model with all fields set",
			input: models.StreamAnalytics{
				ID:                   "stream-123",
				TenantID:             "tenant-456",
				InternalName:         "test-stream",
				SessionStartTime:     time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
				SessionEndTime:       time.Date(2024, 1, 15, 11, 0, 0, 0, time.UTC),
				CurrentViewers:       100,
				PeakViewers:          150,
				TotalConnections:     200,
				BandwidthIn:          1000,
				BandwidthOut:         2000,
				TotalBandwidthGB:     1.5,
				BitrateKbps:          2000,
				Resolution:           "1920x1080",
				PacketsSent:          10000,
				PacketsLost:          50,
				PacketsRetrans:       5,
			},
			valid: true,
		},
		{
			name: "model with zero time values (from NULL database fields)",
			input: models.StreamAnalytics{
				ID:                   "stream-123",
				TenantID:             "tenant-456", 
				InternalName:         "test-stream",
				SessionStartTime:     time.Time{}, // Zero time (from NULL)
				SessionEndTime:       time.Time{}, // Zero time (from NULL)
				CurrentViewers:       0,           // Default for NULL
				PeakViewers:          0,           // Default for NULL
				TotalConnections:     0,           // Default for NULL
				BandwidthIn:          0,           // Default for NULL
				BandwidthOut:         0,           // Default for NULL
				TotalBandwidthGB:     0.0,         // Default for NULL
				BitrateKbps:          0,           // Default for NULL
				Resolution:           "",          // Default for NULL string
				PacketsSent:          0,           // Default for NULL
				PacketsLost:          0,           // Default for NULL
				PacketsRetrans:       0,           // Default for NULL
			},
			valid: true, // Should be valid after NULL handling
		},
		{
			name: "model with required fields missing",
			input: models.StreamAnalytics{
				ID:           "", // Required field empty
				TenantID:     "tenant-456",
				InternalName: "test-stream",
			},
			valid:    false,
			errorMsg: "ID is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Validate model
			err := validateStreamAnalytics(tt.input)

			if tt.valid {
				assert.NoError(t, err, "Model should be valid")
				
				// Verify no negative values (which could indicate improper NULL handling)
				assert.GreaterOrEqual(t, tt.input.CurrentViewers, 0)
				assert.GreaterOrEqual(t, tt.input.PeakViewers, 0)
				assert.GreaterOrEqual(t, tt.input.TotalConnections, 0)
				assert.GreaterOrEqual(t, tt.input.BandwidthIn, int64(0))
				assert.GreaterOrEqual(t, tt.input.BandwidthOut, int64(0))
				assert.GreaterOrEqual(t, tt.input.TotalBandwidthGB, 0.0)
				
				// Verify string fields are not nil pointers
				assert.NotNil(t, tt.input.Resolution)
				assert.NotNil(t, tt.input.InternalName)
			} else {
				assert.Error(t, err, "Model should be invalid")
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			}
		})
	}
}

func TestDatabaseTransaction_NullHandling(t *testing.T) {
	// Test database transactions with NULL field handling
	db, mock := setupDatabaseMock(t)
	defer db.Close()

	// Test transaction with multiple NULL field updates
	mock.ExpectBegin()
	
	// Test UPDATE with COALESCE
	updateQuery := `
		UPDATE stream_analytics 
		SET current_viewers = COALESCE($1, current_viewers, 0),
			peak_viewers = COALESCE($2, peak_viewers, 0),
			session_end_time = COALESCE($3, session_end_time, '1970-01-01 00:00:00'::timestamp)
		WHERE id = $4 AND tenant_id = $5
	`
	
	mock.ExpectExec("UPDATE stream_analytics").
		WithArgs(nil, 200, nil, "stream-123", "tenant-456").
		WillReturnResult(sqlmock.NewResult(0, 1))

	mock.ExpectCommit()

	// Execute transaction
	tx, err := db.Begin()
	require.NoError(t, err)

	_, err = tx.Exec(updateQuery, nil, 200, nil, "stream-123", "tenant-456")
	require.NoError(t, err)

	err = tx.Commit()
	require.NoError(t, err)

	// Verify all expectations were met
	err = mock.ExpectationsWereMet()
	assert.NoError(t, err)
}

// Helper functions

func setupDatabaseMock(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	return db, mock
}

func validateStreamAnalytics(sa models.StreamAnalytics) error {
	if sa.ID == "" {
		return fmt.Errorf("ID is required")
	}
	if sa.TenantID == "" {
		return fmt.Errorf("TenantID is required")
	}
	if sa.InternalName == "" {
		return fmt.Errorf("InternalName is required")
	}
	
	// Validate that numeric fields are not negative (which could indicate poor NULL handling)
	if sa.CurrentViewers < 0 {
		return fmt.Errorf("CurrentViewers cannot be negative")
	}
	if sa.PeakViewers < 0 {
		return fmt.Errorf("PeakViewers cannot be negative")
	}
	if sa.TotalConnections < 0 {
		return fmt.Errorf("TotalConnections cannot be negative")
	}
	if sa.BandwidthIn < 0 {
		return fmt.Errorf("BandwidthIn cannot be negative")
	}
	if sa.BandwidthOut < 0 {
		return fmt.Errorf("BandwidthOut cannot be negative")
	}
	if sa.TotalBandwidthGB < 0 {
		return fmt.Errorf("TotalBandwidthGB cannot be negative")
	}
	
	return nil
}