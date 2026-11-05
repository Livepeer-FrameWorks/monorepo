package handlers

import "testing"

func TestExclusiveInvoiceTaxAndLineRounding(t *testing.T) {
	vat, gross := exclusiveTax(10000, 2100)
	if vat != 2100 || gross != 12100 {
		t.Fatalf("100 net did not become 121 gross: VAT=%d gross=%d", vat, gross)
	}
	for _, net := range [][]int64{{1, 1, 1, 1, 1, 1, 1, 1, 1}, {100, -99}, {1000, 2000, -1500}} {
		allocated := allocateInvoiceLineVAT(net, 2100)
		var sumNet, sumVAT int64
		for i, n := range net {
			sumNet += n
			sumVAT += allocated[i]
		}
		want, _ := exclusiveTax(sumNet, 2100)
		if sumVAT != want {
			t.Fatalf("line VAT %v sums to %d instead of %d", allocated, sumVAT, want)
		}
	}
}
