package agent

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

type countingDNS struct {
	stops atomic.Int32
}

func (c *countingDNS) Start() {}

func (c *countingDNS) Stop() { c.stops.Add(1) }

func (c *countingDNS) UpdateRecords(map[string][]string) error { return nil }

func (c *countingDNS) SetQueriesMetric(_ *prometheus.CounterVec) {}

func TestStopIsIdempotentAcrossConcurrentCallers(t *testing.T) {
	dns := &countingDNS{}
	agent := newTestAgent(t, &fakeMeshClient{}, &fakeWireguard{}, &fakeDNS{})
	agent.dnsServer = dns

	var wg sync.WaitGroup
	for range 4 {
		wg.Go(agent.Stop)
	}
	wg.Wait()
	agent.Stop()

	if got := dns.stops.Load(); got != 1 {
		t.Fatalf("DNS server stopped %d times, want 1", got)
	}
	select {
	case <-agent.stopChan:
	default:
		t.Fatal("stop channel was not closed")
	}
}

func TestStartedReflectsCompletedStartup(t *testing.T) {
	agent := newTestAgent(t, &fakeMeshClient{}, &fakeWireguard{}, &fakeDNS{})
	if agent.Started() {
		t.Fatal("agent reports started before Start finished")
	}
	agent.healthy.Store(true)
	if !agent.Started() {
		t.Fatal("agent does not report started after startup")
	}
}
