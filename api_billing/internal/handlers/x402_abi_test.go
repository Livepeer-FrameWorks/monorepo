package handlers

import (
	"bytes"
	"encoding/hex"
	"math/big"
	"strings"
	"testing"
)

// These helpers ABI-encode the fields that go into the EIP-712 struct hash for
// TransferWithAuthorization (see hashTransferWithAuthorization). That hash is
// what gets signed and submitted on-chain to move USDC, so any misalignment in
// the 32-byte word layout signs over the wrong amount, recipient, or nonce —
// i.e. fund loss or a permanently-failing settlement. The encoding is pure and
// deterministic, so we pin the exact byte layout.

// TestKeccak256 pins the hash against the canonical Keccak-256 empty-input
// vector and proves the variadic form concatenates inputs before hashing
// (hashing a||b, not hashing a and b separately).
func TestKeccak256(t *testing.T) {
	// Keccak-256("") — the well-known empty-input digest.
	const emptyHex = "c5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470"
	if got := hex.EncodeToString(keccak256()); got != emptyHex {
		t.Fatalf("keccak256() = %s, want %s", got, emptyHex)
	}
	if got := hex.EncodeToString(keccak256([]byte{})); got != emptyHex {
		t.Fatalf("keccak256(empty) = %s, want %s", got, emptyHex)
	}

	joined := keccak256([]byte{0x01, 0x02, 0x03})
	split := keccak256([]byte{0x01}, []byte{0x02, 0x03})
	if !bytes.Equal(joined, split) {
		t.Fatal("variadic keccak256 must hash the concatenation of its inputs")
	}
}

// TestPadAddress pins that a 20-byte address lands right-aligned in a 32-byte
// word (left-padded with 12 zero bytes), with the 0x prefix stripped, and that
// malformed/wrong-length input is rejected with an error rather than silently
// zero-padded.
func TestPadAddress(t *testing.T) {
	const addr = "0x00112233445566778899aabbccddeeff00112233"
	got, err := padAddress(addr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 32 {
		t.Fatalf("padAddress len = %d, want 32", len(got))
	}
	for i := 0; i < 12; i++ {
		if got[i] != 0 {
			t.Fatalf("byte %d = %#x, want 0 (address must be right-aligned)", i, got[i])
		}
	}
	want, _ := hex.DecodeString("00112233445566778899aabbccddeeff00112233")
	if !bytes.Equal(got[12:], want) {
		t.Fatalf("address bytes = %x, want %x", got[12:], want)
	}
	// Stripping 0x must yield the same encoding as the bare hex.
	bare, err := padAddress(addr[2:])
	if err != nil {
		t.Fatalf("unexpected error on bare hex: %v", err)
	}
	if !bytes.Equal(got, bare) {
		t.Fatal("0x prefix must not change the encoding")
	}

	for _, bad := range []string{"", "0xzz", "0x1234", "0x" + strings.Repeat("11", 21)} {
		if _, err := padAddress(bad); err == nil {
			t.Fatalf("padAddress(%q) must error (not silently zero-pad)", bad)
		}
	}
}

// TestPadUint256 pins big-endian right-aligned encoding of a decimal uint256.
func TestPadUint256(t *testing.T) {
	cases := []struct {
		name, value string
		wantLast    byte
	}{
		{"zero", "0", 0x00},
		{"one", "1", 0x01},
		{"255", "255", 0xff},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := padUint256(tc.value)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != 32 {
				t.Fatalf("len = %d, want 32", len(got))
			}
			if got[31] != tc.wantLast {
				t.Fatalf("low byte = %#x, want %#x", got[31], tc.wantLast)
			}
			// Everything but the low byte must be zero for these small values.
			for i := 0; i < 31; i++ {
				if got[i] != 0 {
					t.Fatalf("byte %d = %#x, want 0", i, got[i])
				}
			}
		})
	}

	// A value spanning two bytes (256 = 0x0100) must occupy bytes [30] and [31].
	got, err := padUint256("256")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got[30] != 0x01 || got[31] != 0x00 {
		t.Fatalf("256 encoded as ...%#x%#x, want ...0x01 0x00", got[30], got[31])
	}

	// Invalid input must error rather than panic (regression: padUint256 used to
	// ignore SetString's ok and dereference a nil big.Int on empty/garbage input).
	for _, bad := range []string{"", "0xff", "12.5", "abc", "  5", "-1"} {
		if _, err := padUint256(bad); err == nil {
			t.Fatalf("padUint256(%q) must error", bad)
		}
	}
}

// TestPadUint8 pins the single byte landing in the final word position.
func TestPadUint8(t *testing.T) {
	got := padUint8(0x2a)
	if len(got) != 32 {
		t.Fatalf("len = %d, want 32", len(got))
	}
	if got[31] != 0x2a {
		t.Fatalf("got[31] = %#x, want 0x2a", got[31])
	}
	for i := 0; i < 31; i++ {
		if got[i] != 0 {
			t.Fatalf("byte %d = %#x, want 0", i, got[i])
		}
	}
}

// TestPadBytes32 pins that a hex bytes32 value (e.g. the EIP-3009 nonce) is
// left-aligned into the word, with the 0x prefix stripped, since the nonce is
// already a full 32-byte value.
func TestPadBytes32(t *testing.T) {
	const nonce = "0xdeadbeef"
	got, err := padBytes32(nonce)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 32 {
		t.Fatalf("len = %d, want 32", len(got))
	}
	want, _ := hex.DecodeString("deadbeef")
	if !bytes.Equal(got[:4], want) {
		t.Fatalf("leading bytes = %x, want %x (must be left-aligned)", got[:4], want)
	}
	for i := 4; i < 32; i++ {
		if got[i] != 0 {
			t.Fatalf("byte %d = %#x, want 0", i, got[i])
		}
	}

	// Bad hex and over-length (>32 bytes) must error, not panic.
	if _, err := padBytes32("0xzz"); err == nil {
		t.Fatal("padBytes32 with invalid hex must error")
	}
	if _, err := padBytes32("0x" + strings.Repeat("ab", 33)); err == nil {
		t.Fatal("padBytes32 longer than 32 bytes must error")
	}
}

// TestPadBytes32Bytes pins the right-aligned variant used for raw byte slices.
func TestPadBytes32Bytes(t *testing.T) {
	got := padBytes32Bytes([]byte{0xaa, 0xbb})
	if len(got) != 32 {
		t.Fatalf("len = %d, want 32", len(got))
	}
	if got[30] != 0xaa || got[31] != 0xbb {
		t.Fatalf("trailing bytes = %#x %#x, want 0xaa 0xbb (right-aligned)", got[30], got[31])
	}
	for i := 0; i < 30; i++ {
		if got[i] != 0 {
			t.Fatalf("byte %d = %#x, want 0", i, got[i])
		}
	}
}

// TestParseUint256String pins decimal parsing and the rejection of non-decimal
// input — a silent zero here would settle a zero-value or mis-valued transfer.
func TestParseUint256String(t *testing.T) {
	t.Run("valid decimal parses", func(t *testing.T) {
		got, err := parseUint256String("1000000")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Cmp(big.NewInt(1000000)) != 0 {
			t.Fatalf("got %s, want 1000000", got)
		}
	})
	for _, bad := range []string{"", "0xff", "12.5", "abc", "  5"} {
		t.Run("rejects "+bad, func(t *testing.T) {
			if _, err := parseUint256String(bad); err == nil {
				t.Fatalf("parseUint256String(%q) must error", bad)
			}
		})
	}
}

// TestHashTransferWithAuthorization ties the encoders together: the struct hash
// must be deterministic for a fixed authorization and must change if any signed
// field changes (otherwise a tampered transfer would produce the same hash).
func TestHashTransferWithAuthorization(t *testing.T) {
	h := &X402Handler{}
	base := &X402Authorization{
		From:        "0x00112233445566778899aabbccddeeff00112233",
		To:          "0xffeeddccbbaa00998877665544332211ffeeddcc",
		Value:       "1000000",
		ValidAfter:  "0",
		ValidBefore: "9999999999",
		Nonce:       "0xabcdef",
	}
	want, err := h.hashTransferWithAuthorization(base)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(want) != 32 {
		t.Fatalf("struct hash len = %d, want 32", len(want))
	}
	// A different (but well-formed) authorization must hash differently.
	zeroValued := &X402Authorization{
		From:        "0x0000000000000000000000000000000000000000",
		To:          "0x0000000000000000000000000000000000000000",
		Value:       "0",
		ValidAfter:  "0",
		ValidBefore: "0",
		Nonce:       "0x00",
	}
	zeroHash, err := h.hashTransferWithAuthorization(zeroValued)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bytes.Equal(want, zeroHash) {
		t.Fatal("a different authorization must not hash the same as the base one")
	}

	mutations := map[string]func(*X402Authorization){
		"value":       func(a *X402Authorization) { a.Value = "2000000" },
		"to":          func(a *X402Authorization) { a.To = "0x0000000000000000000000000000000000000001" },
		"validBefore": func(a *X402Authorization) { a.ValidBefore = "1" },
		"nonce":       func(a *X402Authorization) { a.Nonce = "0x123456" },
	}
	for field, mutate := range mutations {
		t.Run("changes with "+field, func(t *testing.T) {
			cur := *base
			mutate(&cur)
			got, err := h.hashTransferWithAuthorization(&cur)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if bytes.Equal(want, got) {
				t.Fatalf("struct hash must change when %s changes", field)
			}
		})
	}

	// Regression: a malformed field must surface as an error, never a panic and
	// never a silently zero-padded (forgeable) hash.
	for name, bad := range map[string]*X402Authorization{
		"empty value":   {From: base.From, To: base.To, Value: "", ValidAfter: "0", ValidBefore: "0", Nonce: "0x00"},
		"garbage value": {From: base.From, To: base.To, Value: "abc", ValidAfter: "0", ValidBefore: "0", Nonce: "0x00"},
		"bad address":   {From: "0xnothex", To: base.To, Value: "1", ValidAfter: "0", ValidBefore: "0", Nonce: "0x00"},
	} {
		t.Run("rejects "+name, func(t *testing.T) {
			if _, err := h.hashTransferWithAuthorization(bad); err == nil {
				t.Fatalf("hashTransferWithAuthorization with %s must error", name)
			}
		})
	}
}

// TestIsValidEUVATFormat pins the VAT-number format gate: 8–14 chars after
// removing spaces, uppercased, with a two-letter prefix that is a known EU
// country. Anything else (too short/long, non-EU prefix, numeric prefix) is
// rejected.
func TestIsValidEUVATFormat(t *testing.T) {
	h := &X402Handler{}
	cases := []struct {
		name, vat string
		want      bool
	}{
		{"valid NL", "NL123456789B01", true},
		{"lowercase and spaces normalized", "nl 1234 5678", true},
		{"too short", "NL12345", false},
		{"too long", "NL1234567890123", false},
		{"non-EU prefix", "US123456789", false},
		{"numeric prefix", "1234567890", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := h.isValidEUVATFormat(tc.vat); got != tc.want {
				t.Fatalf("isValidEUVATFormat(%q) = %v, want %v", tc.vat, got, tc.want)
			}
		})
	}
}
