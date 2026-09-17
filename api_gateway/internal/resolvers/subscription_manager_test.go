package resolvers

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	signalmanclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/signalman"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	signalmanpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/signalman"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

type fakeStream struct {
	key    signalmanclient.StreamKey
	addr   string
	events chan *signalmanpb.SignalmanEvent
	done   chan struct{}
	once   sync.Once
}

func (s *fakeStream) Recv() (*signalmanpb.SignalmanEvent, error) {
	select {
	case event, ok := <-s.events:
		if !ok {
			return nil, io.EOF
		}
		return event, nil
	case <-s.done:
		return nil, context.Canceled
	}
}

func (s *fakeStream) Close() {
	s.once.Do(func() { close(s.done) })
}

func (s *fakeStream) isClosed() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

func (s *fakeStream) push(t *testing.T, event *signalmanpb.SignalmanEvent) {
	t.Helper()
	select {
	case s.events <- event:
	case <-time.After(2 * time.Second):
		t.Fatalf("upstream %s did not accept an event", s.key.Channel)
	}
}

type fakeOpener struct {
	mu       sync.Mutex
	failing  map[string]bool
	attempts []string
	opened   chan *fakeStream
}

func newFakeOpener() *fakeOpener {
	return &fakeOpener{failing: map[string]bool{}, opened: make(chan *fakeStream, 64)}
}

func (o *fakeOpener) Open(ctx context.Context, addr string, key signalmanclient.StreamKey) (signalmanclient.EventStream, error) {
	o.mu.Lock()
	o.attempts = append(o.attempts, addr)
	failing := o.failing[addr]
	o.mu.Unlock()
	if failing {
		return nil, fmt.Errorf("replica %s unavailable", addr)
	}
	stream := &fakeStream{
		key:    key,
		addr:   addr,
		events: make(chan *signalmanpb.SignalmanEvent, 16),
		done:   make(chan struct{}),
	}
	go func() {
		select {
		case <-ctx.Done():
			stream.Close()
		case <-stream.done:
		}
	}()
	o.opened <- stream
	return stream, nil
}

func (o *fakeOpener) Close() error { return nil }

func (o *fakeOpener) waitOpened(t *testing.T) *fakeStream {
	t.Helper()
	select {
	case stream := <-o.opened:
		return stream
	case <-time.After(3 * time.Second):
		t.Fatal("no upstream stream opened")
		return nil
	}
}

func (o *fakeOpener) waitOpenedByChannel(t *testing.T, n int) map[signalmanpb.Channel]*fakeStream {
	t.Helper()
	byChannel := map[signalmanpb.Channel]*fakeStream{}
	for range n {
		stream := o.waitOpened(t)
		if _, dup := byChannel[stream.key.Channel]; dup {
			t.Fatalf("opened a second upstream for channel %s", stream.key.Channel)
		}
		byChannel[stream.key.Channel] = stream
	}
	select {
	case extra := <-o.opened:
		t.Fatalf("opened unexpected extra upstream %+v", extra.key)
	case <-time.After(50 * time.Millisecond):
	}
	return byChannel
}

func testSubscriptionManager(t *testing.T, opener *fakeOpener, cfg SubscriptionManagerConfig) *SubscriptionManager {
	t.Helper()
	if len(cfg.SignalmanAddrs) == 0 {
		cfg.SignalmanAddrs = []string{"signalman.internal:19005"}
	}
	sm := newSubscriptionManager(logging.NewLoggerWithService("test"), cfg, opener)
	sm.linger = 20 * time.Millisecond
	t.Cleanup(func() {
		if err := sm.Shutdown(); err != nil {
			t.Errorf("shutdown subscription manager: %v", err)
		}
	})
	return sm
}

func nodeLifecycleEvent(tenantID string) *signalmanpb.SignalmanEvent {
	return &signalmanpb.SignalmanEvent{
		EventType: signalmanpb.EventType_EVENT_TYPE_NODE_LIFECYCLE_UPDATE,
		Channel:   signalmanpb.Channel_CHANNEL_SYSTEM,
		TenantId:  &tenantID,
		Data: &signalmanpb.EventData{Payload: &signalmanpb.EventData_NodeLifecycle{
			NodeLifecycle: &ipcpb.NodeLifecycleUpdate{},
		}},
	}
}

func clientLifecycleEvent(tenantID string) *signalmanpb.SignalmanEvent {
	return &signalmanpb.SignalmanEvent{
		EventType: signalmanpb.EventType_EVENT_TYPE_CLIENT_LIFECYCLE_UPDATE,
		Channel:   signalmanpb.Channel_CHANNEL_ANALYTICS,
		TenantId:  &tenantID,
		Data: &signalmanpb.EventData{Payload: &signalmanpb.EventData_ClientLifecycle{
			ClientLifecycle: &ipcpb.ClientLifecycleUpdate{},
		}},
	}
}

func receiveUpdate[T any](t *testing.T, updates <-chan T) T {
	t.Helper()
	select {
	case update, ok := <-updates:
		if !ok {
			t.Fatal("subscription output closed before an update arrived")
		}
		return update
	case <-time.After(3 * time.Second):
		t.Fatal("subscription did not receive an update")
	}
	var zero T
	return zero
}

func expectNoUpdate[T any](t *testing.T, updates <-chan T) {
	t.Helper()
	select {
	case update, ok := <-updates:
		if ok {
			t.Fatalf("unexpected update %v", update)
		}
	case <-time.After(50 * time.Millisecond):
	}
}

func expectClosed[T any](t *testing.T, updates <-chan T) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case _, ok := <-updates:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("subscription output was not closed")
		}
	}
}

func TestTenantMismatch(t *testing.T) {
	tenant := "tenant-1"
	otherTenant := "tenant-2"

	tests := []struct {
		name     string
		tenantID string
		event    *signalmanpb.SignalmanEvent
		want     bool
	}{
		{name: "empty tenant skips mismatch", tenantID: "", event: &signalmanpb.SignalmanEvent{TenantId: &tenant}, want: false},
		{name: "missing event tenant allowed (infra/system broadcasts)", tenantID: tenant, event: &signalmanpb.SignalmanEvent{}, want: false},
		{name: "tenant match passes", tenantID: tenant, event: &signalmanpb.SignalmanEvent{TenantId: &tenant}, want: false},
		{name: "tenant mismatch blocks", tenantID: tenant, event: &signalmanpb.SignalmanEvent{TenantId: &otherTenant}, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tenantMismatch(tt.tenantID, tt.event); got != tt.want {
				t.Fatalf("tenantMismatch = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConcurrentSubscriptionsShareUpstreamsAndEachReceiveEvents(t *testing.T) {
	opener := newFakeOpener()
	sm := testSubscriptionManager(t, opener, SubscriptionManagerConfig{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	config := ConnectionConfig{UserID: "user-1", TenantID: "tenant-a"}

	systemA, err := sm.SubscribeToSystem(ctx, config)
	if err != nil {
		t.Fatalf("SubscribeToSystem A: %v", err)
	}
	systemB, err := sm.SubscribeToSystem(ctx, ConnectionConfig{UserID: "user-2", TenantID: "tenant-a"})
	if err != nil {
		t.Fatalf("SubscribeToSystem B: %v", err)
	}
	analytics, err := sm.SubscribeToAnalytics(ctx, config, nil)
	if err != nil {
		t.Fatalf("SubscribeToAnalytics: %v", err)
	}
	firehose, err := sm.SubscribeToFirehose(ctx, config)
	if err != nil {
		t.Fatalf("SubscribeToFirehose: %v", err)
	}

	upstreams := opener.waitOpenedByChannel(t, 4)
	for _, channel := range []signalmanpb.Channel{
		signalmanpb.Channel_CHANNEL_SYSTEM,
		signalmanpb.Channel_CHANNEL_ANALYTICS,
		signalmanpb.Channel_CHANNEL_STREAMS,
		signalmanpb.Channel_CHANNEL_AI,
	} {
		stream := upstreams[channel]
		if stream == nil || stream.key.TenantID != "tenant-a" {
			t.Fatalf("upstream for %s = %+v, want one tenant-a stream", channel, stream)
		}
	}

	upstreams[signalmanpb.Channel_CHANNEL_SYSTEM].push(t, nodeLifecycleEvent("tenant-a"))
	receiveUpdate(t, systemA)
	receiveUpdate(t, systemB)
	if event := receiveUpdate(t, firehose); event.SystemHealthEvent == nil {
		t.Fatalf("firehose event = %+v, want system health", event)
	}

	upstreams[signalmanpb.Channel_CHANNEL_ANALYTICS].push(t, clientLifecycleEvent("tenant-a"))
	receiveUpdate(t, analytics)
	if event := receiveUpdate(t, firehose); event.ViewerMetrics == nil {
		t.Fatalf("firehose event = %+v, want viewer metrics", event)
	}
	expectNoUpdate(t, systemA)
	expectNoUpdate(t, systemB)
}

func TestCancelingOneSubscriptionKeepsSiblingAndLingersUpstream(t *testing.T) {
	opener := newFakeOpener()
	sm := testSubscriptionManager(t, opener, SubscriptionManagerConfig{})
	config := ConnectionConfig{TenantID: "tenant-a"}

	firstCtx, cancelFirst := context.WithCancel(context.Background())
	first, err := sm.SubscribeToSystem(firstCtx, config)
	if err != nil {
		t.Fatalf("SubscribeToSystem first: %v", err)
	}
	secondCtx, cancelSecond := context.WithCancel(context.Background())
	defer cancelSecond()
	second, err := sm.SubscribeToSystem(secondCtx, config)
	if err != nil {
		t.Fatalf("SubscribeToSystem second: %v", err)
	}
	upstream := opener.waitOpened(t)

	cancelFirst()
	expectClosed(t, first)
	upstream.push(t, nodeLifecycleEvent("tenant-a"))
	receiveUpdate(t, second)
	if upstream.isClosed() {
		t.Fatal("cancelling one subscription closed the shared upstream")
	}

	cancelSecond()
	expectClosed(t, second)
	deadline := time.Now().Add(2 * time.Second)
	for !upstream.isClosed() {
		if time.Now().After(deadline) {
			t.Fatal("upstream stayed open after its last subscription ended and linger elapsed")
		}
		time.Sleep(5 * time.Millisecond)
	}

	third, err := sm.SubscribeToSystem(context.Background(), config)
	if err != nil {
		t.Fatalf("SubscribeToSystem after linger: %v", err)
	}
	replacement := opener.waitOpened(t)
	replacement.push(t, nodeLifecycleEvent("tenant-a"))
	receiveUpdate(t, third)
}

func TestUpstreamFailureReconnectsWithoutResubscribing(t *testing.T) {
	opener := newFakeOpener()
	sm := testSubscriptionManager(t, opener, SubscriptionManagerConfig{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	updates, err := sm.SubscribeToSystem(ctx, ConnectionConfig{TenantID: "tenant-a"})
	if err != nil {
		t.Fatalf("SubscribeToSystem: %v", err)
	}
	failed := opener.waitOpened(t)
	close(failed.events)

	reconnected := opener.waitOpened(t)
	if reconnected == failed {
		t.Fatal("reconnect reused the failed stream")
	}
	reconnected.push(t, nodeLifecycleEvent("tenant-a"))
	receiveUpdate(t, updates)
}

func TestTenantsGetSeparateUpstreams(t *testing.T) {
	opener := newFakeOpener()
	sm := testSubscriptionManager(t, opener, SubscriptionManagerConfig{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tenantA, err := sm.SubscribeToSystem(ctx, ConnectionConfig{TenantID: "tenant-a"})
	if err != nil {
		t.Fatalf("SubscribeToSystem tenant-a: %v", err)
	}
	tenantB, err := sm.SubscribeToSystem(ctx, ConnectionConfig{TenantID: "tenant-b"})
	if err != nil {
		t.Fatalf("SubscribeToSystem tenant-b: %v", err)
	}
	byTenant := map[string]*fakeStream{}
	for range 2 {
		stream := opener.waitOpened(t)
		byTenant[stream.key.TenantID] = stream
	}
	if byTenant["tenant-a"] == nil || byTenant["tenant-b"] == nil {
		t.Fatalf("upstreams by tenant = %v, want one each", byTenant)
	}

	byTenant["tenant-a"].push(t, nodeLifecycleEvent("tenant-a"))
	receiveUpdate(t, tenantA)
	expectNoUpdate(t, tenantB)
}

func TestSubscriptionLimitCountsGraphQLSubscriptions(t *testing.T) {
	opener := newFakeOpener()
	sm := testSubscriptionManager(t, opener, SubscriptionManagerConfig{MaxSubscriptionsPerTenant: 2})
	config := ConnectionConfig{TenantID: "tenant-a"}

	if _, err := sm.SubscribeToFirehose(context.Background(), config); err != nil {
		t.Fatalf("firehose counts as one subscription: %v", err)
	}
	systemCtx, cancelSystem := context.WithCancel(context.Background())
	system, err := sm.SubscribeToSystem(systemCtx, config)
	if err != nil {
		t.Fatalf("second subscription within limit: %v", err)
	}
	if _, err := sm.SubscribeToAnalytics(context.Background(), config, nil); err == nil || !strings.Contains(err.Error(), "max number of active subscriptions") {
		t.Fatalf("third subscription err = %v, want tenant limit", err)
	}
	if _, err := sm.SubscribeToSystem(context.Background(), ConnectionConfig{TenantID: "tenant-b"}); err != nil {
		t.Fatalf("another tenant is not limited by tenant-a: %v", err)
	}

	cancelSystem()
	expectClosed(t, system)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := sm.SubscribeToAnalytics(context.Background(), config, nil); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("subscription slot was not released after cancel")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestUpstreamOpenFailsOverToNextReplica(t *testing.T) {
	opener := newFakeOpener()
	addrs := []string{"signalman-a:19005", "signalman-b:19005", "signalman-c:19005"}
	order := rotateAddrs(addrs, "tenant-a")
	opener.failing[order[0]] = true
	sm := testSubscriptionManager(t, opener, SubscriptionManagerConfig{SignalmanAddrs: addrs})

	if _, err := sm.SubscribeToSystem(context.Background(), ConnectionConfig{TenantID: "tenant-a"}); err != nil {
		t.Fatalf("SubscribeToSystem: %v", err)
	}
	stream := opener.waitOpened(t)
	if stream.addr != order[1] {
		t.Fatalf("upstream opened on %s, want failover to %s", stream.addr, order[1])
	}
	opener.mu.Lock()
	attempts := append([]string(nil), opener.attempts...)
	opener.mu.Unlock()
	if len(attempts) < 2 || attempts[0] != order[0] {
		t.Fatalf("open attempts = %v, want %s first", attempts, order[0])
	}
}

func TestSubscribeWithoutSignalmanAddrsFails(t *testing.T) {
	sm := newSubscriptionManager(logging.NewLoggerWithService("test"), SubscriptionManagerConfig{}, newFakeOpener())
	t.Cleanup(func() { _ = sm.Shutdown() })
	if _, err := sm.SubscribeToSystem(context.Background(), ConnectionConfig{TenantID: "tenant-a"}); err == nil || !strings.Contains(err.Error(), "no Signalman addresses configured") {
		t.Fatalf("SubscribeToSystem err = %v, want no addresses error", err)
	}
}

func TestShutdownClosesServedSubscriptions(t *testing.T) {
	opener := newFakeOpener()
	sm := testSubscriptionManager(t, opener, SubscriptionManagerConfig{})

	updates, err := sm.SubscribeToSystem(context.Background(), ConnectionConfig{TenantID: "tenant-a"})
	if err != nil {
		t.Fatalf("SubscribeToSystem: %v", err)
	}
	upstream := opener.waitOpened(t)

	if err := sm.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	expectClosed(t, updates)
	if !upstream.isClosed() {
		t.Fatal("Shutdown left the upstream stream open")
	}
	if _, err := sm.SubscribeToSystem(context.Background(), ConnectionConfig{TenantID: "tenant-a"}); err == nil {
		t.Fatal("SubscribeToSystem succeeded after Shutdown")
	}
}

func TestSubscriptionActiveGaugeTracksServedSubscription(t *testing.T) {
	metrics := &GraphQLMetrics{
		SubscriptionsActive: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "test_subscription_active_count",
			Help: "Active test subscriptions",
		}, []string{"operation"}),
	}
	opener := newFakeOpener()
	sm := testSubscriptionManager(t, opener, SubscriptionManagerConfig{Metrics: metrics})

	ctx, cancel := context.WithCancel(context.Background())
	updates, err := sm.SubscribeToSystem(ctx, ConnectionConfig{TenantID: "tenant-1"})
	if err != nil {
		t.Fatalf("SubscribeToSystem: %v", err)
	}
	waitForGauge(t, metrics.SubscriptionsActive.WithLabelValues("system"), 1)
	cancel()
	expectClosed(t, updates)
	waitForGauge(t, metrics.SubscriptionsActive.WithLabelValues("system"), 0)
}

func waitForGauge(t *testing.T, metric prometheus.Gauge, want float64) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		if got := gaugeValue(t, metric); got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("gauge = %v, want %v", gaugeValue(t, metric), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func gaugeValue(t *testing.T, metric prometheus.Gauge) float64 {
	t.Helper()
	dtoMetric := &dto.Metric{}
	if err := metric.Write(dtoMetric); err != nil {
		t.Fatalf("write gauge metric: %v", err)
	}
	return dtoMetric.GetGauge().GetValue()
}
