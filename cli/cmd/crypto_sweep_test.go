//nolint:govet // The ceremony fixture intentionally reuses local error names at each independent assertion.
package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/cryptosweep"
	"github.com/btcsuite/btcd/btcutil/hdkeychain"
	"github.com/btcsuite/btcd/chaincfg"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

func TestRunCryptoSweepSignUsesFDAndProducesVerifiableBundle(t *testing.T) {
	master, err := hdkeychain.NewMaster(bytes.Repeat([]byte{0x42}, 32), &chaincfg.MainNetParams)
	if err != nil {
		t.Fatal(err)
	}
	xpub, err := master.Neuter()
	if err != nil {
		t.Fatal(err)
	}
	child, err := master.Derive(7)
	if err != nil {
		t.Fatal(err)
	}
	privateKey, err := child.ECPrivKey()
	if err != nil {
		t.Fatal(err)
	}
	key, err := ethcrypto.ToECDSA(privateKey.Serialize())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	manifest := cryptosweep.Manifest{
		BatchID: "11111111-1111-1111-1111-111111111111", Network: "base", ChainID: 8453,
		TreasuryAddress: "0x1111111111111111111111111111111111111111", XPub: xpub.String(),
		SnapshotBlock: 100, SnapshotBlockHash: "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
		Items: []cryptosweep.ManifestItem{{
			ItemID:           "22222222-2222-2222-2222-222222222222",
			CustodyAddressID: "33333333-3333-3333-3333-333333333333", WalletID: "wallet",
			Asset: "USDC", SourceAddress: strings.ToLower(ethcrypto.PubkeyToAddress(key.PublicKey).Hex()),
			DerivationIndex: 7, DestinationAddress: "0x1111111111111111111111111111111111111111",
			AmountBaseUnits: "5000000", AssetContract: "0x3333333333333333333333333333333333333333",
			AuthorizationNonce: "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			AuthorizationAfter: now.Add(-time.Minute).Unix(), AuthorizationBefore: now.Add(time.Hour).Unix(),
			TokenDomainName: "USD Coin", TokenDomainVersion: "2", GasLimit: 150000,
			MaxFeePerGas: "3000000000", MaxPriorityFeePerGas: "1000000000",
		}},
	}
	if err := manifest.Finalize(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "manifest.json")
	allowlistPath := filepath.Join(dir, "treasury.json")
	bundlePath := filepath.Join(dir, "bundle.json")
	manifestJSON, _ := json.Marshal(manifest)
	if err := os.WriteFile(manifestPath, manifestJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(allowlistPath, []byte(`{"base":"0x1111111111111111111111111111111111111111"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.WriteString(master.String()); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	defer reader.Close()
	if err := runCryptoSweepSign(context.Background(), &bytes.Buffer{}, manifestPath, bundlePath, allowlistPath, int(reader.Fd()), big.NewInt(5_000_000_000), big.NewInt(2_000_000_000)); err != nil {
		t.Fatal(err)
	}
	bundleJSON, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	var bundle cryptosweep.SignedBundle
	if err := json.Unmarshal(bundleJSON, &bundle); err != nil {
		t.Fatal(err)
	}
	if err := bundle.Validate(time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if len(bundle.Items) != 1 || len(bundle.Items[0].AuthorizationSignature) != 132 {
		t.Fatalf("unexpected signed bundle: %+v", bundle.Items)
	}
}

func TestOfflineSweepFeePolicyRejectsPlannerFeeAboveOperatorCeiling(t *testing.T) {
	manifest := cryptosweep.Manifest{Items: []cryptosweep.ManifestItem{{
		ItemID: "item", MaxFeePerGas: "6000000000", MaxPriorityFeePerGas: "1000000000",
	}}}
	if err := validateOfflineSweepFeePolicy(manifest, big.NewInt(5_000_000_000), big.NewInt(2_000_000_000)); err == nil {
		t.Fatal("fee above the independent offline ceiling unexpectedly passed")
	}
	manifest.Items[0].MaxFeePerGas = "5000000000"
	manifest.Items[0].MaxPriorityFeePerGas = "3000000000"
	if err := validateOfflineSweepFeePolicy(manifest, big.NewInt(5_000_000_000), big.NewInt(2_000_000_000)); err == nil {
		t.Fatal("priority fee above the independent offline ceiling unexpectedly passed")
	}
}

func TestCryptoSweepReleaseRequestDefaultsToDryRunAndBindsAcknowledgement(t *testing.T) {
	dryRun, err := cryptoSweepReleaseRequest("batch", "expired plan", "", false)
	if err != nil || !dryRun.GetDryRun() {
		t.Fatalf("dry-run request=%+v err=%v", dryRun, err)
	}
	if _, err := cryptoSweepReleaseRequest("batch", "expired plan", "", true); err == nil {
		t.Fatal("execute without acknowledgement unexpectedly succeeded")
	}
	execute, err := cryptoSweepReleaseRequest("batch", "expired plan", " checksum ", true)
	if err != nil {
		t.Fatal(err)
	}
	if execute.GetDryRun() || execute.GetCeremonyAck() != sweepCeremonyAcknowledgementPrefix+"checksum" {
		t.Fatalf("execute request=%+v", execute)
	}
}
