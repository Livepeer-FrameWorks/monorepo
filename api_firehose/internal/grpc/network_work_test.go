package grpc

import (
	"context"
	"encoding/json"
	"testing"

	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/google/uuid"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestGatewayWorkPreservesExactNestedAccountingAcrossKafkaEnvelope(t *testing.T) {
	producer := &fakeProducer{}
	server := newTestServer(producer)
	credit := &pb.NetworkAmount{Numerator: "115792089237316195423570985008687907853269984665640564039457584007913129639934000000000000000000", Denominator: "115792089237316195423570985008687907853269984665640564039457584007913129639935", Currency: "ETH", Unit: "wei"}
	id := uuid.NewString()
	_, err := server.SendGatewayTelemetry(context.Background(), &pb.GatewayTelemetryEvent{EventId: id, SchemaVersion: 3, Timestamp: timestamppb.Now(), GatewayId: "test-gateway", GatewayRegion: "eu", ClusterId: "test-cluster", ClusterOwnerTenantId: uuid.NewString(), StreamTenantId: uuid.NewString(), NetworkId: "42161", Environment: "staging", ProducerKind: "gateway", Payload: &pb.GatewayTelemetryEvent_Transcode{Transcode: &pb.OrchestratorTranscodeOutcome{Pixels: 9007199254740993, Accounting: &pb.OrchestratorAttemptAccounting{Version: 1, RequestDispatched: true, ReturnedPixelsKnown: true, TicketCreditSent: credit}}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(producer.publishCalls) != 1 {
		t.Fatal("work observation was not published")
	}
	event := producer.publishCalls[0]
	if event.EventID != id || event.SchemaVersion != 3 {
		t.Fatal("stable observation identity was lost")
	}
	raw, err := json.Marshal(event.Data)
	if err != nil {
		t.Fatal(err)
	}
	var payload pb.OrchestratorTranscodeOutcome
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Pixels != 9007199254740993 || payload.Accounting == nil || payload.Accounting.TicketCreditSent.Numerator != credit.Numerator || payload.Accounting.TicketCreditSent.Denominator != credit.Denominator {
		t.Fatalf("exact work/accounting lost across Kafka JSON: %+v", payload.Accounting)
	}
}
