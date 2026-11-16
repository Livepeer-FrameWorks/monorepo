package kafkasource

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	internalv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/sessionrevocations"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/topology"
	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// No Signalman is started. Both replica caches must learn a Kafka fact, and a
// replacement process must reconstruct the fact that preceded its live boundary.
func TestKafkaProjectionEveryReplicaMirrorAndRestartWithoutSignalman(t *testing.T) {
	cluster, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.SeedTopics(3, topology.TopicDomainEvents, "eu.domain.events"))
	if err != nil {
		t.Fatal(err)
	}
	defer cluster.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	producer, err := kgo.NewClient(kgo.SeedBrokers(cluster.ListenAddrs()...))
	if err != nil {
		t.Fatal(err)
	}
	defer producer.Close()
	const tenant = "f5d2b12e-d54a-4a1b-81b1-947b5a622a70"
	const user = "9b94d6cd-f57d-4c69-9890-72a14807631b"
	const sid = "d0d6be61-8a02-4bc2-a909-289b885edec8"
	token := func(id string) string {
		v, e := auth.GenerateNamedSessionJWT(user, tenant, "user@example.test", "member", nil, id, time.Now(), time.Now().Add(auth.SessionTokenTTL), []byte("local-session-revocation-authority"))
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	revokedToken, otherToken := token(sid), token("other")
	var snapshot atomic.Pointer[commodorepb.ListRecentSessionRevocationsResponse]
	snapshot.Store(&commodorepb.ListRecentSessionRevocationsResponse{AuthSessionsIncluded: true})
	newReplica := func() *sessionrevocations.Feed {
		t.Helper()
		feed, closeFeed, e := Connect(ctx, sessionrevocations.Config{Secret: []byte("local-session-revocation-authority"), List: func(context.Context, time.Time) (*commodorepb.ListRecentSessionRevocationsResponse, error) {
			return snapshot.Load(), nil
		}}, Transport{Brokers: cluster.ListenAddrs(), MirrorRegionPrefixes: []string{"eu"}, ClientID: "auth-replica"})
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(closeFeed)
		waitFor := time.NewTicker(5 * time.Millisecond)
		defer waitFor.Stop()
		for status.Code(feed.CheckSession(ctx, otherToken)) == codes.Unavailable {
			select {
			case <-waitFor.C:
			case <-ctx.Done():
				t.Fatal("projection did not hydrate", ctx.Err())
			}
		}
		return feed
	}
	a, b := newReplica(), newReplica()
	payload := &internalv1.AuthSessionRevoked{UserId: user, SessionId: sid, RevokedAt: timestamppb.Now()}
	event, err := events.New("commodore", tenant, sid, payload)
	if err != nil {
		t.Fatal(err)
	}
	key, headers, value, err := events.EncodeRecord(event.Envelope())
	if err != nil {
		t.Fatal(err)
	}
	record := &kgo.Record{Topic: "eu.domain.events", Key: key, Value: value}
	for _, header := range headers {
		record.Headers = append(record.Headers, kgo.RecordHeader{Key: header.Key, Value: header.Value})
	}
	if err := producer.ProduceSync(ctx, record).FirstErr(); err != nil {
		t.Fatal(err)
	}
	for i, feed := range []*sessionrevocations.Feed{a, b} {
		ticker := time.NewTicker(5 * time.Millisecond)
		for status.Code(feed.CheckSession(ctx, revokedToken)) != codes.Unauthenticated {
			select {
			case <-ticker.C:
			case <-ctx.Done():
				ticker.Stop()
				t.Fatalf("replica %d did not receive revocation: %v", i, ctx.Err())
			}
		}
		ticker.Stop()
		if err := feed.CheckSession(ctx, otherToken); err != nil {
			t.Fatalf("replica %d rejected other device: %v", i, err)
		}
	}

	snapshot.Store(&commodorepb.ListRecentSessionRevocationsResponse{AuthSessionsIncluded: true, Revocations: []*commodorepb.SessionRevocation{{TenantId: tenant, UserId: user, SessionId: sid, RevokedAt: payload.RevokedAt}}})
	replacement := newReplica()
	if code := status.Code(replacement.CheckSession(ctx, revokedToken)); code != codes.Unauthenticated {
		t.Fatalf("restart revived revoked token: %s", code)
	}
	if err := replacement.CheckSession(ctx, otherToken); err != nil {
		t.Fatal(err)
	}
}
