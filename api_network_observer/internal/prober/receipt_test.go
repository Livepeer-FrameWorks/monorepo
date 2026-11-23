package prober

import (
	"encoding/json"
	obs "github.com/Livepeer-FrameWorks/monorepo/pkg/networkobservation"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHTTP200WithoutMatchingReceiptNeverAcknowledges(t *testing.T) {
	for _, body := range []string{`<html>maintenance</html>`, `{}`, `{"accepted":true,"event_id":"wrong"}`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer server.Close()
			p := &Prober{ID: "site", URL: server.URL, Client: server.Client()}
			raw, _ := json.Marshal(obs.Batch{ID: uuid.NewString(), ObserverID: "site"})
			result := p.sendRecords(t.Context(), [][]byte{raw})[0]
			if result.Delivered || result.QuarantineCode != "" || result.Err == nil {
				t.Fatalf("HTTP success without a committed record receipt discarded original: %+v", result)
			}
		})
	}
}

func TestReceiptBackpressureHonorsRetryAfter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	p := &Prober{ID: "site", URL: server.URL, Client: server.Client()}
	p.bulkRecords.Store(64)
	p.bulkBytes.Store(380 << 10)
	for _, count := range []int{1, 2} {
		var records [][]byte
		for range count {
			raw, _ := json.Marshal(obs.Batch{ID: uuid.NewString(), ObserverID: "site"})
			records = append(records, raw)
		}
		for _, result := range p.sendRecords(t.Context(), records) {
			if result.Delivered || result.RetryAfter != 7*time.Second {
				t.Fatalf("%d-record receiver backpressure ignored: %+v", count, result)
			}
		}
	}
}
