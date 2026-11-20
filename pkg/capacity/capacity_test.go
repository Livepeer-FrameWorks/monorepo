package capacity

import (
	"math"
	"testing"
)

func TestLinkAdmissionHeadroom(t *testing.T) {
	link := Link{Budget: 1000, Observed: 600, Reserved: 100, ObservedValid: true}
	if link.Committed() != 700 || link.Usable() != 850 || link.Available() != 150 || link.RawAvailable() != 300 {
		t.Fatalf("committed=%d usable=%d available=%d raw=%d", link.Committed(), link.Usable(), link.Available(), link.RawAvailable())
	}
	if !link.WithinTarget() || !link.Admits(150) || link.Admits(151) {
		t.Fatal("admission must retain 15% headroom, including outstanding reservations")
	}
	link.Reserved++
	if link.WithinTarget() {
		t.Fatal("a node above 70% committed load is outside the preferred range")
	}
	if got := link.LoadFraction(); math.Abs(got-0.701) > 1e-12 {
		t.Fatalf("load fraction = %f", got)
	}
}

func TestLinkExhaustedAndUnknown(t *testing.T) {
	for _, link := range []Link{
		{},
		{Budget: 1000, Observed: 851},
		{Budget: 1000, Observed: 950},
		{Budget: 1000, Observed: 1200},
		{Budget: 1000, Observed: 700, Reserved: 151},
	} {
		if link.Available() != 0 || link.Admits(0) || link.WithinTarget() {
			t.Fatalf("exhausted or unknown link admitted: %+v", link)
		}
	}
	if !math.IsInf((Link{}).LoadFraction(), 1) {
		t.Fatal("unknown budget must not appear idle")
	}
	if (Link{Budget: 1000, Observed: 850}).Admits(1) {
		t.Fatal("admission at the hard cap must refuse any additional load")
	}
}

func TestCapacityArithmeticDoesNotWrap(t *testing.T) {
	link := Link{Budget: math.MaxUint64, Observed: math.MaxUint64 - 10, Reserved: 100}
	if link.Committed() != math.MaxUint64 || link.Available() != 0 || link.RawAvailable() != 0 || link.Admits(math.MaxUint64) {
		t.Fatal("overflow made an overloaded link appear available")
	}
	if link.LoadFraction() < 1 {
		t.Fatal("overflow made load appear below capacity")
	}
	if got, want := link.Usable(), uint64(15679732462653118872); got != want {
		t.Fatalf("large usable budget = %d, want %d", got, want)
	}
	for budget, usable := range map[uint64]uint64{1: 0, 2: 1, 19: 16, 20: 17, 100: 85} {
		if got := (Link{Budget: budget}).Usable(); got != usable {
			t.Errorf("budget %d usable=%d, want %d", budget, got, usable)
		}
	}
}

func TestBitrateReservationIncludesOverheadWithoutCeiling(t *testing.T) {
	for bitrate, want := range map[uint64]uint64{
		0: 65536, 1: 65536, 1000: 137500, 20000: 2750000, 20001: 2750138,
		math.MaxUint64: math.MaxUint64,
	} {
		if got := EstimateFromTopBitrateKbps(bitrate); got != want {
			t.Errorf("%d kbps reservation=%d B/s, want %d", bitrate, got, want)
		}
	}
	if DefaultBudget != 125000000 || DefaultIngestEstimate != 1250000 || TargetUtilization != 0.70 || MaxUtilization != 0.85 {
		t.Fatal("capacity defaults differ from the platform admission policy")
	}
}
