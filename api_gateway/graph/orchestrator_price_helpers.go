package graph

import (
	"fmt"
	"strings"
)

// formatWeiAsEth renders wei as an exact ETH decimal. Livepeer quotes prices
// of a few hundred wei per pixel, so every one of the 18 wei digits is kept.
func formatWeiAsEth(wei int64) string {
	const weiPerEth uint64 = 1_000_000_000_000_000_000
	if wei == 0 {
		return "0"
	}
	sign := ""
	magnitude := uint64(wei)
	if wei < 0 {
		sign = "-"
		magnitude = -magnitude
	}
	whole := magnitude / weiPerEth
	frac := magnitude % weiPerEth
	if frac == 0 {
		return fmt.Sprintf("%s%d", sign, whole)
	}
	return strings.TrimRight(fmt.Sprintf("%s%d.%018d", sign, whole, frac), "0")
}
