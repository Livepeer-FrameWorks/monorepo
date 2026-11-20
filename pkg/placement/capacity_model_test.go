package placement

import "testing"

func TestCapacityHeadroomRejectsOlderPeerAtNinetyFivePercent(t *testing.T) {
	c := edge("node", "cell", 0, 0)
	c.BWLimit, c.BWAvailable = 1000, 50
	r := request(c)
	if got := feasibility(c, r, true); got != CapacityFull {
		t.Fatalf("95%% used: %s, want %s", got, CapacityFull)
	}
	c.BWAvailable = 151
	if got := feasibility(c, r, true); got != Eligible {
		t.Fatalf("85%% boundary: %s", got)
	}
}

func TestCapacityTargetPrefersPullBelowSeventyPercent(t *testing.T) {
	holder := edge("holder", "cell", 0, 0)
	holder.BWAvailable = 250
	sibling := edge("sibling", "cell", 0, 0)
	sibling.Presence, sibling.BWAvailable = Absent, 700
	d := evaluate(t, noGeo("cell", holder, sibling))
	if winner(d) != "sibling" {
		t.Fatalf("75%% holder beat 30%% pull: %+v", d.Choices)
	}
}
