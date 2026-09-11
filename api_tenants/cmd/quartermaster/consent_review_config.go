package main

import (
	"crypto/ed25519"
	"fmt"
	"strings"
	"unicode"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
)

func consentReviewSigningConfig(keyID, encoded string) (ed25519.PrivateKey, error) {
	if keyID == "" && encoded == "" {
		return nil, nil
	}
	if keyID == "" || len(keyID) > 100 || strings.TrimSpace(keyID) != keyID || strings.IndexFunc(keyID, unicode.IsControl) >= 0 || encoded == "" {
		return nil, fmt.Errorf("CAPACITY_CONSENT_REVIEW_KEY_ID and CAPACITY_CONSENT_REVIEW_PRIVATE_KEY_PEM_B64 must be configured together with a valid key ID")
	}
	key, err := mediaauthority.ParseSigningPrivateKey(encoded)
	if err != nil {
		return nil, fmt.Errorf("invalid capacity consent review signing key")
	}
	return key, nil
}
