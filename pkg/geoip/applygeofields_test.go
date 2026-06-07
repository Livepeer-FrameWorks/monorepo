package geoip

import (
	"math"
	"testing"
)

// applyGeoFields is the field-mapping half of EnrichEvent, split out so it
// can be exercised without an MMDB fixture. The invariant under test: a key
// is only ever written with a usable value — empty strings and non-finite
// coordinates must not land in the event map (they poison ClickHouse inserts
// and routing math, which can't distinguish a missing key from a NaN).

func TestApplyGeoFields_FullyPopulated(t *testing.T) {
	event := map[string]any{}
	applyGeoFields(event, &GeoData{
		CountryCode: "NL",
		CountryName: "Netherlands",
		City:        "Amsterdam",
		Latitude:    52.37,
		Longitude:   4.89,
		Timezone:    "Europe/Amsterdam",
	})

	want := map[string]any{
		"country_code": "NL",
		"country_name": "Netherlands",
		"city":         "Amsterdam",
		"latitude":     52.37,
		"longitude":    4.89,
		"timezone":     "Europe/Amsterdam",
	}
	for k, v := range want {
		got, ok := event[k]
		if !ok {
			t.Errorf("missing key %q", k)
			continue
		}
		if got != v {
			t.Errorf("key %q = %v, want %v", k, got, v)
		}
	}
	if len(event) != len(want) {
		t.Errorf("event has %d keys, want %d: %v", len(event), len(want), event)
	}
}

func TestApplyGeoFields_EmptyStringsSkipped(t *testing.T) {
	event := map[string]any{}
	// Valid coordinates but every string field empty: only lat/lon should appear.
	applyGeoFields(event, &GeoData{Latitude: 1.0, Longitude: 2.0})

	for _, k := range []string{"country_code", "country_name", "city", "timezone"} {
		if _, ok := event[k]; ok {
			t.Errorf("empty string field %q should not be written", k)
		}
	}
	if _, ok := event["latitude"]; !ok {
		t.Error("valid latitude should be written")
	}
	if _, ok := event["longitude"]; !ok {
		t.Error("valid longitude should be written")
	}
}

func TestApplyGeoFields_NonFiniteCoordinatesSkipped(t *testing.T) {
	tests := []struct {
		name     string
		lat, lon float64
		wantLat  bool
		wantLon  bool
	}{
		{"NaN latitude", math.NaN(), 4.89, false, true},
		{"NaN longitude", 52.37, math.NaN(), true, false},
		{"+Inf longitude", 52.37, math.Inf(1), true, false},
		{"-Inf latitude", math.Inf(-1), 4.89, false, true},
		{"both NaN", math.NaN(), math.NaN(), false, false},
		{"both finite", 52.37, 4.89, true, true},
		{"zero is finite", 0, 0, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := map[string]any{}
			applyGeoFields(event, &GeoData{Latitude: tt.lat, Longitude: tt.lon})
			if _, ok := event["latitude"]; ok != tt.wantLat {
				t.Errorf("latitude present=%v, want %v", ok, tt.wantLat)
			}
			if _, ok := event["longitude"]; ok != tt.wantLon {
				t.Errorf("longitude present=%v, want %v", ok, tt.wantLon)
			}
		})
	}
}

func TestApplyGeoFields_NilInputsAreNoOps(t *testing.T) {
	// Nil geoData must not panic and must not mutate the event.
	event := map[string]any{"existing": "value"}
	applyGeoFields(event, nil)
	if len(event) != 1 {
		t.Errorf("nil geoData mutated event: %v", event)
	}
	// Nil event must not panic.
	applyGeoFields(nil, &GeoData{CountryCode: "NL"})
}

// EnrichEvent's own guards are pure (they short-circuit before Lookup), so
// they can be checked against a nil Reader without an MMDB.
func TestEnrichEvent_Guards(t *testing.T) {
	t.Run("nil reader is a no-op", func(t *testing.T) {
		var r *Reader
		event := map[string]any{"src_ip": "8.8.8.8"}
		r.EnrichEvent(event, "src_ip")
		if len(event) != 1 {
			t.Errorf("nil reader enriched event: %v", event)
		}
	})

	t.Run("nil event is a no-op", func(t *testing.T) {
		r := &Reader{} // db nil; should never reach Lookup
		r.EnrichEvent(nil, "src_ip")
	})

	t.Run("missing ip field is a no-op", func(t *testing.T) {
		r := &Reader{}
		event := map[string]any{"other": "x"}
		r.EnrichEvent(event, "src_ip")
		if len(event) != 1 {
			t.Errorf("missing ip field enriched event: %v", event)
		}
	})

	t.Run("empty ip string is a no-op", func(t *testing.T) {
		r := &Reader{}
		event := map[string]any{"src_ip": ""}
		r.EnrichEvent(event, "src_ip")
		if len(event) != 1 {
			t.Errorf("empty ip enriched event: %v", event)
		}
	})

	t.Run("non-string ip value is a no-op", func(t *testing.T) {
		r := &Reader{}
		event := map[string]any{"src_ip": 12345}
		r.EnrichEvent(event, "src_ip")
		if len(event) != 1 {
			t.Errorf("non-string ip enriched event: %v", event)
		}
	})
}
