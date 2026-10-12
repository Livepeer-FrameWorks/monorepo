//go:build schema_verify

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sync"
	"testing"
	"time"

	"frameworks/api_analytics_ingest/internal/handlers"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerch"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/topology"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

var insertTablePattern = regexp.MustCompile(`(?is)INSERT\s+INTO\s+(?:periscope\.)?([a-z0-9_]+)`)

// insertCountingConn counts the native INSERT batches each table receives.
type insertCountingConn struct {
	driver.Conn
	mu    sync.Mutex
	sends map[string]int
}

func (c *insertCountingConn) PrepareBatch(ctx context.Context, query string, opts ...driver.PrepareBatchOption) (driver.Batch, error) {
	batch, err := c.Conn.PrepareBatch(ctx, query, opts...)
	if err != nil {
		return nil, err
	}
	table := ""
	if m := insertTablePattern.FindStringSubmatch(query); m != nil {
		table = m[1]
	}
	return &countingBatch{Batch: batch, onSend: func() {
		c.mu.Lock()
		c.sends[table]++
		c.mu.Unlock()
	}}, nil
}

func (c *insertCountingConn) inserts(table string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sends[table]
}

type countingBatch struct {
	driver.Batch
	onSend func()
}

func (b *countingBatch) Send() error {
	err := b.Batch.Send()
	if err == nil {
		b.onSend()
	}
	return err
}

func testIngestMetrics() *handlers.PeriscopeMetrics {
	counter := func(name string, labels ...string) *prometheus.CounterVec {
		return prometheus.NewCounterVec(prometheus.CounterOpts{Name: name}, labels)
	}
	return &handlers.PeriscopeMetrics{
		AnalyticsEvents:         counter("analytics_events_total", "event_type", "status"),
		BatchProcessingDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "batch_processing_duration_seconds"}, []string{"source"}),
		HandlerWrites:           counter("handler_writes_total", "table", "status"),
		ClickHouseInserts:       counter("clickhouse_inserts_total", "table", "status"),
		DuplicateEvents:         counter("duplicate_events_total", "event_type"),
		DLQMessages:             counter("dlq_messages_total", "topic", "error_type"),
		KafkaMessages:           counter("kafka_messages_total", "topic", "action", "status"),
		KafkaDuration:           prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "kafka_duration_seconds"}, []string{"action"}),
	}
}

func analyticsRecord(t *testing.T, eventType, tenantID string, ts time.Time, mt *ipcpb.MistTrigger) *kgo.Record {
	t.Helper()
	raw, err := protojson.Marshal(mt)
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	value, err := json.Marshal(kafka.AnalyticsEvent{
		EventID: uuid.NewString(), EventType: eventType, Timestamp: ts, Source: "foghorn",
		TenantID: tenantID, Data: data,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &kgo.Record{Topic: topology.TopicAnalyticsEvents, Value: value}
}

// ingestHarness runs the production ingest consumer against an in-process
// Kafka cluster and a real ClickHouse.
type ingestHarness struct {
	t       *testing.T
	cluster *kfake.Cluster
	admin   *kadm.Client
	conn    *insertCountingConn
	group   string
}

func (h *ingestHarness) committed(topic string, partition int32) int64 {
	offsets, err := h.admin.FetchOffsets(context.Background(), h.group)
	if err != nil {
		return -1
	}
	o, ok := offsets.Lookup(topic, partition)
	if !ok || o.Err != nil {
		return -1
	}
	return o.At
}

// consumeUntil runs one consumer until the analytics partition's committed
// offset reaches end, then stops it and leaves the group.
func (h *ingestHarness) consumeUntil(end int64, timeout time.Duration) time.Duration {
	h.t.Helper()
	logger := logging.NewLogger()
	metrics := testIngestMetrics()
	handler := handlers.NewAnalyticsHandler(h.conn, logger, metrics)
	consumer, _, err := newIngestConsumer(ingestConsumerConfig{
		brokers: h.cluster.ListenAddrs(), groupID: h.group, clusterID: "test", clientID: "periscope-ingest-test",
		topics: ingestTopics{
			analytics:     topology.TopicAnalyticsEvents,
			serviceEvents: topology.TopicServiceEvents,
			domainEvents:  topology.TopicDomainEvents,
			rawTriggers:   topology.TopicRawMistTriggers,
		},
		dlqTopic: "decklog_events_dlq",
	}, handler, metrics, nil, logger)
	if err != nil {
		h.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(done)
		_ = consumer.Start(ctx)
	}()
	deadline := time.Now().Add(timeout)
	for h.committed(topology.TopicAnalyticsEvents, 0) < end {
		if time.Now().After(deadline) {
			cancel()
			<-done
			_ = consumer.Close()
			h.t.Fatalf("committed offset %d after %s, want %d", h.committed(topology.TopicAnalyticsEvents, 0), timeout, end)
		}
		time.Sleep(50 * time.Millisecond)
	}
	elapsed := time.Since(start)
	cancel()
	<-done
	_ = consumer.Close()
	return elapsed
}

func (h *ingestHarness) rewind(offset int64) {
	h.t.Helper()
	var offsets kadm.Offsets
	offsets.Add(kadm.Offset{Topic: topology.TopicAnalyticsEvents, Partition: 0, At: offset, LeaderEpoch: -1})
	resp, err := h.admin.CommitOffsets(context.Background(), h.group, offsets)
	if err != nil {
		h.t.Fatal(err)
	}
	if err := resp.Error(); err != nil {
		h.t.Fatal(err)
	}
}

func countRows(t *testing.T, conn database.ClickHouseNativeConn, query string, args ...any) uint64 {
	t.Helper()
	var n uint64
	if err := conn.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// TestIngestConsumerBatchesClickHouseInserts_RealClickHouse drives load
// balancing and viewer events through the production consumer path and
// requires each table's rows to land in a bounded number of INSERTs, not one
// INSERT per event. A redelivery of the whole log, and of a suffix that cuts
// the original poll batches differently, must not change the stored counts.
func TestIngestConsumerBatchesClickHouseInserts_RealClickHouse(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	native := dockerch.StartCurrent(t, root, "fw-periscope-batch-ingest").Native
	conn := &insertCountingConn{Conn: native, sends: map[string]int{}}

	cluster, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.SeedTopics(1,
		topology.TopicAnalyticsEvents, topology.TopicServiceEvents, topology.TopicDomainEvents,
		topology.TopicRawMistTriggers, "decklog_events_dlq"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cluster.Close)
	producer, err := kgo.NewClient(kgo.SeedBrokers(cluster.ListenAddrs()...))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(producer.Close)

	const routingEvents, viewerEvents = 400, 100
	tenantID, streamID := uuid.NewString(), uuid.NewString()
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	var records []*kgo.Record
	for i := 0; i < routingEvents+viewerEvents; i++ {
		ts := base.Add(time.Duration(i) * time.Second)
		if i%5 == 4 {
			records = append(records, analyticsRecord(t, "viewer_connect", tenantID, ts, &ipcpb.MistTrigger{
				TriggerType: "USER_NEW", NodeId: "edge-1", TenantId: proto.String(tenantID), StreamId: proto.String(streamID),
				TriggerPayload: &ipcpb.MistTrigger_ViewerConnect{ViewerConnect: &ipcpb.ViewerConnectTrigger{
					StreamName: "live+batch", SessionId: fmt.Sprintf("session-%d", i), Connector: "HLS", Host: "198.51.100.7",
				}},
			}))
			continue
		}
		records = append(records, analyticsRecord(t, "load_balancing", tenantID, ts, &ipcpb.MistTrigger{
			TriggerType: "LOAD_BALANCING", NodeId: "foghorn-1", TenantId: proto.String(tenantID),
			TriggerPayload: &ipcpb.MistTrigger_LoadBalancingData{LoadBalancingData: &ipcpb.LoadBalancingData{
				StreamId: proto.String(streamID), InternalName: proto.String("live+batch"), SelectedNode: "edge-1",
				Status: "success", ClientIp: fmt.Sprintf("203.0.113.%d", i%250), ClientCountry: "NL",
			}},
		}))
	}
	if err := producer.ProduceSync(context.Background(), records...).FirstErr(); err != nil {
		t.Fatal(err)
	}
	total := int64(len(records))

	h := &ingestHarness{t: t, cluster: cluster, admin: kadm.NewClient(producer), conn: conn, group: "periscope-ingest-batch-test"}
	elapsed := h.consumeUntil(total, 10*time.Minute)

	routingRows := countRows(t, native, `SELECT count() FROM periscope.routing_decisions WHERE tenant_id = ?`, uuid.MustParse(tenantID))
	viewerRows := countRows(t, native, `SELECT count() FROM periscope.viewer_connection_events WHERE tenant_id = ?`, uuid.MustParse(tenantID))
	t.Logf("consumed %d events in %s: routing_decisions rows=%d inserts=%d, viewer_connection_events rows=%d inserts=%d",
		total, elapsed, routingRows, conn.inserts("routing_decisions"), viewerRows, conn.inserts("viewer_connection_events"))
	if routingRows != routingEvents || viewerRows != viewerEvents {
		t.Fatalf("rows routing=%d viewer=%d, want %d/%d", routingRows, viewerRows, routingEvents, viewerEvents)
	}
	// Every record was produced before the consumer started, so the log is
	// drained in a few poll batches; one INSERT per event is the defect.
	const maxInsertsPerTable = 10
	for _, table := range []string{"routing_decisions", "viewer_connection_events"} {
		if got := conn.inserts(table); got > maxInsertsPerTable {
			t.Errorf("%s received %d INSERTs for %d events, want at most %d", table, got, total, maxInsertsPerTable)
		}
	}

	routing := func() uint64 {
		return countRows(t, native, `SELECT count() FROM periscope.routing_decisions WHERE tenant_id = ?`, uuid.MustParse(tenantID))
	}
	viewers := func() uint64 {
		return countRows(t, native, `SELECT count() FROM periscope.viewer_connection_events WHERE tenant_id = ?`, uuid.MustParse(tenantID))
	}

	// A batch redelivered unchanged, as after a crash between insert and
	// commit, adds nothing: event-id tables drop the claimed ids and
	// Replicated block deduplication drops the identical routing block.
	h.rewind(0)
	h.consumeUntil(total, 10*time.Minute)
	if got := viewers(); got != viewerEvents {
		t.Errorf("viewer_connection_events rows after an identical redelivery = %d, want %d", got, viewerEvents)
	}
	if got := routing(); got != routingEvents {
		t.Errorf("routing_decisions rows after an identical redelivery = %d, want %d", got, routingEvents)
	}

	// A redelivery cut differently from the original batches adds no row
	// either: every row carries the ingest_id of its record.
	h.rewind(total / 3)
	h.consumeUntil(total, 10*time.Minute)
	if got := viewers(); got != viewerEvents {
		t.Errorf("viewer_connection_events rows after a suffix redelivery = %d, want %d", got, viewerEvents)
	}
	if got := routing(); got != routingEvents {
		t.Errorf("routing_decisions rows after a suffix redelivery = %d, want %d", got, routingEvents)
	}
}
