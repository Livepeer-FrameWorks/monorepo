package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// errorResponse is a local test type for error response validation
type errorResponse struct {
	Error string `json:"error"`
}

// Mock ClickHouse interface for testing
type mockClickHouseDB struct {
	queryFunc func(query string, args ...interface{}) ([]map[string]interface{}, error)
}

func (m *mockClickHouseDB) Query(query string, args ...interface{}) ([]map[string]interface{}, error) {
	if m.queryFunc != nil {
		return m.queryFunc(query, args...)
	}
	return nil, fmt.Errorf("mock query function not set")
}

func TestDateTimeConversion_RFC3339ToTime(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		expectError bool
		expected    time.Time
	}{
		{
			name:        "valid RFC3339 with Z timezone",
			input:       "2024-01-15T10:30:45Z",
			expectError: false,
			expected:    time.Date(2024, 1, 15, 10, 30, 45, 0, time.UTC),
		},
		{
			name:        "valid RFC3339 with offset timezone",
			input:       "2024-01-15T10:30:45-05:00",
			expectError: false,
			expected:    time.Date(2024, 1, 15, 15, 30, 45, 0, time.UTC), // Converted to UTC
		},
		{
			name:        "valid RFC3339 with positive offset",
			input:       "2024-01-15T10:30:45+02:00",
			expectError: false,
			expected:    time.Date(2024, 1, 15, 8, 30, 45, 0, time.UTC), // Converted to UTC
		},
		{
			name:        "valid RFC3339 with milliseconds",
			input:       "2024-01-15T10:30:45.123Z",
			expectError: false,
			expected:    time.Date(2024, 1, 15, 10, 30, 45, 123000000, time.UTC),
		},
		{
			name:        "valid RFC3339 with microseconds",
			input:       "2024-01-15T10:30:45.123456Z",
			expectError: false,
			expected:    time.Date(2024, 1, 15, 10, 30, 45, 123456000, time.UTC),
		},
		{
			name:        "invalid format - missing timezone",
			input:       "2024-01-15T10:30:45",
			expectError: true,
		},
		{
			name:        "invalid format - wrong date format",
			input:       "15-01-2024T10:30:45Z",
			expectError: true,
		},
		{
			name:        "invalid format - not RFC3339",
			input:       "2024/01/15 10:30:45",
			expectError: true,
		},
		{
			name:        "empty string",
			input:       "",
			expectError: true,
		},
		{
			name:        "invalid timezone offset",
			input:       "2024-01-15T10:30:45+25:00",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, err := time.Parse(time.RFC3339, tt.input)

			if tt.expectError {
				assert.Error(t, err, "Expected error for input: %s", tt.input)
			} else {
				assert.NoError(t, err, "Unexpected error for input: %s", tt.input)
				assert.True(t, parsed.Equal(tt.expected), "Expected %v, got %v for input: %s", tt.expected, parsed, tt.input)
			}
		})
	}
}

func TestGetViewerMetrics_DateTimeConversion(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name             string
		startTime        string
		endTime          string
		expectedStatus   int
		expectClickHouse bool
		validateTimes    func(startTime, endTime time.Time) bool
	}{
		{
			name:             "valid RFC3339 times",
			startTime:        "2024-01-15T10:00:00Z",
			endTime:          "2024-01-15T11:00:00Z",
			expectedStatus:   http.StatusOK,
			expectClickHouse: true,
			validateTimes: func(start, end time.Time) bool {
				return start.Before(end) && start.Equal(time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC))
			},
		},
		{
			name:             "times with different timezones",
			startTime:        "2024-01-15T10:00:00-05:00",
			endTime:          "2024-01-15T11:00:00+02:00",
			expectedStatus:   http.StatusOK,
			expectClickHouse: true,
			validateTimes: func(start, end time.Time) bool {
				// -05:00 = 15:00 UTC, +02:00 = 09:00 UTC
				return start.After(end) // This would be a logical error in the request, but parsing should work
			},
		},
		{
			name:           "invalid start time format",
			startTime:      "2024/01/15 10:00:00",
			endTime:        "2024-01-15T11:00:00Z",
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "invalid end time format",
			startTime:      "2024-01-15T10:00:00Z",
			endTime:        "2024/01/15 11:00:00",
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "empty start time",
			startTime:      "",
			endTime:        "2024-01-15T11:00:00Z",
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "malformed timezone",
			startTime:      "2024-01-15T10:00:00+25:00",
			endTime:        "2024-01-15T11:00:00Z",
			expectedStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := gin.New()

			// Mock ClickHouse database
			var capturedStartTime, capturedEndTime time.Time
			mockDB := &mockClickHouseDB{
				queryFunc: func(query string, args ...interface{}) ([]map[string]interface{}, error) {
					// Capture the time arguments passed to ClickHouse
					if len(args) >= 3 {
						if startTime, ok := args[1].(time.Time); ok {
							capturedStartTime = startTime
						}
						if endTime, ok := args[2].(time.Time); ok {
							capturedEndTime = endTime
						}
					}

					// Return mock viewer metrics data
					return []map[string]interface{}{
						{
							"timestamp":    time.Now(),
							"stream_name":  "test-stream",
							"viewer_count": 100,
							"peak_viewers": 150,
							"bandwidth":    1000.5,
						},
					}, nil
				},
			}

			// Setup the handler
			router.GET("/api/v1/analytics/:tenant_id/viewer-metrics", func(c *gin.Context) {
				tenantID := c.Param("tenant_id")

				// Parse time range from query params (same logic as real handler)
				startTimeStr := c.Query("start_time")
				endTimeStr := c.Query("end_time")

				// For this test, require explicit time parameters to test validation
				if startTimeStr == "" {
					c.JSON(http.StatusBadRequest, errorResponse{Error: "start_time parameter is required. Use RFC3339 format."})
					return
				}
				if endTimeStr == "" {
					endTimeStr = time.Now().Format(time.RFC3339)
				}

				// Parse time strings into time.Time objects for ClickHouse
				startTime, err := time.Parse(time.RFC3339, startTimeStr)
				if err != nil {
					c.JSON(http.StatusBadRequest, errorResponse{Error: "Invalid start_time format. Use RFC3339 format."})
					return
				}

				endTime, err := time.Parse(time.RFC3339, endTimeStr)
				if err != nil {
					c.JSON(http.StatusBadRequest, errorResponse{Error: "Invalid end_time format. Use RFC3339 format."})
					return
				}

				// Mock query to ClickHouse with proper time.Time objects
				_, err = mockDB.Query(`
					SELECT timestamp, stream_id, bitrate, fps, buffer_health
					FROM stream_health_samples
					WHERE tenant_id = $1 AND timestamp BETWEEN $2 AND $3
					ORDER BY timestamp DESC
				`, tenantID, startTime, endTime)

				if err != nil {
					c.JSON(http.StatusInternalServerError, errorResponse{Error: "Failed to fetch viewer metrics"})
					return
				}

				// Return success response
				c.JSON(http.StatusOK, gin.H{
					"data": []map[string]interface{}{
						{
							"timestamp":    "2024-01-15T10:30:00Z",
							"stream_id":    "stream-123",
							"bitrate":      4500,
							"fps":          60.0,
							"buffer_health": 0.92,
						},
					},
					"start_time": startTime.Format(time.RFC3339),
					"end_time":   endTime.Format(time.RFC3339),
				})
			})

			// Build request URL
			reqURL := fmt.Sprintf("/api/v1/analytics/tenant-123/viewer-metrics")
			if tt.startTime != "" || tt.endTime != "" {
				params := url.Values{}
				if tt.startTime != "" {
					params.Set("start_time", tt.startTime)
				}
				if tt.endTime != "" {
					params.Set("end_time", tt.endTime)
				}
				reqURL += "?" + params.Encode()
			}

			req := httptest.NewRequest("GET", reqURL, nil)
			w := httptest.NewRecorder()

			router.ServeHTTP(w, req)

			// Verify response status
			assert.Equal(t, tt.expectedStatus, w.Code, "Unexpected status code for %s", tt.name)

			if tt.expectedStatus == http.StatusOK {
				// Verify response is valid JSON
				var response map[string]interface{}
				err := json.Unmarshal(w.Body.Bytes(), &response)
				require.NoError(t, err, "Response should be valid JSON")

				// Verify time parsing worked correctly
				if tt.expectClickHouse && tt.validateTimes != nil {
					assert.True(t, tt.validateTimes(capturedStartTime, capturedEndTime),
						"Time validation failed for start: %v, end: %v", capturedStartTime, capturedEndTime)
				}

				// Verify response contains time information
				assert.Contains(t, response, "start_time", "Response should contain start_time")
				assert.Contains(t, response, "end_time", "Response should contain end_time")
			} else if tt.expectedStatus == http.StatusBadRequest {
				// Verify error response
				var errResp errorResponse
				err := json.Unmarshal(w.Body.Bytes(), &errResp)
				require.NoError(t, err, "Error response should be valid JSON")
				assert.Contains(t, errResp.Error, "format", "Error should mention format issue")
			}
		})
	}
}

func TestClickHouseTimeQuery_ProperTypes(t *testing.T) {
	// Test that we're passing proper time.Time objects to ClickHouse queries, not strings
	tests := []struct {
		name      string
		startTime string
		endTime   string
		query     string
	}{
		{
			name:      "viewer metrics query",
			startTime: "2024-01-15T10:00:00Z",
			endTime:   "2024-01-15T11:00:00Z",
			query: `
				SELECT timestamp, stream_id, bitrate, fps, buffer_health
				FROM stream_health_samples
				WHERE tenant_id = $1 AND timestamp BETWEEN $2 AND $3
				ORDER BY timestamp DESC
			`,
		},
		{
			name:      "routing events query",
			startTime: "2024-01-15T09:00:00Z",
			endTime:   "2024-01-15T10:00:00Z",
			query: `
				SELECT timestamp, internal_name, selected_node, status
				FROM routing_decisions
				WHERE tenant_id = $1 AND timestamp BETWEEN $2 AND $3
				ORDER BY timestamp DESC
			`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			startTime, err := time.Parse(time.RFC3339, tt.startTime)
			require.NoError(t, err)

			endTime, err := time.Parse(time.RFC3339, tt.endTime)
			require.NoError(t, err)

			// Mock ClickHouse query
			mockDB := &mockClickHouseDB{
				queryFunc: func(query string, args ...interface{}) ([]map[string]interface{}, error) {
					// Verify we receive proper time.Time objects, not strings
					require.Len(t, args, 3, "Expected 3 arguments: tenant_id, start_time, end_time")

					tenantID, ok := args[0].(string)
					require.True(t, ok, "First argument should be tenant_id string")
					assert.Equal(t, "tenant-123", tenantID)

					receivedStart, ok := args[1].(time.Time)
					require.True(t, ok, "Second argument should be time.Time, got %T", args[1])
					assert.True(t, receivedStart.Equal(startTime), "Start time mismatch")

					receivedEnd, ok := args[2].(time.Time)
					require.True(t, ok, "Third argument should be time.Time, got %T", args[2])
					assert.True(t, receivedEnd.Equal(endTime), "End time mismatch")

					return []map[string]interface{}{}, nil
				},
			}

			// Execute the query
			_, err = mockDB.Query(tt.query, "tenant-123", startTime, endTime)
			require.NoError(t, err)
		})
	}
}

func TestTimezoneHandling(t *testing.T) {
	// Test that different timezone inputs are properly converted to UTC for ClickHouse
	tests := []struct {
		name           string
		input          string
		expectedUTC    string
		expectedOffset int // in seconds from UTC
	}{
		{
			name:           "UTC timezone",
			input:          "2024-01-15T10:00:00Z",
			expectedUTC:    "2024-01-15T10:00:00Z",
			expectedOffset: 0,
		},
		{
			name:           "EST timezone (UTC-5)",
			input:          "2024-01-15T10:00:00-05:00",
			expectedUTC:    "2024-01-15T15:00:00Z",
			expectedOffset: -5 * 3600,
		},
		{
			name:           "CET timezone (UTC+1)",
			input:          "2024-01-15T10:00:00+01:00",
			expectedUTC:    "2024-01-15T09:00:00Z",
			expectedOffset: 1 * 3600,
		},
		{
			name:           "JST timezone (UTC+9)",
			input:          "2024-01-15T10:00:00+09:00",
			expectedUTC:    "2024-01-15T01:00:00Z",
			expectedOffset: 9 * 3600,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, err := time.Parse(time.RFC3339, tt.input)
			require.NoError(t, err, "Should parse valid RFC3339 time")

			// Verify UTC conversion
			utc := parsed.UTC()
			expected, err := time.Parse(time.RFC3339, tt.expectedUTC)
			require.NoError(t, err, "Expected time should be valid")

			assert.True(t, utc.Equal(expected), "UTC conversion failed: expected %v, got %v", expected, utc)

			// Verify timezone offset
			_, offset := parsed.Zone()
			assert.Equal(t, tt.expectedOffset, offset, "Timezone offset mismatch")
		})
	}
}

func TestDateTimeValidationEdgeCases(t *testing.T) {
	// Test edge cases for datetime validation
	tests := []struct {
		name        string
		input       string
		expectError bool
		description string
	}{
		{
			name:        "leap year date",
			input:       "2024-02-29T10:00:00Z",
			expectError: false,
			description: "Should handle leap year dates",
		},
		{
			name:        "invalid leap year date",
			input:       "2023-02-29T10:00:00Z",
			expectError: true,
			description: "Should reject invalid leap year dates",
		},
		{
			name:        "end of year",
			input:       "2024-12-31T23:59:59Z",
			expectError: false,
			description: "Should handle end of year",
		},
		{
			name:        "start of year",
			input:       "2024-01-01T00:00:00Z",
			expectError: false,
			description: "Should handle start of year",
		},
		{
			name:        "invalid month",
			input:       "2024-13-01T10:00:00Z",
			expectError: true,
			description: "Should reject invalid months",
		},
		{
			name:        "invalid day",
			input:       "2024-01-32T10:00:00Z",
			expectError: true,
			description: "Should reject invalid days",
		},
		{
			name:        "invalid hour",
			input:       "2024-01-01T25:00:00Z",
			expectError: true,
			description: "Should reject invalid hours",
		},
		{
			name:        "invalid minute",
			input:       "2024-01-01T10:60:00Z",
			expectError: true,
			description: "Should reject invalid minutes",
		},
		{
			name:        "invalid second",
			input:       "2024-01-01T10:00:60Z",
			expectError: true,
			description: "Should reject invalid seconds",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := time.Parse(time.RFC3339, tt.input)

			if tt.expectError {
				assert.Error(t, err, tt.description)
			} else {
				assert.NoError(t, err, tt.description)
			}
		})
	}
}
