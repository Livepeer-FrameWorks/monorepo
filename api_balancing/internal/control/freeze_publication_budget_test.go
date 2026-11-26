package control

import (
	"math"
	"testing"
	"time"
)

func TestFreezePublicationBudgetIsBounded(t *testing.T) {
	for _, size := range []int64{-1, 0, 1, 3068515312, math.MaxInt64} {
		got := freezePublicationTimeout(size)
		if got < time.Minute || got > 15*time.Minute {
			t.Errorf("size %d: budget %s", size, got)
		}
	}
	if got := freezePublicationTimeout(3068515312); got < 6*time.Minute {
		t.Fatalf("masterclass publication budget %s", got)
	}
}
