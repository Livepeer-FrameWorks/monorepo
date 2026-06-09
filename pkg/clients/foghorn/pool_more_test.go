package foghorn

import (
	"testing"
	"time"
)

func TestPoolConfigWithDefaults_AppliesExactDefaultsWhenZero(t *testing.T) {
	got := PoolConfig{}.withDefaults()
	if got.Timeout != 30*time.Second {
		t.Fatalf("Timeout default = %v, want 30s", got.Timeout)
	}
	if got.MaxIdleTime != 10*time.Minute {
		t.Fatalf("MaxIdleTime default = %v, want 10m", got.MaxIdleTime)
	}
	if got.HealthCheckInterval != 30*time.Second {
		t.Fatalf("HealthCheckInterval default = %v, want 30s", got.HealthCheckInterval)
	}
}

func TestPoolConfigWithDefaults_PreservesNonZero(t *testing.T) {
	in := PoolConfig{
		Timeout:             5 * time.Second,
		MaxIdleTime:         1 * time.Minute,
		HealthCheckInterval: 2 * time.Second,
	}
	got := in.withDefaults()
	if got.Timeout != 5*time.Second {
		t.Fatalf("Timeout = %v, want preserved 5s", got.Timeout)
	}
	if got.MaxIdleTime != 1*time.Minute {
		t.Fatalf("MaxIdleTime = %v, want preserved 1m", got.MaxIdleTime)
	}
	if got.HealthCheckInterval != 2*time.Second {
		t.Fatalf("HealthCheckInterval = %v, want preserved 2s", got.HealthCheckInterval)
	}
}
