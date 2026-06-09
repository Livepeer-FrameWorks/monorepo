package turnstile

import (
	"testing"
	"time"
)

func TestNewValidator_HTTPClientTimeout(t *testing.T) {
	v := NewValidator("secret")
	if v.httpClient.Timeout != 10*time.Second {
		t.Fatalf("httpClient timeout = %v, want 10s", v.httpClient.Timeout)
	}
}
