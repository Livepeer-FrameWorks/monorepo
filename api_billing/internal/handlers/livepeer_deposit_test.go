package handlers

import (
	"math/big"
	"testing"
)

func TestEthToWei_OneETH(t *testing.T) {
	wei := ethToWei(1.0)
	expected := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil) // 1e18
	if wei.Cmp(expected) != 0 {
		t.Fatalf("expected %s, got %s", expected, wei)
	}
}

func TestEthToWei_FractionalETH(t *testing.T) {
	wei := ethToWei(0.2)
	expected := new(big.Int).Mul(big.NewInt(2), new(big.Int).Exp(big.NewInt(10), big.NewInt(17), nil)) // 2e17
	if wei.Cmp(expected) != 0 {
		t.Fatalf("expected %s, got %s", expected, wei)
	}
}

func TestEthToWei_Zero(t *testing.T) {
	wei := ethToWei(0)
	if wei.Cmp(big.NewInt(0)) != 0 {
		t.Fatalf("expected 0, got %s", wei)
	}
}

func TestWeiToETH_Roundtrip(t *testing.T) {
	original := 0.123456
	wei := ethToWei(original)
	result := weiToETH(wei)
	diff := original - result
	if diff < 0 {
		diff = -diff
	}
	if diff > 1e-10 {
		t.Fatalf("roundtrip: expected %f, got %f (diff: %e)", original, result, diff)
	}
}
