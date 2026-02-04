package handlers

import (
	"testing"

	"github.com/google/uuid"
)

func TestMax64(t *testing.T) {
	cases := []struct {
		name     string
		first    int64
		second   int64
		expected int64
	}{
		{name: "first larger", first: 10, second: 2, expected: 10},
		{name: "second larger", first: -4, second: 8, expected: 8},
		{name: "equal", first: 5, second: 5, expected: 5},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := max64(tc.first, tc.second); got != tc.expected {
				t.Fatalf("max64(%d, %d) = %d, want %d", tc.first, tc.second, got, tc.expected)
			}
		})
	}
}

func TestNilIfZeroFloat32(t *testing.T) {
	cases := []struct {
		name     string
		input    float32
		expected *float32
	}{
		{name: "zero", input: 0, expected: nil},
		{name: "non-zero", input: 1.25, expected: float32Ptr(1.25)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nilIfZeroFloat32(tc.input)
			assertInterfaceValue(t, got, tc.expected)
		})
	}
}

func TestNilIfZeroBool(t *testing.T) {
	cases := []struct {
		name     string
		input    bool
		expected *bool
	}{
		{name: "false", input: false, expected: nil},
		{name: "true", input: true, expected: boolPtr(true)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nilIfZeroBool(tc.input)
			assertInterfaceValue(t, got, tc.expected)
		})
	}
}

func TestNilIfZeroUint64(t *testing.T) {
	cases := []struct {
		name     string
		input    uint64
		expected *uint64
	}{
		{name: "zero", input: 0, expected: nil},
		{name: "non-zero", input: 42, expected: uint64Ptr(42)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nilIfZeroUint64(tc.input)
			assertInterfaceValue(t, got, tc.expected)
		})
	}
}

func TestNilIfZeroUint32(t *testing.T) {
	cases := []struct {
		name     string
		input    uint32
		expected *uint32
	}{
		{name: "zero", input: 0, expected: nil},
		{name: "non-zero", input: 7, expected: uint32Ptr(7)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nilIfZeroUint32(tc.input)
			assertInterfaceValue(t, got, tc.expected)
		})
	}
}

func TestNilIfEmptyString(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		expected *string
	}{
		{name: "empty", input: "", expected: nil},
		{name: "non-empty", input: "value", expected: stringPtr("value")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nilIfEmptyString(tc.input)
			assertInterfaceValue(t, got, tc.expected)
		})
	}
}

func TestParseUUID(t *testing.T) {
	valid := uuid.New()
	cases := []struct {
		name     string
		input    string
		expected uuid.UUID
	}{
		{name: "empty", input: "", expected: uuid.Nil},
		{name: "invalid", input: "not-a-uuid", expected: uuid.Nil},
		{name: "valid", input: valid.String(), expected: valid},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseUUID(tc.input); got != tc.expected {
				t.Fatalf("parseUUID(%q) = %s, want %s", tc.input, got, tc.expected)
			}
		})
	}
}

func TestIsValidUUIDString(t *testing.T) {
	valid := uuid.New().String()
	cases := []struct {
		name     string
		input    string
		expected bool
	}{
		{name: "empty", input: "", expected: false},
		{name: "invalid", input: "invalid", expected: false},
		{name: "nil uuid", input: uuid.Nil.String(), expected: false},
		{name: "valid", input: valid, expected: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isValidUUIDString(tc.input); got != tc.expected {
				t.Fatalf("isValidUUIDString(%q) = %t, want %t", tc.input, got, tc.expected)
			}
		})
	}
}

func TestBoolToUint8(t *testing.T) {
	cases := []struct {
		name     string
		input    bool
		expected uint8
	}{
		{name: "false", input: false, expected: 0},
		{name: "true", input: true, expected: 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := boolToUint8(tc.input); got != tc.expected {
				t.Fatalf("boolToUint8(%t) = %d, want %d", tc.input, got, tc.expected)
			}
		})
	}
}

func assertInterfaceValue[T comparable](t *testing.T, got interface{}, expected *T) {
	t.Helper()
	if expected == nil {
		if got != nil {
			t.Fatalf("expected nil, got %#v", got)
		}
		return
	}
	if got == nil {
		t.Fatalf("expected %#v, got nil", *expected)
	}
	value, ok := got.(T)
	if !ok {
		t.Fatalf("expected type %T, got %T", *expected, got)
	}
	if value != *expected {
		t.Fatalf("expected %#v, got %#v", *expected, value)
	}
}

func float32Ptr(v float32) *float32 {
	return &v
}

func boolPtr(v bool) *bool {
	return &v
}

func uint64Ptr(v uint64) *uint64 {
	return &v
}

func uint32Ptr(v uint32) *uint32 {
	return &v
}

func stringPtr(v string) *string {
	return &v
}
