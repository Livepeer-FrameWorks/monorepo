package platformfeatures

import (
	"slices"
	"testing"
)

func TestShippedIsSortedUniqueAndCopied(t *testing.T) {
	got := Shipped()
	if len(got) == 0 {
		t.Fatal("no shipped features")
	}
	if !slices.IsSorted(got) || len(slices.Compact(slices.Clone(got))) != len(got) {
		t.Fatalf("shipped features not sorted and unique: %v", got)
	}
	if !slices.Contains(got, "viewer-protocol-selection") {
		t.Fatalf("viewer-protocol-selection missing: players feature-detect on it")
	}
	got[0] = "mutated"
	if Shipped()[0] == "mutated" {
		t.Fatal("Shipped exposes its backing array")
	}
}
