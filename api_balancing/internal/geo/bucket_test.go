package geo

import (
	"math"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/geoip"
)

// BucketCoords snaps a precise coordinate to the centroid of an H3 cell. The
// point of bucketing is anonymization: it must be deterministic, must reject
// invalid input (so junk never reaches routing), and must coarsen the location
// enough that nearby points collapse onto a shared centroid.
func TestBucketCoords(t *testing.T) {
	const res = geoip.DefaultResolution

	t.Run("valid coordinate buckets deterministically", func(t *testing.T) {
		a := BucketCoords(52.3702, 4.8952, res)
		if a == nil {
			t.Fatal("expected a bucket for a valid coordinate")
		}
		if a.Resolution != res {
			t.Errorf("resolution = %d, want %d", a.Resolution, res)
		}
		if a.H3Index == 0 {
			t.Error("expected a non-zero H3 index")
		}
		b := BucketCoords(52.3702, 4.8952, res)
		if b == nil || a.H3Index != b.H3Index || a.Latitude != b.Latitude || a.Longitude != b.Longitude {
			t.Errorf("bucketing not deterministic: %+v vs %+v", a, b)
		}
	})

	t.Run("nearby points collapse to the same cell", func(t *testing.T) {
		// ~11m apart in latitude — far inside a resolution-5 hexagon
		// (~8 km edge), so both must anonymize to the same centroid.
		a := BucketCoords(52.3702, 4.8952, res)
		b := BucketCoords(52.3703, 4.8952, res)
		if a == nil || b == nil {
			t.Fatal("expected buckets for both nearby points")
		}
		if a.H3Index != b.H3Index {
			t.Errorf("nearby points landed in different cells: %d vs %d", a.H3Index, b.H3Index)
		}
		// The returned centroid is the cell's, not the caller's exact input.
		if a.Latitude == 52.3702 && a.Longitude == 4.8952 {
			t.Error("centroid equals exact input; coordinate was not coarsened")
		}
	})

	t.Run("invalid coordinates return nil", func(t *testing.T) {
		for _, c := range []struct {
			name     string
			lat, lon float64
		}{
			{"null island", 0, 0},
			{"NaN", math.NaN(), 4.0},
			{"lat out of range", 91, 4.0},
		} {
			if got := BucketCoords(c.lat, c.lon, res); got != nil {
				t.Errorf("%s: expected nil, got %+v", c.name, got)
			}
		}
	})
}

// Bucket is the production wrapper: it fixes the resolution to the default and
// returns the proto GeoBucket plus a validity flag the callers gate on.
func TestBucket(t *testing.T) {
	t.Run("valid coordinate", func(t *testing.T) {
		gb, lat, lon, ok := Bucket(52.3702, 4.8952)
		if !ok {
			t.Fatal("expected ok=true for a valid coordinate")
		}
		if gb == nil {
			t.Fatal("expected a non-nil GeoBucket")
		}
		if gb.Resolution != uint32(geoip.DefaultResolution) {
			t.Errorf("resolution = %d, want %d", gb.Resolution, geoip.DefaultResolution)
		}
		want := BucketCoords(52.3702, 4.8952, geoip.DefaultResolution)
		if gb.H3Index != want.H3Index || lat != want.Latitude || lon != want.Longitude {
			t.Errorf("Bucket centroid/index disagrees with BucketCoords: (%d,%v,%v) vs %+v", gb.H3Index, lat, lon, want)
		}
	})

	t.Run("invalid coordinate returns false with zeroed outputs", func(t *testing.T) {
		gb, lat, lon, ok := Bucket(0, 0)
		if ok || gb != nil || lat != 0 || lon != 0 {
			t.Errorf("expected (nil,0,0,false), got (%+v,%v,%v,%v)", gb, lat, lon, ok)
		}
	})
}

// BucketGeoData mutates a GeoData in place, replacing the exact fix with the
// bucket centroid. Invalid coordinates and a nil pointer must be left alone.
func TestBucketGeoData(t *testing.T) {
	t.Run("nil is a no-op", func(t *testing.T) {
		BucketGeoData(nil) // must not panic
	})

	t.Run("valid coordinates are replaced with the centroid", func(t *testing.T) {
		g := &geoip.GeoData{Latitude: 52.3702, Longitude: 4.8952, City: "Amsterdam"}
		BucketGeoData(g)
		want := BucketCoords(52.3702, 4.8952, geoip.DefaultResolution)
		if g.Latitude != want.Latitude || g.Longitude != want.Longitude {
			t.Errorf("coords = (%v,%v), want centroid (%v,%v)", g.Latitude, g.Longitude, want.Latitude, want.Longitude)
		}
		if g.City != "Amsterdam" {
			t.Errorf("unrelated field mutated: City = %q", g.City)
		}
	})

	t.Run("invalid coordinates are left untouched", func(t *testing.T) {
		g := &geoip.GeoData{Latitude: 0, Longitude: 0}
		BucketGeoData(g)
		if g.Latitude != 0 || g.Longitude != 0 {
			t.Errorf("expected null-island coords untouched, got (%v,%v)", g.Latitude, g.Longitude)
		}
	})
}
