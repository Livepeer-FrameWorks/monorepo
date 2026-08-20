// Package cryptosweep defines the deterministic files exchanged between the
// online and offline stages of the manual crypto custody ceremony. It contains
// no network or secret-key access.
package cryptosweep

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"
)

const (
	ManifestVersion = 1
	BundleVersion   = 1
	// MaxSweepGasLimit bounds the worst-case fee exposure accepted by the
	// offline signer. ETH transfers use 21,000 and USDC relays normally use
	// about 150,000, leaving headroom without accepting an arbitrary limit.
	MaxSweepGasLimit = 500_000
)

type Manifest struct {
	Version           int            `json:"version"`
	BatchID           string         `json:"batch_id"`
	Network           string         `json:"network"`
	ChainID           int64          `json:"chain_id"`
	TreasuryAddress   string         `json:"treasury_address"`
	XPub              string         `json:"xpub"`
	SnapshotBlock     int64          `json:"snapshot_block"`
	SnapshotBlockHash string         `json:"snapshot_block_hash"`
	CreatedAt         time.Time      `json:"created_at"`
	ExpiresAt         time.Time      `json:"expires_at"`
	Items             []ManifestItem `json:"items"`
	Checksum          string         `json:"checksum"`
}

type ManifestItem struct {
	ItemID               string `json:"item_id"`
	CustodyAddressID     string `json:"custody_address_id"`
	WalletID             string `json:"wallet_id,omitempty"`
	Asset                string `json:"asset"`
	SourceAddress        string `json:"source_address"`
	DerivationIndex      uint32 `json:"derivation_index"`
	DestinationAddress   string `json:"destination_address"`
	AmountBaseUnits      string `json:"amount_base_units"`
	AssetContract        string `json:"asset_contract,omitempty"`
	SourceNonce          uint64 `json:"source_nonce,omitempty"`
	MaxFeePerGas         string `json:"max_fee_per_gas,omitempty"`
	MaxPriorityFeePerGas string `json:"max_priority_fee_per_gas,omitempty"`
	GasLimit             uint64 `json:"gas_limit,omitempty"`
	AuthorizationNonce   string `json:"authorization_nonce,omitempty"`
	AuthorizationAfter   int64  `json:"authorization_after,omitempty"`
	AuthorizationBefore  int64  `json:"authorization_before,omitempty"`
	TokenDomainName      string `json:"token_domain_name,omitempty"`
	TokenDomainVersion   string `json:"token_domain_version,omitempty"`
}

type SignedBundle struct {
	Version          int                `json:"version"`
	Manifest         Manifest           `json:"manifest"`
	ManifestChecksum string             `json:"manifest_checksum"`
	SignedAt         time.Time          `json:"signed_at"`
	Items            []SignedBundleItem `json:"items"`
	Checksum         string             `json:"checksum"`
}

type SignedBundleItem struct {
	ItemID                 string `json:"item_id"`
	RawTransaction         string `json:"raw_transaction,omitempty"`
	AuthorizationSignature string `json:"authorization_signature,omitempty"`
}

func checksumJSON(value any) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func (m Manifest) calculatedChecksum() (string, error) {
	m.Checksum = ""
	sort.Slice(m.Items, func(i, j int) bool { return m.Items[i].ItemID < m.Items[j].ItemID })
	return checksumJSON(m)
}

func (m *Manifest) Finalize() error {
	if m.Version == 0 {
		m.Version = ManifestVersion
	}
	sort.Slice(m.Items, func(i, j int) bool { return m.Items[i].ItemID < m.Items[j].ItemID })
	checksum, err := m.calculatedChecksum()
	if err != nil {
		return err
	}
	m.Checksum = checksum
	return nil
}

func (m Manifest) Validate(now time.Time) error {
	if m.Version != ManifestVersion {
		return fmt.Errorf("unsupported manifest version %d", m.Version)
	}
	if m.BatchID == "" || m.Network == "" || m.ChainID <= 0 || m.XPub == "" {
		return fmt.Errorf("manifest identity, network, chain_id, and xpub are required")
	}
	if !isAddress(m.TreasuryAddress) || m.SnapshotBlock < 0 || !isHash(m.SnapshotBlockHash) {
		return fmt.Errorf("invalid treasury or snapshot")
	}
	if !m.ExpiresAt.After(now) || m.CreatedAt.After(m.ExpiresAt) || m.CreatedAt.After(now.Add(5*time.Minute)) {
		return fmt.Errorf("manifest is expired or has an invalid validity window")
	}
	if len(m.Items) == 0 {
		return fmt.Errorf("manifest has no sweep items")
	}
	custodyAssets := map[string]struct{}{}
	itemIDs := map[string]struct{}{}
	for _, item := range m.Items {
		if item.ItemID == "" || item.CustodyAddressID == "" || !isAddress(item.SourceAddress) || !isAddress(item.DestinationAddress) {
			return fmt.Errorf("invalid sweep item identity or address")
		}
		if _, exists := itemIDs[item.ItemID]; exists {
			return fmt.Errorf("duplicate item id %s", item.ItemID)
		}
		itemIDs[item.ItemID] = struct{}{}
		if !strings.EqualFold(item.DestinationAddress, m.TreasuryAddress) {
			return fmt.Errorf("item %s destination does not match treasury", item.ItemID)
		}
		if item.Asset != "ETH" && item.Asset != "USDC" {
			return fmt.Errorf("item %s has unsupported asset %q", item.ItemID, item.Asset)
		}
		maxFee, maxFeeOK := positiveDecimal(item.MaxFeePerGas)
		maxPriorityFee, maxPriorityFeeOK := positiveDecimal(item.MaxPriorityFeePerGas)
		if !isPositiveDecimal(item.AmountBaseUnits) || !maxFeeOK || !maxPriorityFeeOK ||
			item.GasLimit == 0 || item.GasLimit > MaxSweepGasLimit {
			return fmt.Errorf("item %s has invalid amount", item.ItemID)
		}
		if maxPriorityFee.Cmp(maxFee) > 0 {
			return fmt.Errorf("item %s priority fee exceeds max fee", item.ItemID)
		}
		key := item.CustodyAddressID + "\x00" + item.Asset
		if _, exists := custodyAssets[key]; exists {
			return fmt.Errorf("duplicate custody/asset item %s", item.ItemID)
		}
		custodyAssets[key] = struct{}{}
		if item.Asset == "ETH" {
			if item.AssetContract != "" || item.AuthorizationNonce != "" || item.AuthorizationBefore != 0 {
				return fmt.Errorf("ETH item %s contains token authorization fields", item.ItemID)
			}
		} else if !isAddress(item.AssetContract) || !isHash(item.AuthorizationNonce) ||
			item.AuthorizationAfter < 0 || item.AuthorizationBefore <= item.AuthorizationAfter ||
			item.AuthorizationBefore > m.ExpiresAt.Unix() || strings.TrimSpace(item.TokenDomainName) == "" ||
			strings.TrimSpace(item.TokenDomainVersion) == "" {
			return fmt.Errorf("USDC item %s has invalid token authorization fields", item.ItemID)
		}
	}
	checksum, err := m.calculatedChecksum()
	if err != nil {
		return err
	}
	if checksum != m.Checksum {
		return fmt.Errorf("manifest checksum mismatch")
	}
	return nil
}

func (b SignedBundle) calculatedChecksum() (string, error) {
	b.Checksum = ""
	sort.Slice(b.Items, func(i, j int) bool { return b.Items[i].ItemID < b.Items[j].ItemID })
	return checksumJSON(b)
}

func (b *SignedBundle) Finalize() error {
	if b.Version == 0 {
		b.Version = BundleVersion
	}
	sort.Slice(b.Items, func(i, j int) bool { return b.Items[i].ItemID < b.Items[j].ItemID })
	checksum, err := b.calculatedChecksum()
	if err != nil {
		return err
	}
	b.Checksum = checksum
	return nil
}

func (b SignedBundle) Validate(now time.Time) error {
	if b.Version != BundleVersion {
		return fmt.Errorf("unsupported bundle version %d", b.Version)
	}
	if err := b.Manifest.Validate(now); err != nil {
		return err
	}
	if b.ManifestChecksum != b.Manifest.Checksum {
		return fmt.Errorf("bundle manifest checksum mismatch")
	}
	if len(b.Items) != len(b.Manifest.Items) {
		return fmt.Errorf("bundle item count does not match manifest")
	}
	manifestItems := make(map[string]ManifestItem, len(b.Manifest.Items))
	for _, item := range b.Manifest.Items {
		manifestItems[item.ItemID] = item
	}
	seen := make(map[string]struct{}, len(b.Items))
	for _, item := range b.Items {
		if _, duplicate := seen[item.ItemID]; duplicate {
			return fmt.Errorf("duplicate signed item %s", item.ItemID)
		}
		seen[item.ItemID] = struct{}{}
		manifest, ok := manifestItems[item.ItemID]
		if !ok {
			return fmt.Errorf("signed item %s is absent from manifest", item.ItemID)
		}
		if manifest.Asset == "ETH" && !strings.HasPrefix(item.RawTransaction, "0x") {
			return fmt.Errorf("ETH item %s is missing raw transaction", item.ItemID)
		}
		if manifest.Asset == "USDC" && !isSignature(item.AuthorizationSignature) {
			return fmt.Errorf("USDC item %s is missing authorization signature", item.ItemID)
		}
	}
	checksum, err := b.calculatedChecksum()
	if err != nil {
		return err
	}
	if checksum != b.Checksum {
		return fmt.Errorf("bundle checksum mismatch")
	}
	return nil
}

func isAddress(value string) bool {
	return isHexBytes(value, 20) && value != "0x"+strings.Repeat("0", 40)
}

func isHash(value string) bool {
	return isHexBytes(value, 32)
}

func isSignature(value string) bool {
	return isHexBytes(value, 65)
}

func isHexBytes(value string, size int) bool {
	if len(value) != 2+size*2 || !strings.HasPrefix(value, "0x") {
		return false
	}
	_, err := hex.DecodeString(value[2:])
	return err == nil
}

func isPositiveDecimal(value string) bool {
	_, ok := positiveDecimal(value)
	return ok
}

func positiveDecimal(value string) (*big.Int, bool) {
	parsed, ok := new(big.Int).SetString(value, 10)
	return parsed, ok && parsed.Sign() > 0
}
