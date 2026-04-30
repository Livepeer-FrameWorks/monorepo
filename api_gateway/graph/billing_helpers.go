package graph

import (
	"fmt"
	"sort"
	"strings"

	"frameworks/api_gateway/graph/model"
)

// entitlementMapToEntries flattens proto's map<string,string> entitlements
// (where each value is a JSON-encoded scalar) into the GraphQL list shape.
// Keys are sorted so the wire response is deterministic and stable across
// proto map-iteration orderings.
func entitlementMapToEntries(m map[string]string) []*model.EntitlementEntry {
	if len(m) == 0 {
		return []*model.EntitlementEntry{}
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]*model.EntitlementEntry, 0, len(keys))
	for _, k := range keys {
		out = append(out, &model.EntitlementEntry{Key: k, Value: m[k]})
	}
	return out
}

func paymentMethodFromPurser(method string) (model.PaymentMethod, error) {
	switch strings.ToLower(method) {
	case "card":
		return model.PaymentMethodCard, nil
	case "crypto_eth":
		return model.PaymentMethodCryptoEth, nil
	case "crypto_usdc":
		return model.PaymentMethodCryptoUsdc, nil
	case "bank_transfer":
		return model.PaymentMethodBankTransfer, nil
	default:
		return "", fmt.Errorf("unknown payment method: %s", method)
	}
}
