package kafkasource

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	internalv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestRevocationDecoderRejectsUntrustedOrAmbiguousFacts(t *testing.T) {
	for _, tc := range []struct {
		name      string
		change    func(*kafka.Message)
		wantError bool
	}{
		{"authoritative", func(*kafka.Message) {}, false},
		{"other producer", func(m *kafka.Message) { m.Headers[events.HeaderSource] = "purser" }, true},
		{"public header", func(m *kafka.Message) { m.Headers[events.HeaderVisibility] = events.VisibilityPublic }, true},
		{"missing tenant", func(m *kafka.Message) { delete(m.Headers, events.HeaderTenantID) }, true},
		{"different aggregate", func(m *kafka.Message) {
			m.Headers[events.HeaderSubject] = "auth_sessions/other"
			m.Key = []byte("auth_sessions/other")
		}, true},
		{"malformed", func(m *kafka.Message) { m.Value = []byte{0xff} }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event, err := events.New("commodore", "f5d2b12e-d54a-4a1b-81b1-947b5a622a70", "d0d6be61-8a02-4bc2-a909-289b885edec8", &internalv1.AuthSessionRevoked{UserId: "user", SessionId: "d0d6be61-8a02-4bc2-a909-289b885edec8", RevokedAt: timestamppb.Now()})
			if err != nil {
				t.Fatal(err)
			}
			key, headers, value, err := events.EncodeRecord(event.Envelope())
			if err != nil {
				t.Fatal(err)
			}
			message := kafka.Message{Key: key, Value: value, Headers: map[string]string{}}
			for _, header := range headers {
				message.Headers[header.Key] = string(header.Value)
			}
			tc.change(&message)
			rev, err := decodeRecord(message)
			if (err != nil) != tc.wantError {
				t.Fatalf("unexpected decoding result: rev=%v err=%v", rev, err)
			}
			if tc.wantError && rev != nil {
				t.Fatal("refused fact changed the projection")
			}
		})
	}
}
