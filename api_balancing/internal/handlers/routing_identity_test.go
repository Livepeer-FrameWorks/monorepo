package handlers

import (
	"context"
	"math"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/cache"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/geoip"
	"github.com/gin-gonic/gin"
)

func TestRoutingIdentitySharedByMediaFrontDoors(t *testing.T) {
	for _, tc := range []struct{ name, trust, peer, want string }{
		{"untrusted forwarding", "", "192.0.2.5:7000", "192.0.2.5"},
		{"explicit proxy", "127.0.0.1/32", "127.0.0.1:7000", "198.51.100.5"},
		{"IPv6 peer", "", "[2001:db8::5]:7000", "2001:db8::5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useTrustedProxyCIDRs(t, tc.trust)
			for _, path := range []string{"/play/example", "/resolve/example", "/ingest/example", "/source/example"} {
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequestWithContext(context.Background(), "GET", path, nil)
				c.Request.RemoteAddr = tc.peer
				c.Request.Header.Set("X-Forwarded-For", "198.51.100.5")
				if got := trustedClientIP(c); got != tc.want {
					t.Fatalf("%s: got %q, want %q", path, got, tc.want)
				}
				if got := ctxkeys.GetClientIP(c.Request.Context()); got != tc.want {
					t.Fatalf("request context got %q", got)
				}
				if got := c.GetString(string(ctxkeys.KeyClientIP)); got != tc.want {
					t.Fatalf("gin context got %q", got)
				}
				c.Request.Header.Set("X-Forwarded-For", "203.0.113.1")
				if got := trustedClientIP(c); got != tc.want {
					t.Fatalf("request identity changed to %q", got)
				}
			}
		})
	}
}

func TestPublicRoutingRejectsAssertedGeography(t *testing.T) {
	prevReader := geoipReader
	geoipReader = nil
	t.Cleanup(func() { geoipReader = prevReader })
	useTrustedProxyCIDRs(t, "127.0.0.1/32")
	for _, peer := range []string{"192.0.2.5:7000", "127.0.0.1:7000"} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequestWithContext(context.Background(), "GET", "/stream?lat=52&lon=5", nil)
		c.Request.RemoteAddr = peer
		for _, name := range []string{"CF-IPLatitude", "CF-IPLongitude", "X-Latitude", "X-Longitude"} {
			c.Request.Header.Set(name, "10")
		}
		if lat := getLatLon(c, c.Request.URL.Query(), "lat", "X-Latitude"); !math.IsNaN(lat) {
			t.Fatalf("public lat asserted through %s: %v", peer, lat)
		}
		if lon := getLatLon(c, c.Request.URL.Query(), "lon", "X-Longitude"); !math.IsNaN(lon) {
			t.Fatalf("public lon asserted through %s: %v", peer, lon)
		}
	}
}

func TestPublicRoutingLooksUpTrustedClientGeography(t *testing.T) {
	prevReader, prevCache := geoipReader, geoipCache
	geoipReader = &geoip.Reader{}
	geoipCache = cache.New(cache.Options{TTL: time.Minute, MaxEntries: 4}, cache.MetricsHooks{})
	geoipCache.Set("198.51.100.5", &geoip.GeoData{Latitude: 38.9, Longitude: -77}, time.Minute)
	geoipCache.Set("127.0.0.1", &geoip.GeoData{Latitude: 52.4, Longitude: 4.9}, time.Minute)
	t.Cleanup(func() { geoipReader, geoipCache = prevReader, prevCache })
	useTrustedProxyCIDRs(t, "127.0.0.1/32")
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequestWithContext(context.Background(), "GET", "/stream?lat=52&lon=5", nil)
	c.Request.RemoteAddr = "127.0.0.1:7000"
	c.Request.Header.Set("X-Forwarded-For", "198.51.100.5")
	c.Request.Header.Set("CF-IPLatitude", "10")
	c.Request.Header.Set("CF-IPLongitude", "20")
	if lat := getLatLon(c, c.Request.URL.Query(), "lat", "X-Latitude"); lat != 38.9 {
		t.Fatalf("client latitude = %v, want 38.9", lat)
	}
	if lon := getLatLon(c, c.Request.URL.Query(), "lon", "X-Longitude"); lon != -77 {
		t.Fatalf("client longitude = %v, want -77", lon)
	}
}

func TestNodeCoordinatesAreBounded(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthenticatedNodeCluster, "source")
	for _, value := range []string{"NaN", "+Inf", "-Inf", "91", "-91"} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequestWithContext(ctx, "GET", "/", nil)
		c.Request.Header.Set("X-Latitude", value)
		if got := getLatLon(c, nil, "lat", "X-Latitude"); !math.IsNaN(got) {
			t.Fatalf("accepted invalid coordinate %q: %v", value, got)
		}
	}
}

func TestRoutingTelemetryUsesTrustedIdentity(t *testing.T) {
	prevQueue, prevDisabled, prevReader := routingEventQueue, routingEventsDisabled, geoipReader
	routingEventQueue = make(chan queuedRoutingEvent, 1)
	routingEventsDisabled, geoipReader = false, nil
	t.Cleanup(func() {
		routingEventQueue, routingEventsDisabled, geoipReader = prevQueue, prevDisabled, prevReader
	})
	useTrustedProxyCIDRs(t, "")
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequestWithContext(context.Background(), "GET", "/stream", nil)
	c.Request.RemoteAddr = "192.0.2.5:7000"
	for _, name := range []string{"CF-Connecting-IP", "X-Forwarded-For", "X-Real-IP"} {
		c.Request.Header.Set(name, "198.51.100.99")
	}
	c.Request.Header.Set("CF-IPCountry", "US")
	c.Request.Header.Set("X-Country-Code", "US")
	postBalancingEventExWithIdentity(c, "stream", "", 0, math.NaN(), math.NaN(), "success", "", 0, 0, "", 0, "", &routingEventIdentity{TenantID: "tenant"})
	select {
	case item := <-routingEventQueue:
		if item.event.ClientIP != "192.0.2.5" || item.event.ClientCountry != "" {
			t.Fatalf("telemetry accepted asserted identity: %+v", item.event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("routing event not queued")
	}
}
