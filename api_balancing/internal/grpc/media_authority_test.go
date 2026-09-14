package grpc

import (
	"context"
	"errors"
	"testing"

	localauthority "frameworks/api_balancing/internal/mediaauthority"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestMediaAuthorityServiceIsInternalServerSurface(t *testing.T) {
	server := grpc.NewServer()
	NewFoghornGRPCServer(nil, nil, nil, nil, nil, nil, nil, nil).RegisterServices(server)
	if _, ok := server.GetServiceInfo()[foghornpb.MediaAuthorityControlService_ServiceDesc.ServiceName]; !ok {
		t.Fatal("media authority service was not registered")
	}
}

func TestApplyMediaAuthorityRequiresConfiguredStore(t *testing.T) {
	server := NewFoghornGRPCServer(nil, nil, nil, nil, nil, nil, nil, nil)
	_, err := server.ApplyMediaAuthority(context.Background(), &foghornpb.ApplyMediaAuthorityRequest{
		Authority: &mediaauthoritypb.SignedAuthorityEnvelope{Envelope: &mediaauthoritypb.AuthorityEnvelope{}},
	})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("code = %s, want Unavailable (%v)", status.Code(err), err)
	}
}

func TestMediaAuthorityStatusClassification(t *testing.T) {
	tests := []struct {
		err  error
		code codes.Code
	}{
		{sharedauthority.ErrWrongAudience, codes.PermissionDenied},
		{sharedauthority.ErrUnknownSigner, codes.PermissionDenied},
		{sharedauthority.ErrInvalidSignature, codes.PermissionDenied},
		{localauthority.ErrRollback, codes.FailedPrecondition},
		{localauthority.ErrVersionConflict, codes.FailedPrecondition},
		{sharedauthority.ErrMalformed, codes.InvalidArgument},
		{sharedauthority.ErrUnknownSchema, codes.InvalidArgument},
		{sharedauthority.ErrPayloadDigest, codes.InvalidArgument},
		{sharedauthority.ErrExpired, codes.InvalidArgument},
		{sharedauthority.ErrNotYetValid, codes.InvalidArgument},
		{sharedauthority.ErrNonCanonical, codes.InvalidArgument},
		{errors.New("database unavailable"), codes.Unavailable},
	}
	for _, test := range tests {
		if got := status.Code(mediaAuthorityStatus(test.err)); got != test.code {
			t.Errorf("mediaAuthorityStatus(%v) = %s, want %s", test.err, got, test.code)
		}
	}
}

func TestAttestCellPlacementCapabilityOnlyFromLedgerReads(t *testing.T) {
	server := NewFoghornGRPCServer(nil, nil, nil, nil, nil, nil, nil, nil)
	if server.attestCellPlacementCapability(context.Background()) != nil {
		t.Fatal("attested without a media authority store")
	}
	server.cellPlacementCapability = func(context.Context) (localauthority.CellPlacementCapability, error) {
		return localauthority.CellPlacementCapability{}, errors.New("ledger unavailable")
	}
	if server.attestCellPlacementCapability(context.Background()) != nil {
		t.Fatal("ledger failure produced an attestation")
	}
	server.cellPlacementCapability = func(context.Context) (localauthority.CellPlacementCapability, error) {
		return localauthority.CellPlacementCapability{SupportedSchemaVersions: []uint32{1, 2}, EnforcementReady: true, LiveReplicas: 1 << 21}, nil
	}
	if server.attestCellPlacementCapability(context.Background()) != nil {
		t.Fatal("implausible replica count produced an attestation")
	}
	server.cellPlacementCapability = func(context.Context) (localauthority.CellPlacementCapability, error) {
		return localauthority.CellPlacementCapability{SupportedSchemaVersions: []uint32{1, 2}, EnforcementReady: true, LiveReplicas: 3}, nil
	}
	got := server.attestCellPlacementCapability(context.Background())
	if got == nil || !got.GetEnforcementReady() || got.GetLiveReplicas() != 3 || len(got.GetSupportedSchemaVersions()) != 2 || got.GetSupportedSchemaVersions()[1] != sharedauthority.PlacementSchemaVersion {
		t.Fatalf("attestation = %v", got)
	}
}

func TestGetMediaCellPlacementCapabilityUsesReplicaLedger(t *testing.T) {
	server := NewFoghornGRPCServer(nil, nil, nil, nil, nil, nil, nil, nil)
	if _, err := server.GetMediaCellPlacementCapability(context.Background(), nil); status.Code(err) != codes.Unavailable {
		t.Fatalf("missing ledger code = %s, want Unavailable", status.Code(err))
	}
	server.cellPlacementCapability = func(context.Context) (localauthority.CellPlacementCapability, error) {
		return localauthority.CellPlacementCapability{
			SupportedSchemaVersions: []uint32{1, sharedauthority.PlacementSchemaVersion},
			EnforcementReady:        true,
			LiveReplicas:            3,
		}, nil
	}
	got, err := server.GetMediaCellPlacementCapability(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !got.GetEnforcementReady() || got.GetLiveReplicas() != 3 {
		t.Fatalf("capability = %+v", got)
	}
}
