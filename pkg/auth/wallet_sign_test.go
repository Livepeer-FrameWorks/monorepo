package auth

import (
	"strings"
	"testing"
)

func TestSignWalletMessageVerifiesAsLogin(t *testing.T) {
	const key = "4c0883a69102937d6231471b5dbb6204fe5129617082792ae468d01a3f362318"
	message := GenerateWalletAuthMessage("stack-nonce")

	address, signature, err := SignWalletMessage(key, message)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	// The address the reference vector above belongs to.
	if address != "0x2c7536E3605D9C16a7a3D7b1898e529396a65c23" {
		t.Fatalf("address = %s, want the key's checksummed address", address)
	}
	ok, err := VerifyWalletAuth(WalletMessage{Address: address, Message: message, Signature: signature})
	if err != nil || !ok {
		t.Fatalf("a signed login message does not verify: ok=%v err=%v", ok, err)
	}
	if !strings.HasPrefix(signature, "0x") || len(signature) != 2+130 {
		t.Fatalf("signature = %q, want 0x-prefixed 65 bytes", signature)
	}
}

func TestSignWalletMessageRejectsMalformedKey(t *testing.T) {
	for _, key := range []string{"", "zz", strings.Repeat("ab", 31)} {
		if _, _, err := SignWalletMessage(key, "FrameWorks Login"); err == nil {
			t.Fatalf("key %q signed, want an error", key)
		}
	}
}
