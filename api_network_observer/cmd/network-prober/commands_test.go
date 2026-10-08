package main

import (
	"bytes"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/accounts/keystore"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	"github.com/joho/godotenv"
)

func TestOfflineSigningAndConfigValidation(t *testing.T) {
	directory := t.TempDir()
	key, err := crypto.HexToECDSA("0000000000000000000000000000000000000000000000000000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	address := crypto.PubkeyToAddress(key.PublicKey)
	encrypted, err := keystore.EncryptKey(&keystore.Key{Id: uuid.New(), Address: address, PrivateKey: key}, "test-password", 1024, 1)
	if err != nil {
		t.Fatal(err)
	}
	keyFile, passwordFile, output := filepath.Join(directory, "keystore"), filepath.Join(directory, "password"), filepath.Join(directory, "identity.env")
	if err = os.WriteFile(keyFile, encrypted, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(passwordFile, []byte("test-password\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	args := []string{"--keystore", keyFile, "--password-file", passwordFile, "--output", output}
	if err = signDiscovery(args, &stdout); err != nil {
		t.Fatal(err)
	}
	identity, err := godotenv.Read(output)
	if err != nil {
		t.Fatal(err)
	}
	signature := strings.TrimPrefix(identity["LIVEPEER_BROADCASTER_SIGNATURE"], "0x")
	if identity["LIVEPEER_BROADCASTER_ADDRESS"] != address.Hex() || strings.Contains(stdout.String(), signature) || strings.Contains(stdout.String(), hex.EncodeToString(crypto.FromECDSA(key))) {
		t.Fatal("identity or command output is invalid")
	}
	// Verify the two hashing steps independently from the signing helper.
	expected, err := crypto.Sign(accounts.TextHash(crypto.Keccak256([]byte(address.Hex()))), key)
	if err != nil {
		t.Fatal(err)
	}
	expected[64] += 27
	if identity["LIVEPEER_BROADCASTER_SIGNATURE"] != "0x"+hex.EncodeToString(expected) {
		t.Fatal("signature differs from the Livepeer wire protocol")
	}
	stat, err := os.Stat(output)
	if err != nil || stat.Mode().Perm() != 0600 {
		t.Fatal("identity file is not private")
	}
	before, _ := os.ReadFile(output)
	if err = signDiscovery(args, &stdout); err == nil {
		t.Fatal("existing identity was overwritten")
	}
	after, _ := os.ReadFile(output)
	if !bytes.Equal(before, after) {
		t.Fatal("existing identity changed")
	}
	configPath := filepath.Join(directory, "prober.env")
	configuration := "NETWORK_OBSERVER_URL=https://observer.example.net\nNETWORK_OBSERVER_SECRET=test-ingest-secret\nNETWORK_PROBER_ID=test-prober\nNETWORK_PROBER_REGION=eu\nNETWORK_PROBER_SPOOL_DIR=" + filepath.Join(directory, "spool") + "\n"
	if err = os.WriteFile(configPath, []byte(configuration), 0600); err != nil {
		t.Fatal(err)
	}
	if err = checkConfig([]string{"--env-file", configPath, "--identity-file", output}, &stdout); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout.String(), "test-ingest-secret") || strings.Contains(stdout.String(), "test-password") || strings.Contains(stdout.String(), signature) {
		t.Fatal("preflight printed a credential")
	}
}
