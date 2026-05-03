package bunny

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestClientUsesCallerContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	client := NewClient("test-key")
	client.baseURL = server.URL

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err := client.ListZones(ctx)
	if err == nil {
		t.Fatal("expected context deadline error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context deadline exceeded, got %v", err)
	}
}

func TestRecordIdentityIgnoresGeolocationConfig(t *testing.T) {
	oldLat, oldLon := 52.3676, 4.9041
	newLat, newLon := 40.7128, -74.0060
	current := Record{
		Type:                 RecordTypeA,
		Name:                 "edge-ingest",
		Value:                "198.51.100.10",
		TTL:                  60,
		Weight:               100,
		MonitorType:          MonitorTypeNone,
		SmartRoutingType:     SmartRoutingGeolocation,
		GeolocationLatitude:  &oldLat,
		GeolocationLongitude: &oldLon,
	}
	desired := current
	desired.GeolocationLatitude = &newLat
	desired.GeolocationLongitude = &newLon

	if !sameRecordIdentity(current, desired) {
		t.Fatal("record identity should not include geolocation coordinates")
	}
	if sameRecordConfig(current, desired) {
		t.Fatal("record config should detect geolocation coordinate changes")
	}
}
