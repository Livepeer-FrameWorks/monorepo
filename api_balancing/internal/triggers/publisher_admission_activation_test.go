package triggers

import (
	"testing"

	"frameworks/api_balancing/internal/control"
	localauthority "frameworks/api_balancing/internal/mediaauthority"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
)

func TestPublisherAdmissionWaitsForReadySchema4Pair(t *testing.T) {
	repository := control.NewPublisherAdmissionRepository(nil)
	processor := &Processor{publisherAdmissions: repository}
	for _, test := range []struct {
		name                       string
		tenantSchema, objectSchema uint32
		tenantReady, objectReady   bool
		config                     uint32
		want                       bool
	}{
		{"legacy", 1, 1, true, true, 0, false},
		{"mixed cell", 3, 3, true, true, 1, false},
		{"unready tenant", 4, 4, false, true, 1, false},
		{"unready object", 4, 4, true, false, 1, false},
		{"incomplete configuration", 4, 4, true, true, 0, false},
		{"complete cutover", 4, 4, true, true, 1, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			local := localIngestAuthority{
				tenant: localauthority.TenantSnapshot{Authority: &mediapb.TenantAuthority{SchemaVersion: test.tenantSchema}, IngestReady: test.tenantReady},
				object: localauthority.MediaObjectSnapshot{Authority: &mediapb.MediaObjectAuthority{SchemaVersion: test.objectSchema, Object: &mediapb.MediaObjectAuthority_LiveStream{LiveStream: &mediapb.LiveStreamAuthority{PublisherConfigurationVersion: test.config}}}, IngestReady: test.objectReady},
			}
			if got := processor.publisherRepositoryFor(t.Context(), local); (got != nil) != test.want {
				t.Fatalf("atomic publisher coordinator enabled=%v, want %v", got != nil, test.want)
			}
		})
	}
}

func TestPublisherAdmissionWaitsForEdgeSettlementCapability(t *testing.T) {
	processor := &Processor{publisherAdmissions: control.NewPublisherAdmissionRepository(nil)}
	local := localIngestAuthority{
		tenant: localauthority.TenantSnapshot{Authority: &mediapb.TenantAuthority{SchemaVersion: 4}, IngestReady: true},
		object: localauthority.MediaObjectSnapshot{Authority: &mediapb.MediaObjectAuthority{SchemaVersion: 4, Object: &mediapb.MediaObjectAuthority_LiveStream{LiveStream: &mediapb.LiveStreamAuthority{PublisherConfigurationVersion: 1}}}, IngestReady: true},
	}
	ctx, cancel := control.LegacyPublisherContext(t.Context(), nil)
	defer cancel()
	if processor.publisherRepositoryFor(ctx, local) != nil {
		t.Fatal("older edge activated atomic transitions without durable denial settlement")
	}
	if processor.publisherRepositoryFor(t.Context(), local) == nil {
		t.Fatal("unary publisher lost the schema-4 atomic path")
	}
}
