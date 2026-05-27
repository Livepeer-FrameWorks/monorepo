package bootstrap

import (
	"context"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// TestEncodeProcessPolicy_RejectsNonArrayShape locks the contract that
// process_policy must be a Mist process array (the shape STREAM_PROCESS
// returns to MistServer). An object-shaped policy would serialize fine
// but Mist would silently ignore it — disabling processing entirely.
func TestEncodeProcessPolicy_RejectsNonArrayShape(t *testing.T) {
	cases := []struct {
		name   string
		policy any
		errSub string
	}{
		{
			name: "object_with_categories_rejected",
			policy: map[string]any{
				"video":    []any{},
				"audio":    []any{},
				"metadata": []any{map[string]any{"type": "thumbnail"}},
			},
			errSub: "list of Mist process objects",
		},
		{
			name:   "scalar_rejected",
			policy: "thumbs",
			errSub: "list of Mist process objects",
		},
		{
			name:   "list_of_non_objects_rejected",
			policy: []any{"thumbs"},
			errSub: "each entry must be a Mist process object",
		},
		{
			name:   "list_of_objects_missing_process_rejected",
			policy: []any{map[string]any{"x-LSP-name": "no process key"}},
			errSub: "missing required 'process' key",
		},
		{
			name:   "list_of_objects_blank_process_rejected",
			policy: []any{map[string]any{"process": "   "}},
			errSub: "must be a non-empty string",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := encodeProcessPolicy(tc.policy); err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.errSub)
			} else if !strings.Contains(err.Error(), tc.errSub) {
				t.Fatalf("err = %q, want substring %q", err.Error(), tc.errSub)
			}
		})
	}
}

func TestEncodeProcessPolicy_AcceptsCanonicalMistArray(t *testing.T) {
	policy := []any{
		map[string]any{"process": "Thumbs", "track_select": "video=lowres", "x-LSP-name": "Thumbnail Sprites"},
		map[string]any{"process": "AV", "codec": "AAC", "track_inhibit": "audio=aac", "track_select": "audio=all&video=none&subtitle=none"},
	}
	got, err := encodeProcessPolicy(policy)
	if err != nil {
		t.Fatalf("canonical Mist array must serialize: %v", err)
	}
	if !strings.Contains(got, `"process":"Thumbs"`) || !strings.Contains(got, `"process":"AV"`) {
		t.Fatalf("encoded JSON missing expected entries: %s", got)
	}
}

func TestValidateMistNativeShape(t *testing.T) {
	good := MistNativeStream{
		PlaybackID:        "frameworks-demo",
		OwnerTenant:       TenantRef{Ref: "quartermaster.system_tenant"},
		Title:             "Demo",
		Source:            "ts-exec:ffmpeg -re -stream_loop -1 -i /var/lib/frameworks/demo/clip.mp4 -c copy -f mpegts -",
		SourceKind:        "exec",
		AllowedClusterIDs: []string{"cluster-edge"},
	}
	if err := validateMistNativeShape(good); err != nil {
		t.Fatalf("good shape rejected: %v", err)
	}

	cases := []struct {
		name    string
		mutate  func(*MistNativeStream)
		errLike string
	}{
		{"empty_playback_id", func(m *MistNativeStream) { m.PlaybackID = "" }, "playback_id"},
		{"empty_owner_tenant", func(m *MistNativeStream) { m.OwnerTenant = TenantRef{} }, "owner_tenant"},
		{"empty_title", func(m *MistNativeStream) { m.Title = "" }, "title"},
		{"empty_source", func(m *MistNativeStream) { m.Source = "" }, "source"},
		{"unknown_kind", func(m *MistNativeStream) { m.SourceKind = "ffmpeg" }, "source_kind"},
		{"negative_placement", func(m *MistNativeStream) { m.PlacementCount = -1 }, "placement_count"},
		{"empty_allowed_clusters", func(m *MistNativeStream) { m.AllowedClusterIDs = nil }, "allowed_cluster_ids"},
		{"blank_allowed_cluster_entry", func(m *MistNativeStream) { m.AllowedClusterIDs = []string{"  "} }, "non-empty"},
		{"multiple_source_clusters", func(m *MistNativeStream) {
			m.AllowedClusterIDs = []string{"cluster-edge", "cluster-edge-us"}
		}, "exactly one source cluster"},
		{"multi_cluster_multi_edge", func(m *MistNativeStream) {
			m.PlacementCount = 2
			m.AllowedClusterIDs = []string{"cluster-edge", "cluster-edge-us"}
		}, "exactly one source cluster"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := good
			tc.mutate(&m)
			err := validateMistNativeShape(m)
			if err == nil {
				t.Fatalf("expected error containing %q", tc.errLike)
			}
			if !strings.Contains(err.Error(), tc.errLike) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.errLike)
			}
		})
	}
}

func TestReconcileMistNativeStreams_NoopOnIdempotentRerun(t *testing.T) {
	ctx := context.Background()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	source := "ts-exec:ffmpeg -re -stream_loop -1 -i /var/lib/frameworks/demo/clip.mp4 -c copy -f mpegts -"
	processPolicyJSON := `[{"process":"Thumbs","track_select":"video=lowres","x-LSP-name":"Thumbnail Sprites"}]`

	probeCols := []string{
		"id", "title", "description", "ingest_mode", "always_on", "is_recording_enabled",
		"source_spec", "source_kind", "placement_count",
		"allowed_cluster_ids", "local_asset_paths", "processes_live",
	}
	mock.ExpectQuery(`FROM commodore\.streams s`).
		WithArgs("tenant-uuid", "frameworks-demo").
		WillReturnRows(sqlmock.NewRows(probeCols).AddRow(
			"stream-uuid", "Demo", "Loop", "mist_native", true, false,
			source, "exec", int32(1),
			"{cluster-edge}", "[]", processPolicyJSON,
		))
	// Prune pass lists every mist_native stream for the tenant; the desired
	// row covers stream-uuid so nothing is deleted.
	mock.ExpectQuery(`FROM commodore\.streams s\s+WHERE s\.tenant_id`).
		WithArgs("tenant-uuid").
		WillReturnRows(sqlmock.NewRows([]string{"id", "playback_id"}).
			AddRow("stream-uuid", "frameworks-demo"))

	res, err := ReconcileMistNativeStreams(ctx, db, []MistNativeStream{{
		PlaybackID:        "frameworks-demo",
		OwnerTenant:       TenantRef{Ref: "quartermaster.system_tenant"},
		Title:             "Demo",
		Description:       "Loop",
		Source:            source,
		SourceKind:        "exec",
		AlwaysOn:          true,
		PlacementCount:    1,
		AllowedClusterIDs: []string{"cluster-edge"},
		ProcessPolicy: []any{
			map[string]any{"process": "Thumbs", "track_select": "video=lowres", "x-LSP-name": "Thumbnail Sprites"},
		},
	}}, stubTenantResolver{tenantID: "tenant-uuid"})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(res.Noop) != 1 || len(res.Created) != 0 || len(res.Updated) != 0 || len(res.Deleted) != 0 {
		t.Fatalf("expected single noop, got %+v", res)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestReconcileMistNativeStreams_PrunesAbsentFromDesired asserts the
// declarative-delete contract: a tenant's mist_native streams that are
// present in the DB but missing from the bootstrap manifest get deleted.
func TestReconcileMistNativeStreams_PrunesAbsentFromDesired(t *testing.T) {
	ctx := context.Background()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	source := "ts-exec:cat /dev/null"
	probeCols := []string{
		"id", "title", "description", "ingest_mode", "always_on", "is_recording_enabled",
		"source_spec", "source_kind", "placement_count",
		"allowed_cluster_ids", "local_asset_paths", "processes_live",
	}
	// Desired row probe → noop (existing row matches the desired entry).
	mock.ExpectQuery(`FROM commodore\.streams s`).
		WithArgs("tenant-uuid", "frameworks-demo").
		WillReturnRows(sqlmock.NewRows(probeCols).AddRow(
			"stream-keep", "Demo", "", "mist_native", true, false,
			source, "exec", int32(1), "{cluster-edge}", "[]", "",
		))
	// Prune list returns the kept row + an absent row that should be deleted.
	mock.ExpectQuery(`FROM commodore\.streams s\s+WHERE s\.tenant_id`).
		WithArgs("tenant-uuid").
		WillReturnRows(sqlmock.NewRows([]string{"id", "playback_id"}).
			AddRow("stream-keep", "frameworks-demo").
			AddRow("stream-abandoned", "old-loop-stream"))
	// The absent row is deleted; stream-keep is left alone.
	mock.ExpectExec(`DELETE FROM commodore\.streams WHERE id`).
		WithArgs("stream-abandoned").
		WillReturnResult(sqlmock.NewResult(0, 1))

	res, err := ReconcileMistNativeStreams(ctx, db, []MistNativeStream{{
		PlaybackID:        "frameworks-demo",
		OwnerTenant:       TenantRef{Ref: "quartermaster.system_tenant"},
		Title:             "Demo",
		Source:            source,
		SourceKind:        "exec",
		AlwaysOn:          true,
		AllowedClusterIDs: []string{"cluster-edge"},
	}}, stubTenantResolver{tenantID: "tenant-uuid"})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(res.Deleted) != 1 || res.Deleted[0] != "old-loop-stream" {
		t.Fatalf("expected single delete of old-loop-stream, got %+v", res)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestPruneAllMistNativeStreams_EmptyDesiredDeletesAll asserts that an
// empty bootstrap manifest triggers deletion of every mist_native stream
// under the scoped tenant — the "remove the last entry from bootstrap.yaml"
// case must actually stop the stream.
func TestPruneAllMistNativeStreams_EmptyDesiredDeletesAll(t *testing.T) {
	ctx := context.Background()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(`FROM commodore\.streams s\s+WHERE s\.tenant_id`).
		WithArgs("tenant-uuid").
		WillReturnRows(sqlmock.NewRows([]string{"id", "playback_id"}).
			AddRow("stream-abandoned-1", "frameworks-demo").
			AddRow("stream-abandoned-2", "another-loop"))
	mock.ExpectExec(`DELETE FROM commodore\.streams WHERE id`).
		WithArgs("stream-abandoned-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM commodore\.streams WHERE id`).
		WithArgs("stream-abandoned-2").
		WillReturnResult(sqlmock.NewResult(0, 1))

	res, err := PruneAllMistNativeStreams(ctx, db, stubTenantResolver{tenantID: "tenant-uuid"}, []string{"frameworks"})
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if len(res.Deleted) != 2 {
		t.Fatalf("expected 2 deletes, got %+v", res)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestReconcileMistNativeStreams_RejectsModeMismatch(t *testing.T) {
	ctx := context.Background()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	probeCols := []string{
		"id", "title", "description", "ingest_mode", "always_on", "is_recording_enabled",
		"source_spec", "source_kind", "placement_count",
		"allowed_cluster_ids", "local_asset_paths", "processes_live",
	}
	mock.ExpectQuery(`FROM commodore\.streams s`).
		WithArgs("tenant-uuid", "frameworks-demo").
		WillReturnRows(sqlmock.NewRows(probeCols).AddRow(
			"stream-uuid", "Demo", "", "pull", false, false,
			nil, nil, nil, "{}", "[]", "",
		))

	_, err = ReconcileMistNativeStreams(ctx, db, []MistNativeStream{{
		PlaybackID:        "frameworks-demo",
		OwnerTenant:       TenantRef{Ref: "quartermaster.system_tenant"},
		Title:             "Demo",
		Source:            "ts-exec:cat /dev/null",
		SourceKind:        "exec",
		AllowedClusterIDs: []string{"cluster-edge"},
	}}, stubTenantResolver{tenantID: "tenant-uuid"})
	if err == nil || !strings.Contains(err.Error(), "refusing to convert") {
		t.Fatalf("expected refuse-to-convert error, got: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

type stubTenantResolver struct{ tenantID string }

func (s stubTenantResolver) Resolve(_ context.Context, _ string) (string, error) {
	return s.tenantID, nil
}
