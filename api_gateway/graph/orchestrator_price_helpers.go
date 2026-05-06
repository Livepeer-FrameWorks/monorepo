package graph

import (
	"fmt"
	"strings"
)

func formatWeiAsEth(wei int64) string {
	const weiPerEth int64 = 1_000_000_000_000_000_000
	if wei == 0 {
		return "0"
	}
	sign := ""
	if wei < 0 {
		sign = "-"
		wei = -wei
	}
	whole := wei / weiPerEth
	frac := (wei % weiPerEth) / 1_000_000
	if frac == 0 {
		return fmt.Sprintf("%s%d", sign, whole)
	}
	out := strings.TrimRight(fmt.Sprintf("%s%d.%012d", sign, whole, frac), "0")
	return strings.TrimRight(out, ".")
}
