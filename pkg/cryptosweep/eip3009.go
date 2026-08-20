package cryptosweep

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	ethmath "github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/signer/core/apitypes"
)

// EIP3009Digest returns the exact EIP-712 digest signed by an offline sweep
// signer and recovered again by the online broadcaster.
func EIP3009Digest(manifest Manifest, item ManifestItem) ([]byte, error) {
	value, ok := new(big.Int).SetString(item.AmountBaseUnits, 10)
	if !ok || value.Sign() <= 0 {
		return nil, fmt.Errorf("invalid authorization value")
	}
	nonce := common.HexToHash(item.AuthorizationNonce)
	typed := apitypes.TypedData{
		Types: apitypes.Types{
			"EIP712Domain": {
				{Name: "name", Type: "string"},
				{Name: "version", Type: "string"},
				{Name: "chainId", Type: "uint256"},
				{Name: "verifyingContract", Type: "address"},
			},
			"TransferWithAuthorization": {
				{Name: "from", Type: "address"},
				{Name: "to", Type: "address"},
				{Name: "value", Type: "uint256"},
				{Name: "validAfter", Type: "uint256"},
				{Name: "validBefore", Type: "uint256"},
				{Name: "nonce", Type: "bytes32"},
			},
		},
		PrimaryType: "TransferWithAuthorization",
		Domain: apitypes.TypedDataDomain{
			Name: item.TokenDomainName, Version: item.TokenDomainVersion,
			ChainId:           ethmath.NewHexOrDecimal256(manifest.ChainID),
			VerifyingContract: item.AssetContract,
		},
		Message: apitypes.TypedDataMessage{
			"from": item.SourceAddress, "to": item.DestinationAddress,
			"value": value.String(), "validAfter": fmt.Sprintf("%d", item.AuthorizationAfter),
			"validBefore": fmt.Sprintf("%d", item.AuthorizationBefore), "nonce": nonce.Hex(),
		},
	}
	digest, _, err := apitypes.TypedDataAndHash(typed)
	return digest, err
}
