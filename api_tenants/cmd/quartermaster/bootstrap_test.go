package main

import (
	"fmt"
	"testing"

	"github.com/lib/pq"
)

func TestIsRetryableBootstrapTransactionError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "serialization failure",
			err:  fmt.Errorf("nodes: %w", &pq.Error{Code: "40001"}),
			want: true,
		},
		{
			name: "deadlock detected",
			err:  fmt.Errorf("commit: %w", &pq.Error{Code: "40P01"}),
			want: true,
		},
		{
			name: "constraint violation",
			err:  fmt.Errorf("nodes: %w", &pq.Error{Code: "23505"}),
			want: false,
		},
		{
			name: "plain error",
			err:  fmt.Errorf("nodes: probe failed"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRetryableBootstrapTransactionError(tt.err); got != tt.want {
				t.Fatalf("isRetryableBootstrapTransactionError() = %v, want %v", got, tt.want)
			}
		})
	}
}
