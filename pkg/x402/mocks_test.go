package x402

import (
	"context"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
)

type MockPurserClient struct {
	VerifyResponse *purserpb.VerifyX402PaymentResponse
	VerifyError    error
	SettleResponse *purserpb.SettleX402PaymentResponse
	SettleError    error

	VerifyCalled bool
	SettleCalled bool
	LastTenantID string
	LastPayment  *purserpb.X402PaymentPayload
	LastClientIP string
}

func (m *MockPurserClient) VerifyX402Payment(ctx context.Context, tenantID string, payment *purserpb.X402PaymentPayload, clientIP string) (*purserpb.VerifyX402PaymentResponse, error) {
	m.VerifyCalled = true
	m.LastTenantID = tenantID
	m.LastPayment = payment
	m.LastClientIP = clientIP
	return m.VerifyResponse, m.VerifyError
}

func (m *MockPurserClient) SettleX402Payment(ctx context.Context, tenantID string, payment *purserpb.X402PaymentPayload, clientIP string) (*purserpb.SettleX402PaymentResponse, error) {
	m.SettleCalled = true
	m.LastTenantID = tenantID
	m.LastPayment = payment
	m.LastClientIP = clientIP
	return m.SettleResponse, m.SettleError
}

type MockCommodoreClient struct {
	PlaybackResponse         *commodorepb.ResolvePlaybackIDResponse
	PlaybackError            error
	ArtifactPlaybackResponse *commodorepb.ResolveArtifactPlaybackIDResponse
	ArtifactPlaybackError    error
	ClipResponse             *commodorepb.ResolveClipHashResponse
	ClipError                error
	DVRResponse              *commodorepb.ResolveDVRHashResponse
	DVRError                 error
	IdentifierResponse       *commodorepb.ResolveIdentifierResponse
	IdentifierError          error
	VodResponse              *commodorepb.ResolveVodIDResponse
	VodError                 error
	StreamKeyResponse        *commodorepb.ValidateStreamKeyResponse
	StreamKeyError           error
}

func (m *MockCommodoreClient) ResolvePlaybackID(ctx context.Context, playbackID string) (*commodorepb.ResolvePlaybackIDResponse, error) {
	return m.PlaybackResponse, m.PlaybackError
}

func (m *MockCommodoreClient) ResolveArtifactPlaybackID(ctx context.Context, playbackID string) (*commodorepb.ResolveArtifactPlaybackIDResponse, error) {
	return m.ArtifactPlaybackResponse, m.ArtifactPlaybackError
}

func (m *MockCommodoreClient) ResolveClipHash(ctx context.Context, clipHash string) (*commodorepb.ResolveClipHashResponse, error) {
	return m.ClipResponse, m.ClipError
}

func (m *MockCommodoreClient) ResolveDVRHash(ctx context.Context, dvrHash string) (*commodorepb.ResolveDVRHashResponse, error) {
	return m.DVRResponse, m.DVRError
}

func (m *MockCommodoreClient) ResolveIdentifier(ctx context.Context, identifier string) (*commodorepb.ResolveIdentifierResponse, error) {
	return m.IdentifierResponse, m.IdentifierError
}

func (m *MockCommodoreClient) ResolveVodID(ctx context.Context, vodID string) (*commodorepb.ResolveVodIDResponse, error) {
	return m.VodResponse, m.VodError
}

func (m *MockCommodoreClient) ValidateStreamKey(ctx context.Context, streamKey string, _ ...string) (*commodorepb.ValidateStreamKeyResponse, error) {
	return m.StreamKeyResponse, m.StreamKeyError
}
