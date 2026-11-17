package triggers

import (
	"context"
	"encoding/json"
	"errors"
	"frameworks/api_balancing/internal/state"
	"testing"
	"time"

	"frameworks/api_balancing/internal/appconfig"
	"frameworks/api_balancing/internal/control"
	"github.com/DATA-DOG/go-sqlmock"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func TestLiveProcessConfigDoesNotGiveRelaysThePublishersJob(t *testing.T) {
	const config = `[{"process":"Livepeer","workload":"live","frameworks_gateway_cluster_ids":["job-cell"],"target_profiles":[{"name":"360p","height":360,"bitrate":900000}]}]`
	for _, tc := range []struct {
		name, requestingNode string
		lookupError          error
	}{
		{name: "assigned publisher", requestingNode: "publisher-node"},
		{name: "relay has no assigned process", requestingNode: "relay-node"},
		{name: "lookup failure is not an unassigned result", requestingNode: "relay-node", lookupError: errors.New("database unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useFoghornConfig(t, &appconfig.Foghorn{BalancerCapabilitySecret: "job-secret"})
			database, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			previous := control.GetDB()
			control.SetDB(database)
			t.Cleanup(func() { control.SetDB(previous); _ = database.Close() })
			query := mock.ExpectQuery("FROM foghorn.ingest_sessions").WithArgs("owner-stream")
			if tc.lookupError != nil {
				query.WillReturnError(tc.lookupError)
			} else {
				query.WillReturnRows(sqlmock.NewRows([]string{"session_id", "tenant_id", "node_id", "cluster_id", "stream_internal_name", "processes_json"}).
					AddRow("admitted-generation", "tenant-1", "publisher-node", "private-cluster", "owner-stream", config))
			}
			p := newTestProcessor(t)
			p.streamCache.Set("process:owner-stream", config, time.Minute)
			trigger := &ipcpb.MistTrigger{NodeId: tc.requestingNode, TriggerPayload: &ipcpb.MistTrigger_StreamProcess{
				StreamProcess: &ipcpb.StreamProcessTrigger{StreamName: "live+owner-stream"},
			}}
			got, abort, callErr := p.handleStreamProcess(trigger)
			if tc.lookupError != nil {
				if callErr == nil || !abort {
					t.Fatalf("unknown assignment was acknowledged: config=%q abort=%v err=%v", got, abort, callErr)
				}
			} else if tc.requestingNode == "relay-node" {
				if got != "" || abort || callErr != nil {
					t.Fatalf("relay received a job or retry instead of a final empty process answer: config=%q abort=%v err=%v", got, abort, callErr)
				}
			} else {
				if callErr != nil || abort {
					t.Fatalf("assigned publisher was refused: abort=%v err=%v", abort, callErr)
				}
				var processes []struct {
					Token string `json:"job_token"`
				}
				if decodeErr := json.Unmarshal([]byte(got), &processes); decodeErr != nil || len(processes) != 1 {
					t.Fatalf("publisher received invalid processing config: %v", decodeErr)
				}
				claims, tokenErr := control.VerifyTranscodeJobToken("job-secret", processes[0].Token, time.Now())
				if tokenErr != nil || claims.NodeID != "publisher-node" || claims.Session != "admitted-generation" {
					t.Fatalf("publisher job binding changed: claims=%+v err=%v", claims, tokenErr)
				}
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBareManagedLivepeerProcessIsSignedOnlyForAssignedSource(t *testing.T) {
	const config = `[{"process":"Livepeer","workload":"live","frameworks_gateway_cluster_ids":["job-cell"],"target_profiles":[{"name":"360p","height":360,"bitrate":900000}]}]`
	for _, requestedNode := range []string{"publisher-node", "relay-node"} {
		t.Run(requestedNode, func(t *testing.T) {
			useFoghornConfig(t, &appconfig.Foghorn{BalancerCapabilitySecret: "job-secret"})
			previousDB := control.GetDB()
			control.SetDB(nil)
			t.Cleanup(func() { control.SetDB(previousDB) })
			sm := state.ResetDefaultManagerForTests()
			t.Cleanup(sm.Shutdown)
			sm.SetNodeConnectionInfo(context.Background(), "publisher-node", "publisher.test", "tenant-1", "private-cluster", nil)
			if err := sm.UpdateStreamFromBuffer("managed-source", "managed-source", "publisher-node", "tenant-1", "FULL", ""); err != nil {
				t.Fatal(err)
			}
			registry := control.NewStreamRegistry(nil, "private-cluster", time.Minute)
			previousRegistry := control.StreamRegistryInstance
			control.SetStreamRegistry(registry)
			t.Cleanup(func() { control.SetStreamRegistry(previousRegistry) })
			registry.UpsertLocalSource(control.StreamEntry{InternalName: "managed-source", TenantID: "tenant-1", IngestMode: control.IngestMistNative})
			p := newTestProcessor(t)
			p.streamCache.Set("process:managed-source", config, time.Minute)
			got, abort, err := p.handleStreamProcess(&ipcpb.MistTrigger{NodeId: requestedNode, TriggerPayload: &ipcpb.MistTrigger_StreamProcess{StreamProcess: &ipcpb.StreamProcessTrigger{StreamName: "managed-source"}}})
			if err != nil || abort {
				t.Fatalf("managed process failed: %v abort=%v", err, abort)
			}
			if requestedNode == "relay-node" {
				if got != "" {
					t.Fatalf("bare managed relay received origin process: %s", got)
				}
				return
			}
			var processes []struct {
				Token string `json:"job_token"`
			}
			if decodeErr := json.Unmarshal([]byte(got), &processes); decodeErr != nil || len(processes) != 1 {
				t.Fatalf("invalid process config: %s", got)
			}
			claims, err := control.VerifyTranscodeJobToken("job-secret", processes[0].Token, time.Now())
			if err != nil || claims.NodeID != "publisher-node" || claims.ManifestID != "managed-source" || claims.Session == "" {
				t.Fatalf("bare managed source received unsigned or unbound Livepeer job: claims=%+v err=%v", claims, err)
			}
		})
	}
}
