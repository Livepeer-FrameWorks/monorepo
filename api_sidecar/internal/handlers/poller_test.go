package handlers

import "testing"

func TestEvaluateNodeHealth(t *testing.T) {
	tests := []struct {
		name        string
		hasMistData bool
		cpu         float64
		mem         float64
		shm         float64
		expect      bool
	}{
		{
			name:        "no mist data",
			hasMistData: false,
			cpu:         10,
			mem:         10,
			shm:         10,
			expect:      false,
		},
		{
			name:        "cpu degraded",
			hasMistData: true,
			cpu:         91,
			mem:         10,
			shm:         10,
			expect:      false,
		},
		{
			name:        "memory degraded",
			hasMistData: true,
			cpu:         10,
			mem:         95,
			shm:         10,
			expect:      false,
		},
		{
			name:        "shm degraded",
			hasMistData: true,
			cpu:         10,
			mem:         10,
			shm:         92,
			expect:      false,
		},
		{
			name:        "healthy",
			hasMistData: true,
			cpu:         90,
			mem:         90,
			shm:         90,
			expect:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := evaluateNodeHealth(tt.hasMistData, tt.cpu, tt.mem, tt.shm)
			if got != tt.expect {
				t.Fatalf("expected %v, got %v", tt.expect, got)
			}
		})
	}
}
