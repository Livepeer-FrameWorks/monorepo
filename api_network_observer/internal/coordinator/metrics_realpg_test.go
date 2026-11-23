//go:build schema_verify

package coordinator

import (
	"database/sql"
	"errors"
	"io"
	"net/http/httptest"
	"testing"
	"time"

	"frameworks/api_network_observer/internal/store"
	obs "github.com/Livepeer-FrameWorks/monorepo/pkg/networkobservation"
	"github.com/google/uuid"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
)

func TestObserverMetricsContract_RealPG(t *testing.T) {
	db := startObserverPostgres(t)
	a := &API{Store: &store.Store{DB: db, TenantID: uuid.NewString()}}
	observer := "site\"\\name"
	now := time.Now().UTC()
	if updateErr := a.Store.Update(t.Context(), func(s *store.State, _ *sql.Tx) error {
		s.Observers[observer] = obs.Observer{ID: observer, Kind: "prober", Region: "ap-south", LastSeen: now, Delivery: &obs.DeliveryHealth{DroppedRecords: 42, QuarantinedRecords: 3, ClockSkewSeconds: -1.25}}
		return nil
	}); updateErr != nil {
		t.Fatal(updateErr)
	}
	scrape := func() map[string]*dto.MetricFamily {
		t.Helper()
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), "GET", "/metrics", nil))
		if w.Code != 200 {
			t.Fatalf("metrics status %d: %s", w.Code, w.Body)
		}
		decoder := expfmt.NewDecoder(w.Body, expfmt.ResponseFormat(w.Header()))
		families := map[string]*dto.MetricFamily{}
		for {
			var family dto.MetricFamily
			decodeErr := decoder.Decode(&family)
			if errors.Is(decodeErr, io.EOF) {
				return families
			}
			if decodeErr != nil {
				t.Fatalf("invalid Prometheus scrape: %v", decodeErr)
			}
			families[family.GetName()] = &family
		}
	}
	families := scrape()
	health := families["network_observer_health_read_success"]
	if health.GetType() != dto.MetricType_GAUGE || len(health.GetMetric()) != 1 || health.Metric[0].GetGauge().GetValue() != 1 {
		t.Fatalf("healthy scrape lacks a typed health gauge: %v", health)
	}
	for name, want := range map[string]float64{"network_observer_prober_dropped_records_total": 42, "network_observer_prober_quarantined_records_total": 3} {
		family := families[name]
		if family.GetType() != dto.MetricType_COUNTER || len(family.GetMetric()) != 1 || family.Metric[0].GetCounter().GetValue() != want {
			t.Fatalf("durable loss count is not a typed counter: %s=%v", name, family)
		}
		labels := map[string]string{}
		for _, label := range family.Metric[0].GetLabel() {
			labels[label.GetName()] = label.GetValue()
		}
		if labels["observer"] != observer || labels["region"] != "ap-south" {
			t.Fatalf("metric labels lost identity or escaping: %v", labels)
		}
	}
	if closeErr := db.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	health = scrape()["network_observer_health_read_success"]
	if health.GetType() != dto.MetricType_GAUGE || len(health.GetMetric()) != 1 || health.Metric[0].GetGauge().GetValue() != 0 {
		t.Fatalf("database failure did not clear scrape health: %v", health)
	}
}
