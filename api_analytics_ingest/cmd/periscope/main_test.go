package main

import (
	sqldriver "database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestIsRetryableConsumerError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "clickhouse connection refusal",
			err:  fmt.Errorf("api_usage_5m send: dial tcp 172.18.0.7:9000: connect: connection refused"),
			want: true,
		},
		{
			name: "driver bad connection",
			err:  fmt.Errorf("viewer_usage_5m send: %w", sqldriver.ErrBadConn),
			want: true,
		},
		{
			name: "json poison message",
			err:  fmt.Errorf("unmarshal service event: %w", &json.SyntaxError{Offset: 12}),
			want: false,
		},
		{
			name: "clickhouse semantic exception",
			err:  errors.New("viewer_usage_5m_v view: code: 184, message: aggregate function found inside another aggregate function"),
			want: false,
		},
		{
			name: "domain validation",
			err:  errors.New("missing_or_invalid_tenant_id"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRetryableConsumerError(tt.err); got != tt.want {
				t.Fatalf("isRetryableConsumerError() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRetryableConsumerBackoff(t *testing.T) {
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{attempt: 0, want: 500 * time.Millisecond},
		{attempt: 1, want: 500 * time.Millisecond},
		{attempt: 2, want: time.Second},
		{attempt: 7, want: 30 * time.Second},
		{attempt: 20, want: 30 * time.Second},
	}

	for _, tt := range tests {
		if got := retryableConsumerBackoff(tt.attempt); got != tt.want {
			t.Fatalf("retryableConsumerBackoff(%d) = %s, want %s", tt.attempt, got, tt.want)
		}
	}
}
