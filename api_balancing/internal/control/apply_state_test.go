package control

import (
	"testing"
)

func TestSeedVersionMonotonicPerNode(t *testing.T) {
	c := newSeedVersionCounter()
	if v := c.next("a"); v != 1 {
		t.Fatalf("a#1 = %d, want 1", v)
	}
	if v := c.next("a"); v != 2 {
		t.Fatalf("a#2 = %d, want 2", v)
	}
	if v := c.next("b"); v != 1 {
		t.Fatalf("b#1 = %d, want 1 (independent per node)", v)
	}
	if v := c.next("a"); v != 3 {
		t.Fatalf("a#3 = %d, want 3", v)
	}
}
