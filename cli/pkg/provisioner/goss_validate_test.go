package provisioner

import (
	"context"
	"errors"
	"strings"
	"testing"

	"frameworks/cli/pkg/ansible"
)

func TestRetryGossValidate_eventualSuccess(t *testing.T) {
	t.Parallel()

	var calls int
	err := retryGossValidate(context.Background(), "kafka-controller", func() (*ansible.ExecuteResult, error) {
		calls++
		if calls < 3 {
			return &ansible.ExecuteResult{Success: false, Output: "not ready yet"}, errors.New("exit status 2")
		}
		return &ansible.ExecuteResult{Success: true, Output: "ok"}, nil
	})
	if err != nil {
		t.Fatalf("retryGossValidate() error = %v, want nil", err)
	}
	if calls != 3 {
		t.Fatalf("retryGossValidate() calls = %d, want 3", calls)
	}
}

func TestRetryGossValidate_returnsLastFailure(t *testing.T) {
	t.Parallel()

	var calls int
	err := retryGossValidate(context.Background(), "kafka-controller", func() (*ansible.ExecuteResult, error) {
		calls++
		return &ansible.ExecuteResult{Success: false, Output: "port not listening"}, errors.New("exit status 2")
	})
	if err == nil {
		t.Fatal("retryGossValidate() error = nil, want failure")
	}
	if calls != 10 {
		t.Fatalf("retryGossValidate() calls = %d, want 10", calls)
	}
	if got := err.Error(); got == "" || !containsAllParts(got, []string{"kafka-controller", "exit status 2", "port not listening"}) {
		t.Fatalf("retryGossValidate() error = %q, want service name, exit status, and output", got)
	}
}

func containsAllParts(s string, parts []string) bool {
	for _, part := range parts {
		if !strings.Contains(s, part) {
			return false
		}
	}
	return true
}
