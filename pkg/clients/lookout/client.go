package lookout

import (
	"context"
	"fmt"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/grpcutil"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	lookoutpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/lookout"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

const DefaultServerName = "lookout.internal"

type GRPCClient struct {
	conn    *grpc.ClientConn
	client  lookoutpb.LookoutServiceClient
	logger  logging.Logger
	timeout time.Duration
}

type GRPCConfig struct {
	GRPCAddr      string
	Timeout       time.Duration
	Logger        logging.Logger
	ServiceToken  string
	AllowInsecure bool
	CACertFile    string
	ServerName    string
}

func authInterceptor(serviceToken string) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		return invoker(attachAuthMetadata(ctx, serviceToken), method, req, reply, cc, opts...)
	}
}

// attachAuthMetadata forwards the caller's JWT and identity when present so
// Lookout authorizes the end user; without a JWT the call is a service call.
func attachAuthMetadata(ctx context.Context, serviceToken string) context.Context {
	md := metadata.MD{}
	if existingMD, ok := metadata.FromOutgoingContext(ctx); ok {
		md = existingMD.Copy()
	}
	md.Delete("authorization")
	md.Delete("x-user-id")
	md.Delete("x-tenant-id")

	if userID := ctxkeys.GetUserID(ctx); userID != "" {
		md.Set("x-user-id", userID)
	}
	if tenantID := ctxkeys.GetTenantID(ctx); tenantID != "" {
		md.Set("x-tenant-id", tenantID)
	}

	if jwtToken := ctxkeys.GetJWTToken(ctx); jwtToken != "" {
		md.Set("authorization", "Bearer "+jwtToken)
	} else if serviceToken != "" {
		md.Set("authorization", "Bearer "+serviceToken)
	}

	return metadata.NewOutgoingContext(ctx, md)
}

func NewGRPCClient(config GRPCConfig) (*GRPCClient, error) {
	if config.Timeout == 0 {
		config.Timeout = 10 * time.Second
	}

	transport, err := grpcutil.ClientTLS(grpcutil.ClientTLSConfig{
		CACertFile:        config.CACertFile,
		ServerName:        config.ServerName,
		DefaultServerName: DefaultServerName,
		AllowInsecure:     config.AllowInsecure,
	}, config.Logger)
	if err != nil {
		return nil, fmt.Errorf("configure Lookout gRPC TLS: %w", err)
	}

	conn, err := grpc.NewClient(
		config.GRPCAddr,
		transport,
		grpc.WithChainUnaryInterceptor(
			authInterceptor(config.ServiceToken),
			clients.FailsafeUnaryInterceptor("lookout", config.Logger),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Lookout gRPC: %w", err)
	}

	return &GRPCClient{
		conn:    conn,
		client:  lookoutpb.NewLookoutServiceClient(conn),
		logger:  config.Logger,
		timeout: config.Timeout,
	}, nil
}

func (c *GRPCClient) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

func (c *GRPCClient) callCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok || c.timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, c.timeout)
}

func (c *GRPCClient) ListIncidents(ctx context.Context, req *lookoutpb.ListIncidentsRequest) (*lookoutpb.ListIncidentsResponse, error) {
	ctx, cancel := c.callCtx(ctx)
	defer cancel()
	return c.client.ListIncidents(ctx, req)
}

func (c *GRPCClient) GetIncident(ctx context.Context, incidentID string) (*lookoutpb.GetIncidentResponse, error) {
	ctx, cancel := c.callCtx(ctx)
	defer cancel()
	return c.client.GetIncident(ctx, &lookoutpb.GetIncidentRequest{IncidentId: incidentID})
}

func (c *GRPCClient) AcknowledgeIncident(ctx context.Context, incidentID string) (*lookoutpb.IncidentMutationResponse, error) {
	ctx, cancel := c.callCtx(ctx)
	defer cancel()
	return c.client.AcknowledgeIncident(ctx, &lookoutpb.AcknowledgeIncidentRequest{IncidentId: incidentID})
}

func (c *GRPCClient) AssignIncident(ctx context.Context, incidentID, assigneeUserID string) (*lookoutpb.IncidentMutationResponse, error) {
	ctx, cancel := c.callCtx(ctx)
	defer cancel()
	return c.client.AssignIncident(ctx, &lookoutpb.AssignIncidentRequest{IncidentId: incidentID, AssigneeUserId: assigneeUserID})
}

func (c *GRPCClient) ResolveIncident(ctx context.Context, incidentID string) (*lookoutpb.IncidentMutationResponse, error) {
	ctx, cancel := c.callCtx(ctx)
	defer cancel()
	return c.client.ResolveIncident(ctx, &lookoutpb.ResolveIncidentRequest{IncidentId: incidentID})
}

func (c *GRPCClient) AddIncidentNote(ctx context.Context, incidentID, body string) (*lookoutpb.IncidentMutationResponse, error) {
	ctx, cancel := c.callCtx(ctx)
	defer cancel()
	return c.client.AddIncidentNote(ctx, &lookoutpb.AddIncidentNoteRequest{IncidentId: incidentID, Body: body})
}

// AttachInvestigation is service-only on the Lookout side; callers must not
// carry a user JWT in ctx.
func (c *GRPCClient) AttachInvestigation(ctx context.Context, incidentID, tenantID, reportID string) (*lookoutpb.IncidentMutationResponse, error) {
	ctx, cancel := c.callCtx(ctx)
	defer cancel()
	return c.client.AttachInvestigation(ctx, &lookoutpb.AttachInvestigationRequest{
		IncidentId: incidentID,
		TenantId:   tenantID,
		ReportId:   reportID,
	})
}
