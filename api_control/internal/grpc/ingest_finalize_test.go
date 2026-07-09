package grpc

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"google.golang.org/protobuf/proto"
)

// finalizeIngestResponse is the trust boundary of ResolveIngestEndpoint: it must
// enrich only via tenant-scoped queries and, for any non-owner (anonymous or a
// tenant mismatch), strip the echoed stream key and owning tenant. These tests
// pin that boundary without a live Foghorn (the pure-helper test alone can't
// prove the RPC invokes it).
func TestFinalizeIngestResponse(t *testing.T) {
	const enrichQuery = "SELECT title, description FROM commodore.streams WHERE id = \\$1 AND tenant_id = \\$2"

	t.Run("unauthenticated strips key and tenant but still enriches tenant-scoped", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()

		// Foghorn resolved the owning tenant from the key; enrichment must scope to it.
		mock.ExpectQuery(enrichQuery).
			WithArgs("stream-1", "tenant-foghorn").
			WillReturnRows(sqlmock.NewRows([]string{"title", "description"}).AddRow("My Stream", "desc"))

		resp := &sharedpb.IngestEndpointResponse{
			Metadata: &sharedpb.IngestMetadata{
				StreamId:  "stream-1",
				StreamKey: "sk_secret",
				TenantId:  "tenant-foghorn",
			},
		}

		s.finalizeIngestResponse(context.Background(), "", resp)

		if resp.Metadata.StreamKey != "" {
			t.Errorf("stream key not stripped for unauthenticated caller: %q", resp.Metadata.StreamKey)
		}
		if resp.Metadata.TenantId != "" {
			t.Errorf("tenant id not stripped for unauthenticated caller: %q", resp.Metadata.TenantId)
		}
		if resp.Metadata.GetTitle() != "My Stream" {
			t.Errorf("expected enrichment to run, got title %q", resp.Metadata.GetTitle())
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("sqlmock expectations: %v", err)
		}
	})

	t.Run("authenticated owner keeps full metadata", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()

		mock.ExpectQuery(enrichQuery).
			WithArgs("stream-1", "tenant-abc").
			WillReturnRows(sqlmock.NewRows([]string{"title", "description"}).AddRow("My Stream", ""))

		resp := &sharedpb.IngestEndpointResponse{
			Metadata: &sharedpb.IngestMetadata{
				StreamId:  "stream-1",
				StreamKey: "sk_secret",
				TenantId:  "tenant-abc",
			},
		}

		s.finalizeIngestResponse(context.Background(), "tenant-abc", resp)

		if resp.Metadata.StreamKey != "sk_secret" {
			t.Errorf("authenticated caller lost stream key: %q", resp.Metadata.StreamKey)
		}
		if resp.Metadata.TenantId != "tenant-abc" {
			t.Errorf("authenticated caller lost tenant id: %q", resp.Metadata.TenantId)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("sqlmock expectations: %v", err)
		}
	})

	t.Run("authenticated non-owner is stripped and cannot read cross-tenant", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()

		// resolveIngestEndpoint takes an optional JWT, so a signed-in caller from
		// tenant-b can reach this with tenant-a's key. Enrichment scopes to the
		// CALLER (tenant-b), so they read nothing; and being a non-owner, the key
		// and tenant are stripped.
		mock.ExpectQuery(enrichQuery).
			WithArgs("stream-1", "tenant-b").
			WillReturnRows(sqlmock.NewRows([]string{"title", "description"}))

		resp := &sharedpb.IngestEndpointResponse{
			Metadata: &sharedpb.IngestMetadata{
				StreamId:  "stream-1",
				StreamKey: "sk_secret",
				TenantId:  "tenant-a",
			},
		}

		s.finalizeIngestResponse(context.Background(), "tenant-b", resp)

		if resp.Metadata.StreamKey != "" {
			t.Errorf("stream key leaked to authenticated non-owner: %q", resp.Metadata.StreamKey)
		}
		if resp.Metadata.TenantId != "" {
			t.Errorf("tenant id leaked to authenticated non-owner: %q", resp.Metadata.TenantId)
		}
		if resp.Metadata.GetTitle() != "" {
			t.Errorf("cross-tenant title leaked to non-owner: %q", resp.Metadata.GetTitle())
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("sqlmock expectations: %v", err)
		}
	})

	t.Run("upstream-populated title and description are not trusted for non-owner", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()

		// Even if a future Foghorn echoes title/description on the response, a
		// non-owner must not keep them: Commodore re-derives under the caller's own
		// tenant scope (which returns nothing here) and clears the upstream values.
		mock.ExpectQuery(enrichQuery).
			WithArgs("stream-1", "tenant-b").
			WillReturnRows(sqlmock.NewRows([]string{"title", "description"}))

		resp := &sharedpb.IngestEndpointResponse{
			Metadata: &sharedpb.IngestMetadata{
				StreamId:    "stream-1",
				StreamKey:   "sk_secret",
				TenantId:    "tenant-a",
				Title:       proto.String("Tenant A private title"),
				Description: proto.String("Tenant A private description"),
			},
		}

		s.finalizeIngestResponse(context.Background(), "tenant-b", resp)

		if resp.Metadata.GetTitle() != "" {
			t.Errorf("upstream title leaked to non-owner: %q", resp.Metadata.GetTitle())
		}
		if resp.Metadata.GetDescription() != "" {
			t.Errorf("upstream description leaked to non-owner: %q", resp.Metadata.GetDescription())
		}
		if resp.Metadata.StreamKey != "" || resp.Metadata.TenantId != "" {
			t.Errorf("key/tenant leaked to non-owner: key=%q tenant=%q", resp.Metadata.StreamKey, resp.Metadata.TenantId)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("sqlmock expectations: %v", err)
		}
	})

	t.Run("no establishable tenant skips enrichment entirely", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()

		// No ExpectQuery: any DB read here would be an unscoped cross-tenant query
		// and sqlmock would fail it. Metadata has no tenant and the caller is anonymous.
		resp := &sharedpb.IngestEndpointResponse{
			Metadata: &sharedpb.IngestMetadata{
				StreamId:  "stream-1",
				StreamKey: "sk_secret",
			},
		}

		s.finalizeIngestResponse(context.Background(), "", resp)

		if resp.Metadata.GetTitle() != "" {
			t.Errorf("expected no enrichment without a tenant, got title %q", resp.Metadata.GetTitle())
		}
		if resp.Metadata.StreamKey != "" {
			t.Errorf("stream key not stripped: %q", resp.Metadata.StreamKey)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("sqlmock expectations: %v", err)
		}
	})

	t.Run("nil response is a no-op", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		s.finalizeIngestResponse(context.Background(), "", nil)
	})
}
