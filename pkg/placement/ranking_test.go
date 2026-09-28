package placement

import "testing"

// noGeo is a viewer the platform cannot locate (VPN, private address, GeoIP miss) that arrived
// at the cell serving arrivalCluster.
func noGeo(arrivalCluster string, candidates ...Candidate) Request {
	r := request(candidates...)
	r.Location = nil
	r.ArrivalClusterIDs = []string{arrivalCluster}
	return r
}

// The staging incident: the EU origin carries ingest and relay upstream, so it has less bandwidth
// headroom than an idle US edge. Without client coordinates it must still serve the viewer that
// arrived in the EU cell instead of starting a cross-cell pull.
func TestNoGeoViewerStaysOnTheArrivalCellsOrigin(t *testing.T) {
	origin := edge("eu-origin", "eu-cell", 52.4, 4.9)
	origin.BWAvailable, origin.CPUPercent = 550, 30
	idleUS := edge("us-edge", "us-cell", 38.9, -77)
	idleUS.BWAvailable, idleUS.CPUPercent, idleUS.Presence = 990, 2, Absent
	d := evaluate(t, noGeo("eu-cell", idleUS, origin))
	if winner(d) != "eu-origin" || d.Choices[0].RequiresPull {
		t.Fatalf("viewer left its arrival cell for an idle pull: %+v", d.Choices)
	}
}

// Inside the arrival cell a node that must pull the stream is preferred over one already holding
// it only once the holder is past the load bound; another cell is used only when no local node is
// inside the bound.
func TestLoadBoundDecidesPullsAndCellSpill(t *testing.T) {
	origin := edge("eu-origin", "eu-cell", 52.4, 4.9)
	origin.BWAvailable = 150 // 85% used: past the bound
	sibling := edge("eu-sibling", "eu-cell", 52.4, 4.9)
	sibling.Presence, sibling.BWAvailable = Absent, 700
	remote := edge("us-edge", "us-cell", 38.9, -77)
	remote.Presence, remote.BWAvailable = Present, 950

	if d := evaluate(t, noGeo("eu-cell", origin, sibling, remote)); winner(d) != "eu-sibling" || !d.Choices[0].RequiresPull {
		t.Fatalf("overloaded origin should hand the viewer to a local pull: %+v", d.Choices)
	}
	sibling.BWAvailable = 100
	if d := evaluate(t, noGeo("eu-cell", origin, sibling, remote)); winner(d) != "us-edge" {
		t.Fatalf("with no local node inside the bound the viewer should go to the other cell: %+v", d.Choices)
	}
	remote.BWAvailable = 100
	if d := evaluate(t, noGeo("eu-cell", origin, sibling, remote)); winner(d) != "eu-origin" {
		t.Fatalf("with every node past the bound, the least loaded local node should serve: %+v", d.Choices)
	}
}

// Nodes in the same metro are equally close; the one holding the stream serves even when a
// slightly nearer node would have to pull it.
func TestGeoBandPrefersPresenceWithinTheMetro(t *testing.T) {
	holder := edge("ams-holder", "eu-cell", 52.37, 4.90)
	holder.BWAvailable = 600
	nearer := edge("ams-nearer", "eu-cell", 52.30, 4.76)
	nearer.Presence, nearer.BWAvailable = Absent, 950
	r := request(holder, nearer)
	r.Location = &Coordinates{Latitude: 52.31, Longitude: 4.77}
	if d := evaluate(t, r); winner(d) != "ams-holder" {
		t.Fatalf("a nearer node in the same metro started a pull: %+v", d.Choices)
	}
}

// Power of two choices spreads a burst across near-equal nodes when a seed is supplied, never
// picks a node outside the near-best set, and a zero seed keeps the order deterministic.
func TestNearBestSpreadUsesTwoChoices(t *testing.T) {
	a, b, c := edge("a", "cell", 0, 0), edge("b", "cell", 0, 0), edge("c", "cell", 0, 0)
	a.BWAvailable, b.BWAvailable, c.BWAvailable = 800, 790, 780
	far := edge("loaded", "cell", 0, 0)
	far.BWAvailable = 300
	r := noGeo("cell", a, b, c, far)
	wins := map[string]int{}
	for seed := uint64(1); seed <= 300; seed++ {
		r.TieBreakSeed = seed
		wins[winner(evaluate(t, r))]++
	}
	if wins["loaded"] != 0 {
		t.Fatalf("a node outside the near-best set was picked: %v", wins)
	}
	if wins["a"] == 0 || wins["b"] == 0 {
		t.Fatalf("near-equal nodes did not share the burst: %v", wins)
	}
	if wins["c"] != 0 {
		t.Fatalf("the worst of two choices must never lead: %v", wins)
	}
	r.TieBreakSeed = 0
	for i := 0; i < 5; i++ {
		if winner(evaluate(t, r)) != "a" {
			t.Fatal("zero seed is not deterministic")
		}
	}
}
