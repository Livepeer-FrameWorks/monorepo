package bunny

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
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

func TestReconcileRecordSetRetriesStaleBunnyRecordID(t *testing.T) {
	var getCount atomic.Int32
	var postCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/dnszone/123":
			recordID := int64(10)
			if getCount.Add(1) > 1 {
				recordID = 11
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"Id":123,"Domain":"edge.frameworks.network","Records":[{"Id":%d,"Type":0,"Name":"","Value":"203.0.113.10","Ttl":30}]}`, recordID)
		case r.Method == http.MethodPost && r.URL.Path == "/dnszone/123/records/10":
			postCount.Add(1)
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"ErrorKey":"dnszone.record_not_found","Field":"RecordId","Message":"The requested DNS zone record was not found"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/dnszone/123/records/11":
			postCount.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClient("test-key")
	client.baseURL = server.URL

	err := client.ReconcileRecordSet(context.Background(), 123, "", RecordTypeA, []Record{{
		Value:  "203.0.113.10",
		TTL:    60,
		Weight: 100,
	}})
	if err != nil {
		t.Fatalf("ReconcileRecordSet returned error: %v", err)
	}
	if got := getCount.Load(); got != 2 {
		t.Fatalf("GET count = %d, want 2", got)
	}
	if got := postCount.Load(); got != 2 {
		t.Fatalf("POST count = %d, want 2", got)
	}
}
