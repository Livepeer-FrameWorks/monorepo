package handlers

import (
	"math"
	"testing"
)

func TestSanitizeFloat(t *testing.T) {
	tests := []struct {
		name     string
		input    float64
		expected float64
	}{
		{name: "normal", input: 12.5, expected: 12.5},
		{name: "nan", input: math.NaN(), expected: 0},
		{name: "inf", input: math.Inf(1), expected: 0},
		{name: "neg_inf", input: math.Inf(-1), expected: 0},
		{name: "small", input: 1e-9, expected: 1e-9},
		{name: "large", input: 9e15, expected: 9e15},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual := sanitizeFloat(test.input)
			if math.IsNaN(test.input) || math.IsInf(test.input, 0) {
				if actual != 0 {
					t.Fatalf("expected 0, got %v", actual)
				}
				return
			}
			if actual != test.expected {
				t.Fatalf("expected %v, got %v", test.expected, actual)
			}
		})
	}
}
