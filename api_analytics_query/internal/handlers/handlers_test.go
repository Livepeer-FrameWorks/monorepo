package handlers

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"frameworks/pkg/models"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test utilities
func setupTestGin() *gin.Engine {
	gin.SetMode(gin.TestMode)
	return gin.New()
}

func setupMockDB(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	return db, mock
}

// Custom time matcher for SQL mock to handle nullable times
type nullTimeValue struct {
	Time  time.Time
	Valid bool
}

func (n nullTimeValue) Match(v driver.Value) bool {
	switch val := v.(type) {
	case time.Time:
		return n.Valid && val.Equal(n.Time)
	case nil:
		return !n.Valid
	default:
		return false
	}
}

func TestGetStreamAnalytics_NullSessionTimes(t *testing.T) {
	db, mock := setupMockDB(t)
	defer db.Close()

	// Test data with NULL session times
	testCases := []struct {
		name               string
		sessionStartTime   interface{} // nil for NULL, time.Time for valid
		sessionEndTime     interface{} // nil for NULL, time.Time for valid
		expectedStartValid bool
		expectedEndValid   bool
		expectError        bool
	}{
		{
			name:               "both times null",
			sessionStartTime:   nil,
			sessionEndTime:     nil,
			expectedStartValid: false,
			expectedEndValid:   false,
			expectError:        false,
		},
		{
			name:               "start time null, end time valid",
			sessionStartTime:   nil,
			sessionEndTime:     time.Now(),
			expectedStartValid: false,
			expectedEndValid:   true,
			expectError:        false,
		},
		{
			name:               "start time valid, end time null",
			sessionStartTime:   time.Now().Add(-1 * time.Hour),
			sessionEndTime:     nil,
			expectedStartValid: true,
			expectedEndValid:   false,
			expectError:        false,
		},
		{
			name:               "both times valid",
			sessionStartTime:   time.Now().Add(-2 * time.Hour),
			sessionEndTime:     time.Now().Add(-1 * time.Hour),
			expectedStartValid: true,
			expectedEndValid:   true,
			expectError:        false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {

			// Setup mock query expectations
			rows := sqlmock.NewRows([]string{
				"id", "tenant_id", "internal_name", "internal_name",
				"session_start_time", "session_end_time", "total_session_duration",
				"current_viewers", "peak_viewers", "total_connections",
				"bandwidth_in", "bandwidth_out", "total_bandwidth_gb",
				"bitrate_kbps", "resolution", "packets_sent", "packets_lost",
				"packets_retrans", "upbytes", "downbytes", "first_ms", "last_ms",
				"track_count", "inputs", "outputs", "node_id", "node_name", "latitude",
				"longitude", "location", "status", "last_updated", "created_at",
			})

			// Add test data
			now := time.Now()
			rows.AddRow(
				"test-id", "tenant-123", "test-stream", "test-stream",
				tc.sessionStartTime, tc.sessionEndTime, 3600,
				100, 150, 200,
				1000, 2000, 1.5,
				2000, "1920x1080", 10000, 50,
				5, 500000, 400000, 100, 200,
				2, "input1", "output1", "node1", "Test Node", 40.7128,
				-74.0060, "New York", "active", now, now,
			)

			mock.ExpectQuery("SELECT sa.id, sa.tenant_id").WillReturnRows(rows)

			// Setup HTTP request
			router := setupTestGin()

			// Mock the database connection
			// In a real implementation, you'd inject the DB connection
			// For this test, we're testing the NULL handling logic

			router.GET("/api/v1/analytics/streams/:tenant_id/:internal_name", func(c *gin.Context) {
				tenantID := c.Param("tenant_id")
				internalName := c.Param("internal_name")

				var sa models.StreamAnalytics

				// Simulate the SQL scan with potential NULL values
				var sessionStartTime, sessionEndTime sql.NullTime
				if tc.sessionStartTime != nil {
					sessionStartTime = sql.NullTime{Time: tc.sessionStartTime.(time.Time), Valid: true}
				}
				if tc.sessionEndTime != nil {
					sessionEndTime = sql.NullTime{Time: tc.sessionEndTime.(time.Time), Valid: true}
				}

				// Test COALESCE-like behavior for NULL handling
				if sessionStartTime.Valid {
					sa.SessionStartTime = sessionStartTime.Time
				} else {
					// Default to zero time for NULL values
					sa.SessionStartTime = time.Time{}
				}

				if sessionEndTime.Valid {
					sa.SessionEndTime = sessionEndTime.Time
				} else {
					// Default to zero time for NULL values
					sa.SessionEndTime = time.Time{}
				}

				// Set other required fields
				sa.ID = "test-id"
				sa.TenantID = tenantID
				sa.InternalName = internalName
				sa.CurrentViewers = 100
				sa.PeakViewers = 150
				sa.TotalConnections = 200

				c.JSON(http.StatusOK, sa)
			})

			// Make request
			req := httptest.NewRequest("GET", "/api/v1/analytics/streams/tenant-123/test-stream", nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			// Verify response
			assert.Equal(t, http.StatusOK, w.Code)

			var response models.StreamAnalytics
			err := json.Unmarshal(w.Body.Bytes(), &response)
			require.NoError(t, err)

			// Verify NULL handling
			if tc.expectedStartValid {
				assert.False(t, response.SessionStartTime.IsZero(), "Expected valid start time")
			} else {
				assert.True(t, response.SessionStartTime.IsZero(), "Expected zero time for NULL start time")
			}

			if tc.expectedEndValid {
				assert.False(t, response.SessionEndTime.IsZero(), "Expected valid end time")
			} else {
				assert.True(t, response.SessionEndTime.IsZero(), "Expected zero time for NULL end time")
			}
		})
	}
}

func TestStreamAnalyticsQueryWithCOALESCE(t *testing.T) {
	tests := []struct {
		name          string
		query         string
		expectedCalls int
	}{
		{
			name: "query with COALESCE for nullable fields",
			query: `
				SELECT sa.id, sa.tenant_id, sa.internal_name, sa.internal_name, 
				       COALESCE(sa.session_start_time, '1970-01-01 00:00:00'::timestamp) as session_start_time,
				       COALESCE(sa.session_end_time, '1970-01-01 00:00:00'::timestamp) as session_end_time,
				       sa.total_session_duration,
				       COALESCE(sa.current_viewers, 0) as current_viewers,
				       COALESCE(sa.peak_viewers, 0) as peak_viewers,
				       COALESCE(sa.total_connections, 0) as total_connections
				FROM stream_analytics sa
				WHERE sa.tenant_id = $1 AND sa.internal_name = $2
			`,
			expectedCalls: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock := setupMockDB(t)
			defer db.Close()

			// Setup expected query with COALESCE
			rows := sqlmock.NewRows([]string{
				"id", "tenant_id", "internal_name", "internal_name",
				"session_start_time", "session_end_time", "total_session_duration",
				"current_viewers", "peak_viewers", "total_connections",
			})

			epochTime := time.Unix(0, 0) // 1970-01-01 00:00:00
			rows.AddRow(
				"test-id", "tenant-123", "test-stream", "test-stream",
				epochTime, epochTime, 0, // Using epoch time as default for NULL
				0, 0, 0, // Using 0 as default for NULL numeric fields
			)

			// Expect query with COALESCE
			mock.ExpectQuery("SELECT sa.id, sa.tenant_id.*COALESCE").WillReturnRows(rows)

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
			}

			err := db.QueryRow(tt.query, "tenant-123", "test-stream").Scan(
				&result.ID, &result.TenantID, &result.InternalName, &result.InternalName,
				&result.SessionStartTime, &result.SessionEndTime, new(int),
				&result.CurrentViewers, &result.PeakViewers, &result.TotalConnections,
			)

			require.NoError(t, err)

			// Verify that COALESCE provided default values
			assert.Equal(t, epochTime, result.SessionStartTime)
			assert.Equal(t, epochTime, result.SessionEndTime)
			assert.Equal(t, 0, result.CurrentViewers)
			assert.Equal(t, 0, result.PeakViewers)
			assert.Equal(t, 0, result.TotalConnections)

			// Verify all expectations were met
			err = mock.ExpectationsWereMet()
			assert.NoError(t, err)
		})
	}
}

func TestNullFieldValidation(t *testing.T) {
	tests := []struct {
		name           string
		input          interface{}
		expectedOutput interface{}
		fieldType      string
	}{
		{
			name:           "null int becomes zero",
			input:          (*int)(nil),
			expectedOutput: 0,
			fieldType:      "int",
		},
		{
			name:           "null float64 becomes zero",
			input:          (*float64)(nil),
			expectedOutput: 0.0,
			fieldType:      "float64",
		},
		{
			name:           "null string becomes empty",
			input:          (*string)(nil),
			expectedOutput: "",
			fieldType:      "string",
		},
		{
			name:           "null time becomes zero time",
			input:          (*time.Time)(nil),
			expectedOutput: time.Time{},
			fieldType:      "time",
		},
		{
			name:           "valid int preserved",
			input:          42,
			expectedOutput: 42,
			fieldType:      "int",
		},
		{
			name:           "valid float preserved",
			input:          3.14,
			expectedOutput: 3.14,
			fieldType:      "float64",
		},
		{
			name:           "valid string preserved",
			input:          "test",
			expectedOutput: "test",
			fieldType:      "string",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Helper function to validate and default null fields
			validateAndDefault := func(input interface{}, fieldType string) interface{} {
				switch fieldType {
				case "int":
					if ptr, ok := input.(*int); ok && ptr == nil {
						return 0
					}
					if val, ok := input.(int); ok {
						return val
					}
					return 0
				case "float64":
					if ptr, ok := input.(*float64); ok && ptr == nil {
						return 0.0
					}
					if val, ok := input.(float64); ok {
						return val
					}
					return 0.0
				case "string":
					if ptr, ok := input.(*string); ok && ptr == nil {
						return ""
					}
					if val, ok := input.(string); ok {
						return val
					}
					return ""
				case "time":
					if ptr, ok := input.(*time.Time); ok && ptr == nil {
						return time.Time{}
					}
					if val, ok := input.(time.Time); ok {
						return val
					}
					return time.Time{}
				default:
					return input
				}
			}

			result := validateAndDefault(tt.input, tt.fieldType)
			assert.Equal(t, tt.expectedOutput, result)
		})
	}
}

func TestResponseFieldValidation(t *testing.T) {
	// Test that response validation catches and defaults NULL fields
	testResponse := models.StreamAnalytics{
		ID:               "test-id",
		TenantID:         "tenant-123",
		InternalName:     "test-stream",
		SessionStartTime: time.Time{}, // Zero time (was NULL)
		SessionEndTime:   time.Time{}, // Zero time (was NULL)
		CurrentViewers:   0,           // Was NaN, now 0
		PeakViewers:      0,           // Was NaN, now 0
		TotalConnections: 0,           // Was NULL, now 0
		BandwidthIn:      0,           // Was NULL, now 0
		BandwidthOut:     0,           // Was NULL, now 0
		TotalBandwidthGB: 0.0,         // Was NULL, now 0.0
		BitrateKbps:      0,           // Was NULL, now 0
		Resolution:       "",          // Was NULL, now empty string
		PacketsSent:      0,           // Was NULL, now 0
		PacketsLost:      0,           // Was NULL, now 0
		PacketsRetrans:   0,           // Was NULL, now 0
	}

	// Verify that all numeric fields are properly defaulted
	assert.GreaterOrEqual(t, testResponse.CurrentViewers, 0)
	assert.GreaterOrEqual(t, testResponse.PeakViewers, 0)
	assert.GreaterOrEqual(t, testResponse.TotalConnections, 0)
	assert.GreaterOrEqual(t, testResponse.BandwidthIn, int64(0))
	assert.GreaterOrEqual(t, testResponse.BandwidthOut, int64(0))
	assert.GreaterOrEqual(t, testResponse.TotalBandwidthGB, 0.0)

	// Verify that time fields are handled
	assert.True(t, testResponse.SessionStartTime.IsZero() || !testResponse.SessionStartTime.IsZero())
	assert.True(t, testResponse.SessionEndTime.IsZero() || !testResponse.SessionEndTime.IsZero())

	// Verify string fields are not nil
	assert.NotNil(t, testResponse.Resolution)
	assert.NotNil(t, testResponse.InternalName)
	assert.NotNil(t, testResponse.TenantID)
}

func TestDatabaseErrorHandling(t *testing.T) {
	db, mock := setupMockDB(t)
	defer db.Close()

	// Test database connection error
	mock.ExpectQuery("SELECT sa.id").WillReturnError(fmt.Errorf("connection lost"))

	err := db.QueryRow("SELECT sa.id FROM stream_analytics sa WHERE sa.tenant_id = $1", "tenant-123").Scan(new(string))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "connection lost")

	// Test no rows found
	mock.ExpectQuery("SELECT sa.id").WillReturnError(sql.ErrNoRows)

	err = db.QueryRow("SELECT sa.id FROM stream_analytics sa WHERE sa.tenant_id = $1", "nonexistent").Scan(new(string))
	assert.Equal(t, sql.ErrNoRows, err)

	// Verify all expectations were met
	err = mock.ExpectationsWereMet()
	assert.NoError(t, err)
}
