package cryptosweep

import (
	"strings"
	"testing"
	"time"
)

func TestManifestAndBundleChecksumsRejectTampering(t *testing.T) {
	now := time.Now().UTC()
	manifest := Manifest{
		BatchID: "batch", Network: "base", ChainID: 8453,
		TreasuryAddress: "0x1111111111111111111111111111111111111111",
		XPub:            "xpub-test", SnapshotBlock: 10,
		SnapshotBlockHash: "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		CreatedAt:         now, ExpiresAt: now.Add(time.Hour),
		Items: []ManifestItem{{
			ItemID: "item", CustodyAddressID: "custody", WalletID: "wallet", Asset: "USDC",
			SourceAddress:      "0x2222222222222222222222222222222222222222",
			DestinationAddress: "0x1111111111111111111111111111111111111111",
			AmountBaseUnits:    "5000000", AssetContract: "0x3333333333333333333333333333333333333333",
			MaxFeePerGas: "3000000000", MaxPriorityFeePerGas: "1000000000", GasLimit: 150000,
			AuthorizationNonce: "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			AuthorizationAfter: now.Add(-time.Minute).Unix(), AuthorizationBefore: now.Add(time.Hour).Unix(),
			TokenDomainName: "USD Coin", TokenDomainVersion: "2",
		}},
	}
	if err := manifest.Finalize(); err != nil {
		t.Fatal(err)
	}
	if err := manifest.Validate(now); err != nil {
		t.Fatal(err)
	}
	bundle := SignedBundle{
		Manifest: manifest, ManifestChecksum: manifest.Checksum, SignedAt: now,
		Items: []SignedBundleItem{{ItemID: "item", AuthorizationSignature: "0x" + string(make([]byte, 130))}},
	}
	// Use hex characters rather than NULs for the signature fixture.
	bundle.Items[0].AuthorizationSignature = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := bundle.Finalize(); err != nil {
		t.Fatal(err)
	}
	if err := bundle.Validate(now); err != nil {
		t.Fatal(err)
	}
	bundle.Manifest.Items[0].AmountBaseUnits = "6000000"
	if err := bundle.Validate(now); err == nil {
		t.Fatal("tampered bundle unexpectedly validated")
	}
}

func TestManifestRejectsSemanticallyInvalidSweepItems(t *testing.T) {
	now := time.Now().UTC()
	valid := Manifest{
		BatchID: "batch", Network: "base", ChainID: 8453,
		TreasuryAddress: "0x1111111111111111111111111111111111111111", XPub: "xpub-test",
		SnapshotBlock: 10, SnapshotBlockHash: "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
		Items: []ManifestItem{{
			ItemID: "item", CustodyAddressID: "custody", Asset: "USDC",
			SourceAddress:      "0x2222222222222222222222222222222222222222",
			DestinationAddress: "0x1111111111111111111111111111111111111111",
			AmountBaseUnits:    "5000000", AssetContract: "0x3333333333333333333333333333333333333333",
			MaxFeePerGas: "3000000000", MaxPriorityFeePerGas: "1000000000", GasLimit: 150000,
			AuthorizationNonce: "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			AuthorizationAfter: now.Add(-time.Minute).Unix(), AuthorizationBefore: now.Add(time.Hour).Unix(),
			TokenDomainName: "USD Coin", TokenDomainVersion: "2",
		}},
	}
	tests := map[string]func(*Manifest){
		"wrong destination": func(m *Manifest) { m.Items[0].DestinationAddress = "0x4444444444444444444444444444444444444444" },
		"non-hex source":    func(m *Manifest) { m.Items[0].SourceAddress = "0xzz22222222222222222222222222222222222222" },
		"zero amount":       func(m *Manifest) { m.Items[0].AmountBaseUnits = "0" },
		"missing token":     func(m *Manifest) { m.Items[0].AssetContract = "" },
		"unbounded gas":     func(m *Manifest) { m.Items[0].GasLimit = MaxSweepGasLimit + 1 },
		"tip above max fee": func(m *Manifest) { m.Items[0].MaxPriorityFeePerGas = "4000000000" },
		"expired manifest": func(m *Manifest) {
			m.CreatedAt = now.Add(-2 * time.Hour)
			m.ExpiresAt = now.Add(-time.Hour)
		},
		"stale authorization": func(m *Manifest) {
			m.Items[0].AuthorizationBefore = m.ExpiresAt.Add(time.Second).Unix()
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			candidate.Items = append([]ManifestItem(nil), valid.Items...)
			mutate(&candidate)
			if err := candidate.Finalize(); err != nil {
				t.Fatal(err)
			}
			if err := candidate.Validate(now); err == nil {
				t.Fatal("invalid manifest unexpectedly validated")
			}
		})
	}

	withoutWallet := valid
	withoutWallet.Items = append([]ManifestItem(nil), valid.Items...)
	withoutWallet.Items[0].WalletID = ""
	if err := withoutWallet.Finalize(); err != nil {
		t.Fatal(err)
	}
	if err := withoutWallet.Validate(now); err != nil {
		t.Fatalf("x402 custody item without direct wallet id must validate: %v", err)
	}
}

func TestSignedBundleRejectsPartialAndDuplicateItems(t *testing.T) {
	now := time.Now().UTC()
	manifest := Manifest{
		BatchID: "batch", Network: "base", ChainID: 8453,
		TreasuryAddress: "0x1111111111111111111111111111111111111111", XPub: "xpub-test",
		SnapshotBlock: 10, SnapshotBlockHash: "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	for _, id := range []string{"one", "two"} {
		manifest.Items = append(manifest.Items, ManifestItem{
			ItemID: id, CustodyAddressID: id, Asset: "USDC",
			SourceAddress: "0x2222222222222222222222222222222222222222", DestinationAddress: manifest.TreasuryAddress,
			AmountBaseUnits: "1", AssetContract: "0x3333333333333333333333333333333333333333",
			MaxFeePerGas: "2", MaxPriorityFeePerGas: "1", GasLimit: 150000,
			AuthorizationNonce: "0x" + map[string]string{"one": "aa", "two": "bb"}[id] + strings.Repeat("00", 31),
			AuthorizationAfter: now.Add(-time.Minute).Unix(), AuthorizationBefore: now.Add(time.Hour).Unix(),
			TokenDomainName: "USD Coin", TokenDomainVersion: "2",
		})
	}
	if err := manifest.Finalize(); err != nil {
		t.Fatal(err)
	}
	signature := "0x" + strings.Repeat("aa", 65)
	partial := SignedBundle{Version: BundleVersion, Manifest: manifest, ManifestChecksum: manifest.Checksum, SignedAt: now,
		Items: []SignedBundleItem{{ItemID: "one", AuthorizationSignature: signature}}}
	if err := partial.Finalize(); err != nil {
		t.Fatal(err)
	}
	if err := partial.Validate(now); err == nil {
		t.Fatal("partial bundle unexpectedly validated")
	}
	duplicate := partial
	duplicate.Items = []SignedBundleItem{
		{ItemID: "one", AuthorizationSignature: signature},
		{ItemID: "one", AuthorizationSignature: signature},
	}
	if err := duplicate.Finalize(); err != nil {
		t.Fatal(err)
	}
	if err := duplicate.Validate(now); err == nil {
		t.Fatal("duplicate bundle item unexpectedly validated")
	}
}
