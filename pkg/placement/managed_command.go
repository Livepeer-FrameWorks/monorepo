package placement

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"math"
	"strings"
	"time"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/proto"
)

func ManagedCommandDigest(command *ipcpb.ApplyManagedStream) ([]byte, error) {
	if command == nil || proto.Size(command) > 1<<20 {
		return nil, errors.New("managed command is missing or exceeds bound")
	}
	copyCommand := proto.CloneOf(command)
	copyCommand.PlacementAdmission = nil
	encoded, err := (proto.MarshalOptions{Deterministic: true}).Marshal(copyCommand)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(append([]byte("frameworks/managed-stream-command/v1\x00"), encoded...))
	return digest[:], nil
}

// ValidateManagedCommand verifies an authenticated control-channel attestation.
// It is not signature verification or authority for arbitrary external callers.
func ValidateManagedCommand(command *ipcpb.ApplyManagedStream, nodeID string, now time.Time) error {
	a := command.GetPlacementAdmission()
	if a == nil || a.GetSchemaVersion() != 1 || now.IsZero() || command.GetIngestMode() != "mist_native" {
		return errors.New("managed placement admission is missing or unsupported")
	}
	for _, id := range []string{nodeID, a.GetTargetNodeId(), a.GetTargetClusterId(), command.GetTenantId(), command.GetStreamId(), command.GetName()} {
		if id == "" || len(id) > 255 || strings.TrimSpace(id) != id {
			return errors.New("managed command identity is invalid")
		}
	}
	if a.GetTargetNodeId() != nodeID || a.GetObjectAuthorityVersion() <= 0 || a.GetTenantAuthorityVersion() <= 0 || a.GetPolicyRevision() > math.MaxInt64 || a.GetParentRevision() > math.MaxInt64 || command.GetSource() == "" {
		return errors.New("managed command destination or authority version is invalid")
	}
	digest, err := hex.DecodeString(a.GetPolicyDigest())
	if err != nil || len(digest) != sha256.Size || hex.EncodeToString(digest) != a.GetPolicyDigest() {
		return errors.New("managed policy digest is invalid")
	}
	if a.GetIssuedAt() == nil || !a.GetIssuedAt().IsValid() || a.GetExpiresAt() == nil || !a.GetExpiresAt().IsValid() {
		return errors.New("managed command lifetime is invalid")
	}
	issued, until := a.GetIssuedAt().AsTime(), a.GetExpiresAt().AsTime()
	if issued.After(now.Add(time.Second)) || !now.Before(until) || !issued.Before(until) || until.Sub(issued) > PreparationLifetime {
		return errors.New("managed command is expired, future-issued or overlong")
	}
	computed, err := ManagedCommandDigest(command)
	if err != nil || subtle.ConstantTimeCompare(computed, a.GetCommandSha256()) != 1 {
		return errors.New("managed command differs from admitted configuration")
	}
	return nil
}

// ValidateManagedRetraction binds cleanup to a previously admitted command.
// Its fresh cleanup deadline is independent of the old Apply's expired lease.
func ValidateManagedRetraction(command *ipcpb.RetractManagedStream, nodeID string, now time.Time) error {
	r := command.GetPlacementRetraction()
	if r == nil || now.IsZero() || r.GetTargetNodeId() != nodeID || r.GetObjectAuthorityVersion() <= 0 || r.GetTenantAuthorityVersion() <= 0 || len(r.GetCommandSha256()) != sha256.Size ||
		r.GetPolicyRevision() > math.MaxInt64 || r.GetParentRevision() > math.MaxInt64 {
		return errors.New("managed retraction identity or version is invalid")
	}
	for _, id := range []string{nodeID, r.GetTargetClusterId(), r.GetTenantId(), command.GetStreamId(), command.GetName()} {
		if id == "" || len(id) > 255 || strings.TrimSpace(id) != id {
			return errors.New("managed retraction identity is invalid")
		}
	}
	digest, err := hex.DecodeString(r.GetPolicyDigest())
	if err != nil || len(digest) != sha256.Size || hex.EncodeToString(digest) != r.GetPolicyDigest() {
		return errors.New("managed retraction policy digest is invalid")
	}
	if r.GetIssuedAt() == nil || !r.GetIssuedAt().IsValid() || r.GetExpiresAt() == nil || !r.GetExpiresAt().IsValid() {
		return errors.New("managed retraction lifetime is invalid")
	}
	issued, until := r.GetIssuedAt().AsTime(), r.GetExpiresAt().AsTime()
	if issued.After(now.Add(time.Second)) || !now.Before(until) || !issued.Before(until) || until.Sub(issued) > PreparationLifetime {
		return errors.New("managed retraction is expired, future-issued or overlong")
	}
	return nil
}
