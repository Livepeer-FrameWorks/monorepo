package storage

import (
	"errors"
	"fmt"
	"math"
	"testing"
)

// RequiredAvailableBytes adds the fixed MinFreeBytes reserve to the request and
// must clamp instead of wrapping around when the sum would overflow uint64 — a
// silent wrap would report a tiny requirement and defeat the admission gate.
func TestRequiredAvailableBytes(t *testing.T) {
	if got := RequiredAvailableBytes(0); got != MinFreeBytes {
		t.Fatalf("RequiredAvailableBytes(0) = %d, want reserve %d", got, MinFreeBytes)
	}
	if got := RequiredAvailableBytes(10); got != 10+MinFreeBytes {
		t.Fatalf("RequiredAvailableBytes(10) = %d, want %d", got, 10+MinFreeBytes)
	}

	// requiredBytes within MinFreeBytes of the max would overflow; must clamp.
	overflow := uint64(math.MaxUint64) - MinFreeBytes + 1
	if got := RequiredAvailableBytes(overflow); got != math.MaxUint64 {
		t.Fatalf("RequiredAvailableBytes(near-max) = %d, want clamp to %d", got, uint64(math.MaxUint64))
	}
	if got := RequiredAvailableBytes(math.MaxUint64); got != math.MaxUint64 {
		t.Fatalf("RequiredAvailableBytes(max) = %d, want clamp to %d", got, uint64(math.MaxUint64))
	}
}

// IsInsufficientSpace must recognise the wrapped sentinel that the admission
// helpers return, and reject unrelated errors.
func TestIsInsufficientSpace(t *testing.T) {
	if !IsInsufficientSpace(ErrInsufficientSpace) {
		t.Fatal("bare sentinel must match")
	}
	wrapped := fmt.Errorf("statfs failed: %w", ErrInsufficientSpace)
	if !IsInsufficientSpace(wrapped) {
		t.Fatal("wrapped sentinel must match")
	}
	if IsInsufficientSpace(errors.New("disk on fire")) {
		t.Fatal("unrelated error must not match")
	}
	if IsInsufficientSpace(nil) {
		t.Fatal("nil must not match")
	}
}

// HasSpaceForWithinCapacity is the storage admission gate. A zero request always
// fits (only the reserve must be free), an impossibly large request is refused
// with the recognisable sentinel, and a logical capacity cap below used+reserve
// refuses even though the physical filesystem is huge.
func TestHasSpaceForWithinCapacity(t *testing.T) {
	dir := t.TempDir()

	t.Run("zero request fits", func(t *testing.T) {
		if err := HasSpaceForWithinCapacity(dir, 0, 0); err != nil {
			t.Fatalf("zero request should fit, got %v", err)
		}
	})

	t.Run("impossible request is refused", func(t *testing.T) {
		err := HasSpaceForWithinCapacity(dir, math.MaxUint64-MinFreeBytes, 0)
		if !IsInsufficientSpace(err) {
			t.Fatalf("expected ErrInsufficientSpace, got %v", err)
		}
	})

	t.Run("logical capacity below reserve refuses despite huge disk", func(t *testing.T) {
		// capacity (1 byte) < used+reserve, so logical availability is ~0
		// regardless of the real filesystem having space.
		err := HasSpaceForWithinCapacity(dir, 0, 1)
		if !IsInsufficientSpace(err) {
			t.Fatalf("expected ErrInsufficientSpace under tight cap, got %v", err)
		}
	})
}

// HasSpaceFor delegates to HasSpaceForWithinCapacity with no logical cap.
func TestHasSpaceFor(t *testing.T) {
	dir := t.TempDir()
	if err := HasSpaceFor(dir, 0); err != nil {
		t.Fatalf("zero request should fit, got %v", err)
	}
	if err := HasSpaceFor(dir, math.MaxUint64-MinFreeBytes); !IsInsufficientSpace(err) {
		t.Fatalf("impossible request should be refused, got %v", err)
	}
}
