package federation

import (
	"context"
	"testing"
	"time"

	pb "frameworks/pkg/proto"

	"google.golang.org/grpc"
)

type noopClipCreator struct{}

func (noopClipCreator) CreateClip(context.Context, *pb.CreateClipRequest) (*pb.CreateClipResponse, error) {
	return &pb.CreateClipResponse{}, nil
}

type noopDVRCreator struct{}

func (noopDVRCreator) StartDVR(context.Context, *pb.StartDVRRequest) (*pb.StartDVRResponse, error) {
	return &pb.StartDVRResponse{}, nil
}

func TestPeerChannel_StoresIncomingPayloadsInCache(t *testing.T) {
	cache, _ := setupTestCache(t)
	srv := NewFederationServer(FederationServerConfig{
		Logger:    testLogger(),
		ClusterID: "cluster-a",
		Cache:     cache,
	})

	peerCluster := "cluster-b"
	stream := &testPeerChannelServerStream{
		ctx: serviceAuthContext(),
		messages: []*pb.PeerMessage{
			{
				ClusterId: peerCluster,
				Payload: &pb.PeerMessage_EdgeTelemetry{EdgeTelemetry: &pb.EdgeTelemetry{
					StreamName:  "stream-1",
					NodeId:      "node-1",
					BaseUrl:     "edge-1.example.com",
					BwAvailable: 1000,
					ViewerCount: 7,
					CpuPercent:  22.0,
					RamUsed:     128,
					RamMax:      1024,
					GeoLat:      1.1,
					GeoLon:      2.2,
				}},
			},
			{
				ClusterId: peerCluster,
				Payload: &pb.PeerMessage_ReplicationEvent{ReplicationEvent: &pb.ReplicationEvent{
					StreamName: "stream-rep",
					NodeId:     "node-rep",
					BaseUrl:    "edge-2.example.com",
					DtscUrl:    "dtsc://edge-2.example.com/stream-rep",
					Available:  true,
				}},
			},
			{
				ClusterId: peerCluster,
				Payload: &pb.PeerMessage_ClusterSummary{ClusterSummary: &pb.ClusterEdgeSummary{
					Edges: []*pb.EdgeSnapshot{{
						NodeId:         "node-summary",
						BaseUrl:        "edge-summary.example.com",
						GeoLat:         3.3,
						GeoLon:         4.4,
						BwAvailableAvg: 2000,
						CpuPercentAvg:  20.5,
						RamUsed:        256,
						RamMax:         2048,
						TotalViewers:   10,
						Roles:          []string{"edge"},
					}},
					Timestamp: time.Now().Unix(),
				}},
			},
			{
				ClusterId: peerCluster,
				Payload: &pb.PeerMessage_StreamLifecycle{StreamLifecycle: &pb.StreamLifecycleEvent{
					InternalName: "stream-live",
					TenantId:     "tenant-a",
					ClusterId:    peerCluster,
					IsLive:       true,
				}},
			},
			{
				ClusterId: peerCluster,
				Payload: &pb.PeerMessage_StreamLifecycle{StreamLifecycle: &pb.StreamLifecycleEvent{
					InternalName: "stream-live",
					TenantId:     "tenant-a",
					ClusterId:    peerCluster,
					IsLive:       false,
				}},
			},
			{
				ClusterId: peerCluster,
				Payload: &pb.PeerMessage_StreamAd{StreamAd: &pb.StreamAdvertisement{
					InternalName: "stream-ad",
					TenantId:     "tenant-a",
					PlaybackId:   "play-1",
					IsLive:       true,
					Edges: []*pb.PeerStreamEdge{{
						NodeId:      "node-ad",
						BaseUrl:     "edge-ad.example.com",
						DtscUrl:     "dtsc://edge-ad.example.com/stream-ad",
						IsOrigin:    true,
						BwAvailable: 1234,
						CpuPercent:  15.5,
						ViewerCount: 3,
						GeoLat:      5.5,
						GeoLon:      6.6,
						BufferState: "FULL",
					}},
					Timestamp: time.Now().Unix(),
				}},
			},
			{
				ClusterId: peerCluster,
				Payload: &pb.PeerMessage_ArtifactAd{ArtifactAd: &pb.ArtifactAdvertisement{
					Artifacts: []*pb.ArtifactLocation{{
						ArtifactHash: "artifact-1",
						ArtifactType: "clip",
						NodeId:       "node-art",
						BaseUrl:      "edge-art.example.com",
						SizeBytes:    2048,
						AccessCount:  2,
						LastAccessed: time.Now().Unix(),
						GeoLat:       7.7,
						GeoLon:       8.8,
					}},
					Timestamp: time.Now().Unix(),
				}},
			},
			{
				ClusterId: peerCluster,
				Payload: &pb.PeerMessage_PeerHeartbeat{PeerHeartbeat: &pb.PeerHeartbeat{
					ProtocolVersion:  1,
					StreamCount:      5,
					TotalBwAvailable: 9999,
					EdgeCount:        3,
					UptimeSeconds:    123,
					Capabilities:     []string{"stream_ad"},
				}},
			},
			{
				ClusterId: peerCluster,
				Payload: &pb.PeerMessage_CapacitySummary{CapacitySummary: &pb.CapacitySummary{
					TotalBandwidth:     1000,
					AvailableBandwidth: 900,
					TotalEdges:         2,
					AvailableEdges:     1,
					TotalStorage:       10000,
					AvailableStorage:   5000,
					Timestamp:          time.Now().Unix(),
				}},
			},
		},
	}

	if err := srv.PeerChannel(stream); err != nil {
		t.Fatalf("PeerChannel returned error: %v", err)
	}

	ctx := context.Background()

	edges, err := cache.GetRemoteEdges(ctx, peerCluster)
	if err != nil || len(edges) == 0 {
		t.Fatalf("expected remote edge telemetry cached, edges=%v err=%v", edges, err)
	}

	reps, err := cache.GetRemoteReplications(ctx, "stream-rep")
	if err != nil || len(reps) == 0 {
		t.Fatalf("expected replication event cached, reps=%v err=%v", reps, err)
	}

	summary, err := cache.GetEdgeSummary(ctx, peerCluster)
	if err != nil || summary == nil || len(summary.Edges) != 1 {
		t.Fatalf("expected edge summary cached, summary=%v err=%v", summary, err)
	}

	live, err := cache.GetRemoteLiveStream(ctx, "stream-live")
	if err != nil {
		t.Fatalf("GetRemoteLiveStream: %v", err)
	}
	if live != nil {
		t.Fatalf("expected stream-live to be deleted on offline event, got %+v", live)
	}

	ad, err := cache.GetStreamAd(ctx, peerCluster, "stream-ad")
	if err != nil || ad == nil {
		t.Fatalf("expected stream ad cached, ad=%v err=%v", ad, err)
	}
	playbackIdx, err := cache.GetPlaybackIndex(ctx, "play-1")
	if err != nil || playbackIdx != "stream-ad" {
		t.Fatalf("expected playback index play-1->stream-ad, got %q err=%v", playbackIdx, err)
	}

	artifacts, err := cache.GetRemoteArtifacts(ctx, "artifact-1")
	if err != nil || len(artifacts) == 0 {
		t.Fatalf("expected artifact ad cached, artifacts=%v err=%v", artifacts, err)
	}

	hb, err := cache.GetPeerHeartbeat(ctx, peerCluster)
	if err != nil || hb == nil {
		t.Fatalf("expected peer heartbeat cached, hb=%v err=%v", hb, err)
	}
	if hb.StreamCount != 5 || hb.EdgeCount != 3 {
		t.Fatalf("unexpected heartbeat payload: %+v", hb)
	}
}

func TestPeerChannel_HandlerNilPayloadsNoop(t *testing.T) {
	cache, _ := setupTestCache(t)
	srv := NewFederationServer(FederationServerConfig{
		Logger:    testLogger(),
		ClusterID: "cluster-a",
		Cache:     cache,
	})

	ctx := context.Background()
	srv.handleArtifactAdvertisement(ctx, "cluster-b", nil)
	srv.handleStreamAdvertisement(ctx, "cluster-b", nil)
	srv.handlePeerHeartbeat(ctx, "cluster-b", nil)
}

func TestFederationServer_SettersAndRegisterServices(t *testing.T) {
	srv := NewFederationServer(FederationServerConfig{
		Logger:    testLogger(),
		ClusterID: "cluster-a",
	})
	srv.SetClipCreator(noopClipCreator{})
	srv.SetDVRCreator(noopDVRCreator{})
	if srv.clipCreator == nil || srv.dvrCreator == nil {
		t.Fatal("expected clip and dvr creators to be set")
	}

	grpcServer := grpc.NewServer()
	srv.RegisterServices(grpcServer)
}
