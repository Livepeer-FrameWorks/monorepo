package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func TestServiceCacheMetricsNeverExposeLookupKey(t *testing.T) {
	vectors := cacheMetricVectors{
		hits:   prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_cache_hits_total"}, []string{"cache"}),
		misses: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_cache_misses_total"}, []string{"cache"}),
		stale:  prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_cache_stale_total"}, []string{"cache"}),
		stores: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_cache_stores_total"}, []string{"cache", "ok"}),
		errors: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_cache_errors_total"}, []string{"cache"}),
	}
	registry := prometheus.NewRegistry()
	registry.MustRegister(vectors.hits, vectors.misses, vectors.stale, vectors.stores, vectors.errors)

	const secretKey = "commodore:validate:sk_live_metric_secret:cluster:cluster-1"
	c := newServiceCache("commodore", time.Minute, 0, time.Minute, 10, vectors)
	c.SetDefault(secretKey, "admitted")
	if _, ok, err := c.Get(context.Background(), secretKey, nil); err != nil || !ok {
		t.Fatalf("cache hit = (%t, %v), want successful hit", ok, err)
	}

	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	var rendered strings.Builder
	for _, family := range families {
		rendered.WriteString(family.String())
	}
	got := rendered.String()
	if strings.Contains(got, secretKey) || strings.Contains(got, "sk_live_metric_secret") {
		t.Fatalf("cache lookup key leaked into Prometheus labels: %s", got)
	}
	if !strings.Contains(got, `value:"commodore"`) {
		t.Fatalf("fixed cache namespace missing from metrics: %s", got)
	}
}
