package handlers

import (
	"testing"
)

func TestHexQuantityToDecimal(t *testing.T) {
	got, err := hexQuantityToDecimal("0xde0b6b3a7640000")
	if err != nil || got != "1000000000000000000" {
		t.Fatalf("hexQuantityToDecimal() = %q, %v", got, err)
	}
	if _, err := hexQuantityToDecimal("not-hex"); err == nil {
		t.Fatal("malformed quantity was accepted")
	}
}

func TestCryptoScannerStartBlockRequiresProductionAnchor(t *testing.T) {
	t.Setenv("BUILD_ENV", "production")
	t.Setenv("CRYPTO_SCAN_START_BLOCK_BASE", "")
	if _, err := cryptoScannerStartBlock("base", 10_000); err == nil {
		t.Fatal("production scanner accepted an implicit start block")
	}
	t.Setenv("CRYPTO_SCAN_START_BLOCK_BASE", "1234")
	got, err := cryptoScannerStartBlock("base", 10_000)
	if err != nil || got != 1234 {
		t.Fatalf("start block = %d, %v", got, err)
	}
}

func TestCryptoScannerDevelopmentBootstrapIsBounded(t *testing.T) {
	t.Setenv("BUILD_ENV", "development")
	t.Setenv("CRYPTO_SCAN_START_BLOCK_BASE", "")
	got, err := cryptoScannerStartBlock("base", 10_000)
	if err != nil || got != 9_000 {
		t.Fatalf("start block = %d, %v", got, err)
	}
}
