package demo

import (
	"reflect"
	"testing"
)

// assertConnectionShape verifies the GraphQL Relay-style envelope invariants that
// every demo connection generator must satisfy, regardless of concrete edge type:
// non-nil PageInfo, TotalCount >= number of edges, and every edge carrying a
// non-empty cursor and a non-nil node. These are the contract the API sandbox
// relies on — a violation renders as a broken/inconsistent paginated list.
func assertConnectionShape(t *testing.T, name string, conn any) {
	t.Helper()
	v := reflect.ValueOf(conn)
	if v.Kind() != reflect.Pointer || v.IsNil() {
		t.Fatalf("%s: connection is nil", name)
	}
	v = v.Elem()

	edges := v.FieldByName("Edges")
	if !edges.IsValid() || edges.Kind() != reflect.Slice {
		t.Fatalf("%s: missing Edges slice", name)
	}
	n := edges.Len()
	if n == 0 {
		t.Errorf("%s: expected at least one demo edge", name)
	}

	if total := v.FieldByName("TotalCount"); total.IsValid() && total.Kind() == reflect.Int {
		if int(total.Int()) < n {
			t.Errorf("%s: TotalCount %d < len(edges) %d", name, total.Int(), n)
		}
	}
	if pi := v.FieldByName("PageInfo"); pi.IsValid() && pi.Kind() == reflect.Pointer && pi.IsNil() {
		t.Errorf("%s: nil PageInfo", name)
	}

	for i := range n {
		edge := edges.Index(i)
		if edge.Kind() == reflect.Pointer {
			if edge.IsNil() {
				t.Errorf("%s: edge %d is nil", name, i)
				continue
			}
			edge = edge.Elem()
		}
		if c := edge.FieldByName("Cursor"); c.IsValid() && c.Kind() == reflect.String && c.String() == "" {
			t.Errorf("%s: edge %d has empty cursor", name, i)
		}
		if node := edge.FieldByName("Node"); node.IsValid() && node.Kind() == reflect.Pointer && node.IsNil() {
			t.Errorf("%s: edge %d has nil node", name, i)
		}
	}
}

// TestDemoConnectionGeneratorsShape runs every demo connection generator through
// the envelope contract. Optional filter args are passed nil to get the unfiltered
// demo payload.
func TestDemoConnectionGeneratorsShape(t *testing.T) {
	cases := []struct {
		name string
		conn any
	}{
		{"StreamAnalyticsSummariesConnection", GenerateStreamAnalyticsSummariesConnection()},
		{"RoutingEventsConnection", GenerateRoutingEventsConnection()},
		{"ConnectionEventsConnection", GenerateConnectionEventsConnection()},
		{"ArtifactEventsConnection", GenerateArtifactEventsConnection()},
		{"NodeMetricsConnection", GenerateNodeMetricsConnection()},
		{"StreamHealthMetricsConnection", GenerateStreamHealthMetricsConnection()},
		{"TrackListEventsConnection", GenerateTrackListEventsConnection()},
		{"StreamEventsConnection", GenerateStreamEventsConnection()},
		{"BufferEventsConnection", GenerateBufferEventsConnection(DemoStreamID)},
		{"ArtifactStatesConnection", GenerateArtifactStatesConnection()},
		{"StreamConnectionHourlyConnection", GenerateStreamConnectionHourlyConnection()},
		{"QualityTierDailyConnection", GenerateQualityTierDailyConnection()},
		{"StorageUsageConnection", GenerateStorageUsageConnection()},
		{"StorageEventsConnection", GenerateStorageEventsConnection(nil)},
		{"ViewerSessionsConnection", GenerateViewerSessionsConnection(nil)},
		{"ServiceInstancesConnection", GenerateServiceInstancesConnection()},
		{"NodesConnection", GenerateNodesConnection()},
		{"ClustersConnection", GenerateClustersConnection()},
		{"ViewerHoursHourlyConnection", GenerateViewerHoursHourlyConnection(nil)},
		{"ViewerGeoHourlyConnection", GenerateViewerGeoHourlyConnection()},
		{"ProcessingUsageConnection", GenerateProcessingUsageConnection(nil, nil)},
		{"RebufferingEventsConnection", GenerateRebufferingEventsConnection(nil)},
		{"TenantAnalyticsDailyConnection", GenerateTenantAnalyticsDailyConnection()},
		{"StreamAnalyticsDailyConnection", GenerateStreamAnalyticsDailyConnection(nil)},
		{"APIUsageConnection", GenerateAPIUsageConnection(nil, nil, nil)},
		{"VodRetentionAssetConnection", GenerateVodRetentionAssetConnection()},
		{"FederationEventsConnection", GenerateFederationEventsConnection()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertConnectionShape(t, tc.name, tc.conn)
		})
	}
}

// assertNonEmptyNoNil verifies a generator slice is non-empty and contains no nil
// pointer elements — the GraphQL non-null contract for list fields.
func assertNonEmptyNoNil(t *testing.T, name string, slice any) {
	t.Helper()
	v := reflect.ValueOf(slice)
	if v.Kind() != reflect.Slice {
		t.Fatalf("%s: not a slice (%s)", name, v.Kind())
	}
	if v.Len() == 0 {
		t.Errorf("%s: expected at least one demo element", name)
	}
	for i := range v.Len() {
		if e := v.Index(i); e.Kind() == reflect.Pointer && e.IsNil() {
			t.Errorf("%s: element %d is nil", name, i)
		}
	}
}

// TestDemoListGeneratorsNonEmpty runs the demo list generators through the
// non-empty/no-nil contract.
func TestDemoListGeneratorsNonEmpty(t *testing.T) {
	cases := []struct {
		name  string
		slice any
	}{
		{"Streams", GenerateStreams()},
		{"ViewerCountTimeSeries", GenerateViewerCountTimeSeries()},
		{"BillingTiers", GenerateBillingTiers()},
		{"Invoices", GenerateInvoices()},
		{"UsageRecords", GenerateUsageRecords()},
		{"DeveloperTokens", GenerateDeveloperTokens()},
		{"StreamEvents", GenerateStreamEvents()},
		{"TrackListEvents", GenerateTrackListEvents()},
		{"StreamHealthMetrics", GenerateStreamHealthMetrics()},
		{"ViewerTimeSeries", GenerateViewerTimeSeries()},
		{"ViewerGeographics", GenerateViewerGeographics()},
		{"RoutingEvents", GenerateRoutingEvents()},
		{"NodeMetricsAggregated", GenerateNodeMetricsAggregated()},
		{"ServiceInstances", GenerateServiceInstances()},
		{"BootstrapTokens", GenerateBootstrapTokens()},
		{"InfrastructureNodes", GenerateInfrastructureNodes()},
		{"InfrastructureClusters", GenerateInfrastructureClusters()},
		{"Clips", GenerateClips()},
		{"MarketplaceClusters", GenerateMarketplaceClusters()},
		{"MySubscriptions", GenerateMySubscriptions()},
		{"ClusterInvites", GenerateClusterInvites()},
		{"MyClusterInvites", GenerateMyClusterInvites()},
		{"PendingSubscriptions", GeneratePendingSubscriptions()},
		{"VodAssets", GenerateVodAssets()},
		{"ClusterBootOps", GenerateClusterBootOps()},
		{"ClusterQoeOps", GenerateClusterQoeOps()},
		{"PlayerBootTimeSeries", GeneratePlayerBootTimeSeries()},
		{"SessionQoeTimeSeries", GenerateSessionQoeTimeSeries()},
		{"ClusterTrafficMatrix", GenerateClusterTrafficMatrix()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertNonEmptyNoNil(t, tc.name, tc.slice)
		})
	}
}

// TestDemoStreamScopedGeneratorsUseDemoID locks the demo identity invariant:
// stream-scoped demo payloads reference the canonical DemoStreamID, so the fixture
// global IDs used across the playground resolve to real demo rows.
func TestDemoStreamScopedGeneratorsUseDemoID(t *testing.T) {
	summary := GenerateStreamAnalyticsSummary(DemoStreamID)
	if summary == nil {
		t.Fatal("nil stream analytics summary")
	}
	if summary.GetStreamId() != DemoStreamID {
		t.Errorf("summary stream id = %q, want %q", summary.GetStreamId(), DemoStreamID)
	}

	keys := GenerateStreamKeys(DemoStreamID)
	if len(keys) == 0 {
		t.Fatal("expected demo stream keys")
	}
	for i, k := range keys {
		if k == nil {
			t.Errorf("stream key %d is nil", i)
		}
	}

	for i, m := range GenerateStreamHealthMetrics() {
		if m.GetStreamId() != DemoStreamID {
			t.Errorf("health metric %d stream id = %q, want %q", i, m.GetStreamId(), DemoStreamID)
		}
	}
}
