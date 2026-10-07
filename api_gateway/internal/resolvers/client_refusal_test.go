package resolvers

import (
	"context"
	"testing"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/clients/clientstest"
	gwerrors "frameworks/api_gateway/internal/errors"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"

	"github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func errOnly[T any](_ T, err error) error { return err }

// A refusal a resolver makes itself (authorization, missing identity, invalid
// input) is a client fault: Bridge's error presenter logs it at info, keeps
// its message, and gives it a public code. Error-level logs stay a signal of
// platform faults.
func TestResolverRefusalsAreClientFaults(t *testing.T) {
	qm := &clientstest.FakeQuartermaster{
		ListClustersByOwnerFn: func(context.Context, string, *commonpb.CursorPaginationRequest) (*quartermasterpb.ListClustersResponse, error) {
			return &quartermasterpb.ListClustersResponse{}, nil
		},
	}
	r := platformResolverWith(clientstest.WithQuartermaster(qm))
	tenantCtx := clientstest.AuthedCtx("5eed517e-ba5e-da7a-517e-ba5eda7a0001")
	tenantless := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "jwt")

	for _, tc := range []struct {
		name, message, code string
		err                 error
	}{
		{"platform admin read", "platform operator access required", "FORBIDDEN", errOnly(r.DoPlatformClusters(tenantCtx))},
		{"cluster owner", "cluster owner access required", "FORBIDDEN", r.RequireClusterOperatorTenant(tenantCtx)},
		{"service token", "service token authentication required", "FORBIDDEN", errOnly(r.DoGetBootstrapTokens(tenantCtx))},
		{"billing identity", "authentication required", "UNAUTHORIZED", errOnly(r.DoCreateCardTopup(tenantless, model.CreateCardTopupInput{}))},
		{"tenant context", "tenant context required", "UNAUTHORIZED", errOnly(r.ReadCapabilities(tenantless))},
		{"webhook refusal", "only owners and admins manage webhooks", "FORBIDDEN", errOnly(webhookAuthResult[string](&model.AuthError{Message: "only owners and admins manage webhooks"}, nil))},
		{"input", "dvr id is required", "VALIDATION_ERROR", errOnly(NormalizeDvrID(""))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err == nil {
				t.Fatal("the resolver did not refuse")
			}
			logger := logging.NewLogger()
			hook := logtest.NewLocal(logger)
			presented := gwerrors.ErrorPresenter(logger)(context.Background(), tc.err)
			if presented.Message != tc.message || presented.Extensions["code"] != tc.code {
				t.Fatalf("presented %q %#v, want %q with code %s", presented.Message, presented.Extensions, tc.message, tc.code)
			}
			for _, entry := range hook.AllEntries() {
				if entry.Level <= logrus.WarnLevel {
					t.Fatalf("refusal logged at %s: %s", entry.Level, entry.Message)
				}
			}
		})
	}
}

// A downstream error produces exactly one Bridge log line, at the level its
// gRPC code deserves: a client fault (NotFound, InvalidArgument, ...) at info,
// a platform fault (Internal, Unavailable) at error. The line comes from the
// presenter when the resolver returns the error, and from the resolver when it
// turns the error into a result.
func TestDownstreamErrorLogsOnceAtItsClassLevel(t *testing.T) {
	for _, tc := range []struct {
		name  string
		code  codes.Code
		level logrus.Level
	}{
		{"not found", codes.NotFound, logrus.InfoLevel},
		{"invalid argument", codes.InvalidArgument, logrus.InfoLevel},
		{"internal", codes.Internal, logrus.ErrorLevel},
		{"unavailable", codes.Unavailable, logrus.ErrorLevel},
	} {
		downstream := status.Error(tc.code, "dvr not found")
		if tc.code != codes.NotFound {
			downstream = status.Error(tc.code, "downstream refused")
		}
		commodore := &clientstest.FakeCommodore{
			ListPushTargetsFn: func(context.Context, string) (*commodorepb.ListPushTargetsResponse, error) {
				return nil, downstream
			},
			DeleteDVRFn: func(context.Context, string) (bool, error) { return false, downstream },
		}
		for call, run := range map[string]func(*Resolver) error{
			"returned": func(r *Resolver) error {
				return errOnly(r.DoGetStreamPushTargets(clientstest.AuthedCtx("t1"), "s1"))
			},
			"returned or mapped to a result": func(r *Resolver) error {
				return errOnly(r.DoDeleteDVR(clientstest.AuthedCtx("t1"), "dvr1"))
			},
		} {
			t.Run(tc.name+"/"+call, func(t *testing.T) {
				logger := logging.NewLogger()
				hook := logtest.NewLocal(logger)
				r := &Resolver{Clients: clientstest.Clients(clientstest.WithCommodore(commodore)), Logger: logger}
				if err := run(r); err != nil {
					gwerrors.ErrorPresenter(logger)(context.Background(), err)
				}
				entries := hook.AllEntries()
				if len(entries) != 1 || entries[0].Level != tc.level {
					var got []string
					for _, e := range entries {
						got = append(got, e.Level.String()+": "+e.Message)
					}
					t.Fatalf("log lines = %v, want exactly one at %s", got, tc.level)
				}
			})
		}
	}
}
