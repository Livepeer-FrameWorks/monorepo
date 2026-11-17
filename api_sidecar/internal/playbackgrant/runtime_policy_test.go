package playbackgrant

import (
	"context"
	"testing"

	"frameworks/api_sidecar/internal/playbackgrant/playbackgranttest"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func TestPolicyChangeCoversDrainingAndCurrentPhysicalRuntimes(t *testing.T) {
	const old = "live+stream_fw1p_84ed1d3113d2468db7b5ef132d4e3ee9"
	const next = "live+stream_fw1p_faed1d3113d2468db7b5ef132d4e3ee9"
	for _, revoked := range []bool{false, true} {
		t.Run(map[bool]string{false: "connected policy", true: "revocation"}[revoked], func(t *testing.T) {
			store, _ := newTestStore(t, &recordingMist{}, nil)
			store.ApplyGrant(playbackgranttest.Grant(old, ipcpb.PlaybackGrantPolicyKind_PLAYBACK_GRANT_POLICY_KIND_PUBLIC, nil, "pb"))
			store.ApplyGrant(playbackgranttest.Grant(next, ipcpb.PlaybackGrantPolicyKind_PLAYBACK_GRANT_POLICY_KIND_PUBLIC, nil, "pb"))
			store.AdmitSession(viewer(old, "192.0.2.1", "token", "old-session"), false)
			if revoked {
				store.ApplyGrant(&ipcpb.PlaybackGrant{InternalName: next, Revoked: true, RevokedReason: "stream deleted"})
				if _, refused := store.Refused("old-session"); !refused {
					t.Fatal("revocation left the draining runtime's viewer admitted")
				}
			} else {
				store.ApplyGrant(playbackgranttest.Grant(next, ipcpb.PlaybackGrantPolicyKind_PLAYBACK_GRANT_POLICY_KIND_CONNECTED, nil, "pb"))
				if store.names["pb"] != next {
					t.Fatal("policy propagation rerouted playback into the old runtime")
				}
			}
			if decision := store.DecideNewSession(viewer(old, "192.0.2.1", "new-token", "new-session")); decision.Allow {
				t.Fatalf("old physical runtime retained a superseded playback policy: %+v", decision)
			}
		})
	}
}

func TestLogicalStopCoversAllRuntimeGenerationsButPhysicalDrainIsExact(t *testing.T) {
	const old = "live+stream_fw1p_84ed1d3113d2468db7b5ef132d4e3ee9"
	const next = "live+stream_fw1p_faed1d3113d2468db7b5ef132d4e3ee9"
	for _, logical := range []bool{false, true} {
		store, _ := newTestStore(t, &recordingMist{}, nil)
		for _, runtime := range []string{old, next} {
			store.ApplyGrant(playbackgranttest.Grant(runtime, ipcpb.PlaybackGrantPolicyKind_PLAYBACK_GRANT_POLICY_KIND_PUBLIC, nil))
			store.AdmitSession(viewer(runtime, "192.0.2.1", runtime, runtime), false)
		}
		target := old
		if logical {
			target = "stream"
		}
		store.StopStreams([]string{target}, "test")
		if _, refused := store.Refused(old); !refused {
			t.Fatal("old runtime was not stopped")
		}
		_, refused := store.Refused(next)
		if refused != logical {
			t.Fatalf("replacement stopped=%v, logical stop=%v", refused, logical)
		}
	}
}

func TestGrantFetchStartedBeforePolicyChangeCannotRestoreOldPermissions(t *testing.T) {
	const old = "live+stream_fw1p_84ed1d3113d2468db7b5ef132d4e3ee9"
	const next = "live+stream_fw1p_faed1d3113d2468db7b5ef132d4e3ee9"
	for _, action := range []string{"policy change", "revocation", "suspension"} {
		t.Run(action, func(t *testing.T) {
			store, _ := newTestStore(t, &recordingMist{}, nil)
			store.ApplyGrant(playbackgranttest.Grant(old, ipcpb.PlaybackGrantPolicyKind_PLAYBACK_GRANT_POLICY_KIND_PUBLIC, nil, "pb"))
			store.ApplyGrant(playbackgranttest.Grant(next, ipcpb.PlaybackGrantPolicyKind_PLAYBACK_GRANT_POLICY_KIND_PUBLIC, nil, "pb"))
			started, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
			store.fetch = func(context.Context, string) (*ipcpb.PlaybackGrant, error) {
				close(started)
				<-release
				return playbackgranttest.Grant(old, ipcpb.PlaybackGrantPolicyKind_PLAYBACK_GRANT_POLICY_KIND_PUBLIC, nil, "pb"), nil
			}
			go func() { done <- store.fetchGrant(old, true) }()
			<-started
			switch action {
			case "suspension":
				store.StopStreams([]string{"stream"}, "tenant suspended")
			case "revocation":
				store.ApplyGrant(&ipcpb.PlaybackGrant{InternalName: next, Revoked: true, RevokedReason: "deleted"})
			default:
				store.ApplyGrant(playbackgranttest.Grant(next, ipcpb.PlaybackGrantPolicyKind_PLAYBACK_GRANT_POLICY_KIND_CONNECTED, nil, "pb"))
			}
			close(release)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if decision := store.DecideNewSession(viewer(old, "192.0.2.1", "late", "late-session")); decision.Allow {
				t.Fatalf("a fetch started before the policy change restored public access: %+v", decision)
			}
			if action == "policy change" && store.names["pb"] != next {
				t.Fatal("a stale grant fetch moved playback back to the draining publisher")
			}
		})
	}
}
