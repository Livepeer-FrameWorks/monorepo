package cloudflare

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestDNSRecordLifecycle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/zones/zone-1/dns_records":
			if got := r.Header.Get("Authorization"); got != "Bearer token" {
				t.Fatalf("unexpected auth header: %s", got)
			}
			var record DNSRecord
			if err := json.NewDecoder(r.Body).Decode(&record); err != nil {
				t.Fatalf("decode create record: %v", err)
			}
			writeAPIResponse(w, http.StatusOK, DNSRecord{
				ID:      "record-1",
				Type:    record.Type,
				Name:    record.Name,
				Content: record.Content,
				TTL:     record.TTL,
				Proxied: record.Proxied,
			})
		case r.Method == http.MethodPut && r.URL.Path == "/zones/zone-1/dns_records/record-1":
			var record DNSRecord
			if err := json.NewDecoder(r.Body).Decode(&record); err != nil {
				t.Fatalf("decode update record: %v", err)
			}
			if record.Content != "2.2.2.2" {
				t.Fatalf("unexpected update content: %s", record.Content)
			}
			writeAPIResponse(w, http.StatusOK, DNSRecord{
				ID:      "record-1",
				Type:    record.Type,
				Name:    record.Name,
				Content: record.Content,
				TTL:     record.TTL,
				Proxied: record.Proxied,
			})
		case r.Method == http.MethodDelete && r.URL.Path == "/zones/zone-1/dns_records/record-1":
			writeAPIResponse(w, http.StatusOK, map[string]string{"id": "record-1"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := NewClient("token", "zone-1", "account-1")
	client.baseURL = server.URL

	created, err := client.CreateDNSRecord(DNSRecord{
		Type:    "A",
		Name:    "edge.example.com",
		Content: "1.1.1.1",
		TTL:     120,
		Proxied: true,
	})
	if err != nil {
		t.Fatalf("create record: %v", err)
	}
	if created.ID != "record-1" {
		t.Fatalf("unexpected created record id: %s", created.ID)
	}

	created.Content = "2.2.2.2"
	updated, err := client.UpdateDNSRecord(created.ID, *created)
	if err != nil {
		t.Fatalf("update record: %v", err)
	}
	if updated.Content != "2.2.2.2" {
		t.Fatalf("unexpected updated content: %s", updated.Content)
	}

	if err := client.DeleteDNSRecord(created.ID); err != nil {
		t.Fatalf("delete record: %v", err)
	}
}

func TestDNSRecordRetryBehavior(t *testing.T) {
	var postAttempts int32
	var getAttempts int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/zones/zone-1/dns_records":
			atomic.AddInt32(&postAttempts, 1)
			writeAPIError(w, http.StatusInternalServerError, "temporary")
		case r.Method == http.MethodGet && r.URL.Path == "/zones/zone-1/dns_records":
			attempt := atomic.AddInt32(&getAttempts, 1)
			if attempt == 1 {
				writeAPIError(w, http.StatusInternalServerError, "temporary")
				return
			}
			writeAPIResponse(w, http.StatusOK, []DNSRecord{{
				ID:      "record-1",
				Type:    "A",
				Name:    "edge.example.com",
				Content: "1.1.1.1",
			}}, &ResultInfo{TotalPages: 1})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := NewClient("token", "zone-1", "account-1")
	client.baseURL = server.URL

	_, err := client.CreateDNSRecord(DNSRecord{
		Type:    "A",
		Name:    "edge.example.com",
		Content: "1.1.1.1",
		TTL:     120,
		Proxied: true,
	})
	if err == nil {
		t.Fatalf("expected create error")
	}
	if postAttempts != 1 {
		t.Fatalf("expected 1 POST attempt, got %d", postAttempts)
	}

	records, err := client.ListDNSRecords("A", "edge.example.com")
	if err != nil {
		t.Fatalf("list records: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if getAttempts < 2 {
		t.Fatalf("expected retries for GET, got %d", getAttempts)
	}
}

func writeAPIResponse(w http.ResponseWriter, status int, result interface{}, info ...*ResultInfo) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	resp := APIResponse{Success: status < http.StatusBadRequest, Result: result}
	if len(info) > 0 {
		resp.ResultInfo = info[0]
	}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		panic(err)
	}
}

func writeAPIError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	resp := APIResponse{
		Success: false,
		Errors: []APIError{{
			Code:    status,
			Message: message,
		}},
	}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		panic(err)
	}
}
