package mediaauthority

import (
	"errors"
	"fmt"

	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	"google.golang.org/protobuf/proto"
)

var ErrSecretUnavailable = errors.New("media authority sealed secret unavailable")

func (s *Store) OpenLiveStreamSecret(snapshot MediaObjectSnapshot) (*mediaauthoritypb.LiveStreamSecret, error) {
	if s == nil || s.sealPrivateKey == nil || s.sealKeyID == "" || snapshot.Authority == nil || snapshot.Authority.GetLiveStream() == nil {
		return nil, ErrSecretUnavailable
	}
	var selected *mediaauthoritypb.SealedCellSecret
	for _, box := range snapshot.Authority.GetLiveStream().GetSealedCellSecrets() {
		if box.GetAudienceCellId() == s.cellID && box.GetRecipientKeyId() == s.sealKeyID {
			if selected != nil {
				return nil, errors.New("multiple sealed secrets match the local cell key")
			}
			selected = box
		}
	}
	if selected == nil {
		return nil, ErrSecretUnavailable
	}
	plaintext, err := sharedauthority.OpenSecret(selected, s.cellID, snapshot.AuthorityID, s.sealKeyID, s.sealPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("open live-stream secret: %w", err)
	}
	secret := &mediaauthoritypb.LiveStreamSecret{}
	if err := (proto.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(plaintext, secret); err != nil {
		return nil, fmt.Errorf("decode live-stream secret: %w", err)
	}
	if secret.GetAuthorityId() != snapshot.AuthorityID || secret.GetTenantId() != snapshot.Authority.GetTenantId() {
		return nil, errors.New("live-stream secret identity mismatch")
	}
	return secret, nil
}

func (s *Store) OpenPlaybackWebhookSecret(snapshot MediaObjectSnapshot) (*mediaauthoritypb.PlaybackWebhookSecret, error) {
	if s == nil || s.sealPrivateKey == nil || s.sealKeyID == "" || snapshot.Authority == nil {
		return nil, ErrSecretUnavailable
	}
	var selected *mediaauthoritypb.SealedCellSecret
	for _, box := range snapshot.Authority.GetSealedPlaybackSecrets() {
		if box.GetAudienceCellId() == s.cellID && box.GetRecipientKeyId() == s.sealKeyID {
			if selected != nil {
				return nil, errors.New("multiple sealed playback secrets match the local cell key")
			}
			selected = box
		}
	}
	if selected == nil {
		return nil, ErrSecretUnavailable
	}
	plaintext, err := sharedauthority.OpenSecret(selected, s.cellID, snapshot.AuthorityID, s.sealKeyID, s.sealPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("open media-object playback secret: %w", err)
	}
	secret := &mediaauthoritypb.MediaObjectSecret{}
	if err := (proto.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(plaintext, secret); err != nil {
		return nil, fmt.Errorf("decode media-object playback secret: %w", err)
	}
	if secret.GetAuthorityId() != snapshot.AuthorityID || secret.GetTenantId() != snapshot.Authority.GetTenantId() || secret.GetPlaybackWebhook() == nil {
		return nil, errors.New("media-object playback secret identity mismatch")
	}
	return secret.GetPlaybackWebhook(), nil
}
