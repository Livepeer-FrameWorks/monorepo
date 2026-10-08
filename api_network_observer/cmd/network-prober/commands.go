package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"frameworks/api_network_observer/internal/appconfig"
	"frameworks/api_network_observer/internal/providers/livepeer"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/ethereum/go-ethereum/accounts/keystore"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/joho/godotenv"
)

func readCredential(path string, private bool) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("could not open credential file")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || (private && info.Mode().Perm()&0077 != 0) {
		return nil, fmt.Errorf("credential file must be regular and private files must have mode 0600 or stricter")
	}
	raw, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return nil, fmt.Errorf("could not read bounded credential file")
	}
	return raw, nil
}

func signDiscovery(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("sign-discovery", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	keyFile := flags.String("key-file", "", "Private hex-key file (offline use only)")
	keystoreFile := flags.String("keystore", "", "Encrypted Ethereum keystore")
	passwordFile := flags.String("password-file", "", "Private keystore password file")
	output := flags.String("output", "", "New private environment file")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("usage: network-prober sign-discovery (--keystore FILE --password-file FILE | --key-file FILE) --output FILE")
	}
	if flags.NArg() != 0 || *output == "" || (*keyFile == "") == (*keystoreFile == "") || (*keystoreFile != "" && *passwordFile == "") || (*keyFile != "" && *passwordFile != "") {
		return fmt.Errorf("choose one key source and an output file; keystore signing also needs --password-file")
	}
	var key *keystore.Key
	if *keystoreFile != "" {
		raw, err := readCredential(*keystoreFile, false)
		if err != nil {
			return err
		}
		password, err := readCredential(*passwordFile, true)
		if err != nil {
			return err
		}
		key, err = keystore.DecryptKey(raw, strings.TrimSuffix(strings.TrimSuffix(string(password), "\n"), "\r"))
		if err != nil {
			return fmt.Errorf("could not decrypt Ethereum keystore")
		}
	} else {
		raw, err := readCredential(*keyFile, true)
		if err != nil {
			return err
		}
		privateKey, err := crypto.HexToECDSA(strings.TrimPrefix(strings.TrimSpace(string(raw)), "0x"))
		if err != nil {
			return fmt.Errorf("invalid private-key file")
		}
		key = &keystore.Key{PrivateKey: privateKey}
	}
	address := crypto.PubkeyToAddress(key.PrivateKey.PublicKey)
	signature, err := crypto.Sign(livepeer.DiscoveryDigest(address), key.PrivateKey)
	if err != nil {
		return fmt.Errorf("could not sign discovery identity")
	}
	signature[64] += 27
	file, err := os.OpenFile(*output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("could not create identity file; existing files are never overwritten")
	}
	complete := false
	defer func() {
		_ = file.Close()
		if !complete {
			_ = os.Remove(*output)
		}
	}()
	if _, err = fmt.Fprintf(file, "LIVEPEER_BROADCASTER_ADDRESS=%s\nLIVEPEER_BROADCASTER_SIGNATURE=0x%s\n", address.Hex(), hex.EncodeToString(signature)); err != nil {
		return fmt.Errorf("could not write identity file")
	}
	if err = file.Sync(); err != nil {
		return fmt.Errorf("could not persist identity file")
	}
	if err = file.Close(); err != nil {
		return fmt.Errorf("could not close identity file")
	}
	complete = true
	_, err = fmt.Fprintln(out, "Discovery identity written. Copy only this environment file to the prober; retain the funded wallet and its key on the signing host.")
	return err
}

func configuredProbe(cfg *appconfig.Prober) (*livepeer.Probe, error) {
	u, err := url.Parse(cfg.CoordinatorURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && (!cfg.AllowInsecureCoordinator || u.Scheme != "http")) {
		return nil, fmt.Errorf("coordinator requires verified HTTPS; local HTTP needs explicit opt-in")
	}
	if cfg.Concurrency < 1 || cfg.Concurrency > 64 || cfg.TimeoutSeconds < 1 || cfg.TimeoutSeconds > 300 || cfg.SpoolMaxBytes < 1 || !filepath.IsAbs(cfg.SpoolDirectory) {
		return nil, fmt.Errorf("invalid prober limits or spool path; use an absolute persistent directory")
	}
	if len(cfg.ObserverID) > 128 || strings.ContainsAny(cfg.ObserverID, "\r\n") || strings.HasPrefix(cfg.ObserverID, "gateway:") || len(cfg.Region) > 128 {
		return nil, fmt.Errorf("invalid observer identity or region")
	}
	return livepeer.NewProbe(cfg.BroadcasterAddress, cfg.BroadcasterSignature, cfg.SkipLivepeerTLSVerify, time.Duration(cfg.TimeoutSeconds)*time.Second)
}

func checkConfig(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("check-config", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	envFile := flags.String("env-file", "", "Prober environment file")
	identityFile := flags.String("identity-file", "", "Discovery address/signature environment file")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return fmt.Errorf("usage: network-prober check-config [--env-file FILE] [--identity-file FILE]")
	}
	options := config.Options{Service: "network-prober"}
	if *envFile != "" || *identityFile != "" {
		values := map[string]string{}
		for _, path := range []string{*envFile, *identityFile} {
			if path == "" {
				continue
			}
			raw, err := readCredential(path, true)
			if err != nil {
				return err
			}
			parsed, err := godotenv.Unmarshal(string(raw))
			if err != nil {
				return fmt.Errorf("could not parse prober environment file")
			}
			for name, value := range parsed {
				values[name] = value
			}
		}
		options.Lookup = func(name string) (string, bool) { value, exists := values[name]; return value, exists }
	}
	cfg, err := config.Load[appconfig.Prober](options)
	if err != nil {
		return err
	}
	if _, err = configuredProbe(cfg); err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, "Prober configuration and discovery signature are valid. Network access and sender reserve still need a successful discovery observation.")
	return err
}
