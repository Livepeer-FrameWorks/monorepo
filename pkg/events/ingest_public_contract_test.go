package events

import (
	"google.golang.org/protobuf/reflect/protoreflect"
	"testing"
)

func TestStreamIntegrationPublicEventContract(t *testing.T) {
	for _, eventType := range []string{"stream.connected", "stream.live", "stream.idle", "stream.ended", "stream.ingest_disconnected", "recording.chapter_ready"} {
		t.Run(eventType, func(t *testing.T) {
			spec, ok := Lookup(eventType)
			if !ok || !spec.Public() {
				t.Fatalf("%s is missing from the public event registry", eventType)
			}
			fields := spec.NewMessage().ProtoReflect().Descriptor().Fields()
			if eventType != "recording.chapter_ready" && fields.ByName(protoreflect.Name("session_id")) == nil {
				t.Fatal("lifecycle event cannot identify its ingest session")
			}
			if eventType == "recording.chapter_ready" {
				for name, number := range map[protoreflect.Name]protoreflect.FieldNumber{"artifact": 1, "chapter_id": 2, "start_ms": 3, "end_ms": 4, "size_bytes": 5} {
					field := fields.ByName(name)
					if field == nil || field.Number() != number {
						t.Fatalf("chapter field %s must retain wire number %d", name, number)
					}
				}
			}
		})
	}
}
