// GraphQL union interface markers and enum marshaling for purser proto types.

package purserpb

import (
	"fmt"
	"io"
	"strconv"
)

// PaymentResponse implements union interfaces (GraphQL type: Payment)
func (*PaymentResponse) IsCreatePaymentResult() {}

// MarshalGQL implements the graphql.Marshaler interface for CryptoAsset
func (e CryptoAsset) MarshalGQL(w io.Writer) {
	var s string
	switch e {
	case CryptoAsset_CRYPTO_ASSET_ETH:
		s = "ETH"
	case CryptoAsset_CRYPTO_ASSET_USDC:
		s = "USDC"
	case CryptoAsset_CRYPTO_ASSET_LPT:
		s = "LPT"
	default:
		s = "ETH"
	}
	io.WriteString(w, strconv.Quote(s)) //nolint:errcheck // MarshalGQL has no error return
}

// UnmarshalGQL implements the graphql.Unmarshaler interface for CryptoAsset
func (e *CryptoAsset) UnmarshalGQL(v any) error {
	str, ok := v.(string)
	if !ok {
		return fmt.Errorf("enums must be strings")
	}
	switch str {
	case "ETH":
		*e = CryptoAsset_CRYPTO_ASSET_ETH
	case "USDC":
		*e = CryptoAsset_CRYPTO_ASSET_USDC
	case "LPT":
		*e = CryptoAsset_CRYPTO_ASSET_LPT
	default:
		return fmt.Errorf("%s is not a valid CryptoAsset", str)
	}
	return nil
}
