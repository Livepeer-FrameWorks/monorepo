package x402

import (
	"context"

	pb "frameworks/pkg/proto"
)

type MockPurserClient struct {
	VerifyResponse *pb.VerifyX402PaymentResponse
	VerifyError    error
	SettleResponse *pb.SettleX402PaymentResponse
	SettleError    error

	VerifyCalled bool
	SettleCalled bool
	LastTenantID string
	LastPayment  *pb.X402PaymentPayload
	LastClientIP string
}

func (m *MockPurserClient) VerifyX402Payment(ctx context.Context, tenantID string, payment *pb.X402PaymentPayload, clientIP string) (*pb.VerifyX402PaymentResponse, error) {
	m.VerifyCalled = true
	m.LastTenantID = tenantID
	m.LastPayment = payment
	m.LastClientIP = clientIP
	return m.VerifyResponse, m.VerifyError
}

func (m *MockPurserClient) SettleX402Payment(ctx context.Context, tenantID string, payment *pb.X402PaymentPayload, clientIP string) (*pb.SettleX402PaymentResponse, error) {
	m.SettleCalled = true
	m.LastTenantID = tenantID
	m.LastPayment = payment
	m.LastClientIP = clientIP
	return m.SettleResponse, m.SettleError
}

type MockCommodoreClient struct {
	PlaybackResponse         *pb.ResolvePlaybackIDResponse
	PlaybackError            error
	ArtifactPlaybackResponse *pb.ResolveArtifactPlaybackIDResponse
	ArtifactPlaybackError    error
	ClipResponse             *pb.ResolveClipHashResponse
	ClipError                error
	DVRResponse              *pb.ResolveDVRHashResponse
	DVRError                 error
	IdentifierResponse       *pb.ResolveIdentifierResponse
	IdentifierError          error
	VodResponse              *pb.ResolveVodIDResponse
	VodError                 error
	StreamKeyResponse        *pb.ValidateStreamKeyResponse
	StreamKeyError           error
}

func (m *MockCommodoreClient) ResolvePlaybackID(ctx context.Context, playbackID string) (*pb.ResolvePlaybackIDResponse, error) {
	return m.PlaybackResponse, m.PlaybackError
}

func (m *MockCommodoreClient) ResolveArtifactPlaybackID(ctx context.Context, playbackID string) (*pb.ResolveArtifactPlaybackIDResponse, error) {
	return m.ArtifactPlaybackResponse, m.ArtifactPlaybackError
}

func (m *MockCommodoreClient) ResolveClipHash(ctx context.Context, clipHash string) (*pb.ResolveClipHashResponse, error) {
	return m.ClipResponse, m.ClipError
}

func (m *MockCommodoreClient) ResolveDVRHash(ctx context.Context, dvrHash string) (*pb.ResolveDVRHashResponse, error) {
	return m.DVRResponse, m.DVRError
}

func (m *MockCommodoreClient) ResolveIdentifier(ctx context.Context, identifier string) (*pb.ResolveIdentifierResponse, error) {
	return m.IdentifierResponse, m.IdentifierError
}

func (m *MockCommodoreClient) ResolveVodID(ctx context.Context, vodID string) (*pb.ResolveVodIDResponse, error) {
	return m.VodResponse, m.VodError
}

func (m *MockCommodoreClient) ValidateStreamKey(ctx context.Context, streamKey string, _ ...string) (*pb.ValidateStreamKeyResponse, error) {
	return m.StreamKeyResponse, m.StreamKeyError
}
