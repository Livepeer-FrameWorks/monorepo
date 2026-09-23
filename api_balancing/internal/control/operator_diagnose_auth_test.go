package control

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

type operatorDiagnoseDVRServer struct {
	foghornpb.UnimplementedDVRControlServiceServer
}

func (operatorDiagnoseDVRServer) DiagnoseDVR(ctx context.Context, _ *foghornpb.DiagnoseDVRRequest) (*foghornpb.DiagnoseDVRResponse, error) {
	if err := middleware.RequirePlatformOperatorJWT(ctx); err != nil {
		return nil, err
	}
	return &foghornpb.DiagnoseDVRResponse{}, nil
}

func (operatorDiagnoseDVRServer) StopDVR(context.Context, *sharedpb.StopDVRRequest) (*sharedpb.StopDVRResponse, error) {
	return &sharedpb.StopDVRResponse{}, nil
}

// The production interceptor chain must let an operator JWT reach DiagnoseDVR
// while every other DVR control method keeps refusing JWTs.
func TestCommonInterceptorsAdmitOperatorJWTForDiagnoseDVROnly(t *testing.T) {
	secret := "operator-secret"
	operatorJWT, err := auth.GenerateSessionJWT("user-op", "tenant-a", "op@example.test", "member", []string{auth.RolePlatformOperator}, time.Time{}, []byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	listener := bufconn.Listen(1 << 20)
	srv := grpc.NewServer(appendCommonInterceptors(nil, GRPCServerConfig{
		Logger:         logging.NewLogger(),
		ServiceToken:   "service-token",
		JWTSecret:      secret,
		MetadataPolicy: middleware.MetadataPolicyDeny,
	})...)
	foghornpb.RegisterDVRControlServiceServer(srv, operatorDiagnoseDVRServer{})
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := foghornpb.NewDVRControlServiceClient(conn)

	call := func(token string) context.Context {
		return metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+token)
	}
	if _, err := client.DiagnoseDVR(call(operatorJWT), &foghornpb.DiagnoseDVRRequest{DvrHash: "h"}); err != nil {
		t.Fatalf("operator JWT refused on DiagnoseDVR: %v", err)
	}
	if _, err := client.DiagnoseDVR(call("service-token"), &foghornpb.DiagnoseDVRRequest{DvrHash: "h"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("service token on DiagnoseDVR: got %v, want PermissionDenied", err)
	}
	if _, err := client.StopDVR(call(operatorJWT), &sharedpb.StopDVRRequest{DvrHash: "h"}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("operator JWT on StopDVR: got %v, want Unauthenticated", err)
	}
}
