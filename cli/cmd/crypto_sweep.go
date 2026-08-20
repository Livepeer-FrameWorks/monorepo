//nolint:govet,errcheck // CLI ceremony stages keep local validation scopes; Cobra required-flag metadata is static.
package cmd

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"os"
	"strings"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/cryptosweep"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"

	"github.com/btcsuite/btcd/btcutil/hdkeychain"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"
)

const sweepCeremonyAcknowledgementPrefix = "I_UNDERSTAND:"

func newCryptoCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "crypto", Short: "Crypto custody operations"}
	cmd.AddCommand(newCryptoReadinessCmd(), newCryptoSmokeCmd(), newCryptoWalletCmd(), newCryptoSweepCmd(), newCryptoMutationCmd())
	return cmd
}

func newCryptoMutationCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "mutation", Short: "Inspect and resolve ambiguous paid mutation results"}
	cmd.AddCommand(newCryptoMutationResolveCmd())
	return cmd
}

func newCryptoMutationResolveCmd() *cobra.Command {
	var tenantID, key, resultPath, contentType, reason string
	var statusCode int32
	var markReview, execute bool
	cmd := &cobra.Command{
		Use: "resolve", Short: "Attach a known owner result or mark an ambiguous paid mutation for review",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if reason == "" || (markReview && resultPath != "") || (!markReview && resultPath == "") {
				return fmt.Errorf("--reason and exactly one of --result-file or --mark-review are required")
			}
			var payload []byte
			var err error
			if resultPath != "" {
				payload, err = os.ReadFile(resultPath)
				if err != nil {
					return err
				}
				if statusCode < 100 || statusCode > 599 {
					return fmt.Errorf("--status-code must be 100-599 when attaching a result")
				}
			}
			client, ctxCfg, cleanup, err := purserGRPCClientFromContext(cmd.Context())
			if err != nil {
				return err
			}
			defer cleanup()
			ctx, cancel := adminRPCContextTimeout(cmd.Context(), ctxCfg.Auth.JWT, 60*time.Second)
			defer cancel()
			response, err := client.ResolveX402MutationResult(ctx, &purserpb.ResolveX402MutationResultRequest{
				TenantId: tenantID, IdempotencyKey: key, Result: payload, ContentType: contentType,
				StatusCode: statusCode, Reason: reason, MarkReview: markReview, DryRun: !execute,
			})
			if err != nil {
				return err
			}
			return printJSONOrText(cmd, response)
		},
	}
	cmd.Flags().StringVar(&tenantID, "tenant-id", "", "tenant UUID owning the paid mutation")
	cmd.Flags().StringVar(&key, "idempotency-key", "", "original mutation idempotency key")
	cmd.Flags().StringVar(&resultPath, "result-file", "", "known result body to attach")
	cmd.Flags().StringVar(&contentType, "content-type", "application/json", "known result content type")
	cmd.Flags().Int32Var(&statusCode, "status-code", 0, "known result HTTP status code")
	cmd.Flags().StringVar(&reason, "reason", "", "operator evidence/reason for the resolution")
	cmd.Flags().BoolVar(&markReview, "mark-review", false, "keep the mutation blocked in operator review without attaching a result")
	cmd.Flags().BoolVar(&execute, "execute", false, "apply the resolution; default is dry-run")
	_ = cmd.MarkFlagRequired("tenant-id")
	_ = cmd.MarkFlagRequired("idempotency-key")
	_ = cmd.MarkFlagRequired("reason")
	return cmd
}

func newCryptoWalletCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "wallet", Short: "Manage public deposit-address derivation keys"}
	cmd.AddCommand(newCryptoWalletRotateCmd())
	return cmd
}

func newCryptoWalletRotateCmd() *cobra.Command {
	var xpubPath, network string
	cmd := &cobra.Command{
		Use: "rotate", Short: "Activate a new deposit xpub without orphaning historical addresses",
		Long: "Activates a new public derivation key for future addresses. Keep every retired offline xprv until repeated sweep plans report no balances for it.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if xpubPath == "" || network == "" {
				return fmt.Errorf("--xpub-file and --network are required")
			}
			contents, err := os.ReadFile(xpubPath)
			if err != nil {
				return err
			}
			if len(contents) > 16<<10 {
				return fmt.Errorf("xpub file is unexpectedly large")
			}
			xpub := strings.TrimSpace(string(contents))
			client, ctxCfg, cleanup, err := purserGRPCClientFromContext(cmd.Context())
			if err != nil {
				return err
			}
			defer cleanup()
			ctx, cancel := adminRPCContextTimeout(cmd.Context(), ctxCfg.Auth.JWT, 60*time.Second)
			defer cancel()
			response, err := client.RotateCryptoDepositKey(ctx, &purserpb.RotateCryptoDepositKeyRequest{Xpub: xpub, Network: network})
			if err != nil {
				return err
			}
			return printJSONOrText(cmd, response)
		},
	}
	cmd.Flags().StringVar(&xpubPath, "xpub-file", "", "file containing the new account-external xpub")
	cmd.Flags().StringVar(&network, "network", "", "derivation network (mainnet or testnet)")
	return cmd
}

func newCryptoReadinessCmd() *cobra.Command {
	return &cobra.Command{
		Use: "readiness", Short: "Report crypto payment and custody production gates",
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, ctxCfg, cleanup, err := purserGRPCClientFromContext(cmd.Context())
			if err != nil {
				return err
			}
			defer cleanup()
			ctx, cancel := adminRPCContextTimeout(cmd.Context(), ctxCfg.Auth.JWT, 60*time.Second)
			defer cancel()
			response, err := client.GetCryptoReadiness(ctx)
			if err != nil {
				return err
			}
			return printJSONOrText(cmd, response)
		},
	}
}

func newCryptoSweepCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use: "sweep", Short: "Run the manual online/offline crypto sweep ceremony",
		Long: "Plan online, sign on an offline machine, broadcast online, then reconcile canonical receipts. Purser never receives the HD deposit private key.",
	}
	cmd.AddCommand(newCryptoSweepPlanCmd(), newCryptoSweepSignCmd(), newCryptoSweepBroadcastCmd(), newCryptoSweepReconcileCmd(), newCryptoSweepReleaseCmd())
	return cmd
}

func newCryptoSweepPlanCmd() *cobra.Command {
	var network, outputPath string
	var persist bool
	cmd := &cobra.Command{
		Use: "plan", Short: "Create a canonical-balance sweep manifest (dry-run by default)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if network == "" || outputPath == "" {
				return fmt.Errorf("--network and --out are required")
			}
			client, ctxCfg, cleanup, err := purserGRPCClientFromContext(cmd.Context())
			if err != nil {
				return err
			}
			defer cleanup()
			ctx, cancel := adminRPCContextTimeout(cmd.Context(), ctxCfg.Auth.JWT, 60*time.Second)
			defer cancel()
			response, err := client.PlanCryptoSweep(ctx, &purserpb.PlanCryptoSweepRequest{Network: network, DryRun: !persist})
			if err != nil {
				return err
			}
			if err := writeExclusiveFile(outputPath, response.GetManifestJson(), 0o644); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Sweep manifest %s (%d items, persisted=%t) written to %s\n", response.GetBatchId(), response.GetItemCount(), response.GetPersisted(), outputPath)
			if !persist {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Dry run only. Re-run with --persist before signing/broadcasting.")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&network, "network", "", "network name (ethereum, base, arbitrum, or explicitly enabled testnet)")
	cmd.Flags().StringVar(&outputPath, "out", "", "new manifest output path (will not overwrite)")
	cmd.Flags().BoolVar(&persist, "persist", false, "claim sources and persist the sweep batch")
	return cmd
}

func newCryptoSweepSignCmd() *cobra.Command {
	var manifestPath, outputPath, allowlistPath string
	var secretFD int
	var maxFeeGwei, maxPriorityFeeGwei uint64
	cmd := &cobra.Command{
		Use: "sign", Short: "Sign a manifest offline using an xprv supplied through a file descriptor",
		Long: "This command performs no network access. The secret must be an account-external xprv matching the manifest xpub and is never accepted through argv or environment variables.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if manifestPath == "" || outputPath == "" || allowlistPath == "" || secretFD < 3 || maxFeeGwei == 0 || maxPriorityFeeGwei == 0 {
				return fmt.Errorf("--manifest, --out, --treasury-allowlist, --secret-fd >= 3, --max-fee-gwei, and --max-priority-fee-gwei are required")
			}
			maxFeeWei := new(big.Int).Mul(new(big.Int).SetUint64(maxFeeGwei), big.NewInt(1_000_000_000))
			maxPriorityFeeWei := new(big.Int).Mul(new(big.Int).SetUint64(maxPriorityFeeGwei), big.NewInt(1_000_000_000))
			if maxPriorityFeeWei.Cmp(maxFeeWei) > 0 {
				return fmt.Errorf("--max-priority-fee-gwei cannot exceed --max-fee-gwei")
			}
			return runCryptoSweepSign(cmd.Context(), cmd.OutOrStdout(), manifestPath, outputPath, allowlistPath, secretFD, maxFeeWei, maxPriorityFeeWei)
		},
	}
	cmd.Flags().StringVar(&manifestPath, "manifest", "", "manifest created by crypto sweep plan")
	cmd.Flags().StringVar(&outputPath, "out", "", "new signed bundle output path (mode 0600, will not overwrite)")
	cmd.Flags().StringVar(&allowlistPath, "treasury-allowlist", "", "offline JSON map of network to approved treasury address")
	cmd.Flags().IntVar(&secretFD, "secret-fd", -1, "already-open descriptor containing the external-chain xprv")
	cmd.Flags().Uint64Var(&maxFeeGwei, "max-fee-gwei", 0, "offline maximum fee-per-gas policy in gwei (required)")
	cmd.Flags().Uint64Var(&maxPriorityFeeGwei, "max-priority-fee-gwei", 0, "offline maximum priority-fee policy in gwei (required)")
	return cmd
}

func runCryptoSweepSign(_ context.Context, output io.Writer, manifestPath, outputPath, allowlistPath string, secretFD int, maxFeeWei, maxPriorityFeeWei *big.Int) error {
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	var manifest cryptosweep.Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return fmt.Errorf("decode manifest: %w", err)
	}
	if err := manifest.Validate(time.Now().UTC()); err != nil {
		return fmt.Errorf("validate manifest: %w", err)
	}
	if err := validateOfflineSweepFeePolicy(manifest, maxFeeWei, maxPriorityFeeWei); err != nil {
		return err
	}
	allowlistBytes, err := os.ReadFile(allowlistPath)
	if err != nil {
		return err
	}
	var allowlist map[string]string
	if err := json.Unmarshal(allowlistBytes, &allowlist); err != nil {
		return fmt.Errorf("decode treasury allowlist: %w", err)
	}
	allowed, ok := allowlist[manifest.Network]
	if !ok || !strings.EqualFold(allowed, manifest.TreasuryAddress) {
		return fmt.Errorf("manifest treasury is not approved for network %s", manifest.Network)
	}
	duplicatedFD, err := unix.Dup(secretFD)
	if err != nil {
		return fmt.Errorf("duplicate secret file descriptor: %w", err)
	}
	secretFile := os.NewFile(uintptr(duplicatedFD), "crypto-sweep-secret")
	if secretFile == nil {
		_ = unix.Close(duplicatedFD)
		return fmt.Errorf("secret file descriptor is invalid")
	}
	defer secretFile.Close()
	secretBytes, err := io.ReadAll(io.LimitReader(secretFile, 16<<10))
	if err != nil {
		return fmt.Errorf("read secret descriptor: %w", err)
	}
	defer func() {
		for i := range secretBytes {
			secretBytes[i] = 0
		}
	}()
	xprv := strings.TrimSpace(string(secretBytes))
	master, err := hdkeychain.NewKeyFromString(xprv)
	if err != nil || !master.IsPrivate() {
		return fmt.Errorf("secret descriptor must contain a valid external-chain xprv")
	}
	xpub, err := master.Neuter()
	if err != nil || xpub.String() != manifest.XPub {
		return fmt.Errorf("secret xprv does not match manifest xpub")
	}
	bundle := cryptosweep.SignedBundle{
		Version: cryptosweep.BundleVersion, Manifest: manifest,
		ManifestChecksum: manifest.Checksum, SignedAt: time.Now().UTC(),
	}
	for _, item := range manifest.Items {
		child, err := master.Derive(item.DerivationIndex)
		if err != nil {
			return fmt.Errorf("derive item %s: %w", item.ItemID, err)
		}
		privateKey, err := child.ECPrivKey()
		if err != nil {
			return fmt.Errorf("derive private key for item %s: %w", item.ItemID, err)
		}
		key, err := ethcrypto.ToECDSA(privateKey.Serialize())
		if err != nil {
			return err
		}
		if !strings.EqualFold(ethcrypto.PubkeyToAddress(key.PublicKey).Hex(), item.SourceAddress) {
			return fmt.Errorf("derived address mismatch for item %s", item.ItemID)
		}
		signedItem := cryptosweep.SignedBundleItem{ItemID: item.ItemID}
		if item.Asset == "ETH" {
			amount, ok := new(big.Int).SetString(item.AmountBaseUnits, 10)
			maxFee, okFee := new(big.Int).SetString(item.MaxFeePerGas, 10)
			tip, okTip := new(big.Int).SetString(item.MaxPriorityFeePerGas, 10)
			if !ok || !okFee || !okTip {
				return fmt.Errorf("invalid ETH numeric fields for item %s", item.ItemID)
			}
			transaction := types.NewTx(&types.DynamicFeeTx{
				ChainID: big.NewInt(manifest.ChainID), Nonce: item.SourceNonce,
				GasTipCap: tip, GasFeeCap: maxFee, Gas: item.GasLimit,
				To: ptrCLIAddress(common.HexToAddress(item.DestinationAddress)), Value: amount,
			})
			signed, err := types.SignTx(transaction, types.LatestSignerForChainID(big.NewInt(manifest.ChainID)), key)
			if err != nil {
				return err
			}
			raw, err := signed.MarshalBinary()
			if err != nil {
				return err
			}
			signedItem.RawTransaction = "0x" + hex.EncodeToString(raw)
		} else {
			digest, err := cryptosweep.EIP3009Digest(manifest, item)
			if err != nil {
				return err
			}
			signature, err := ethcrypto.Sign(digest, key)
			if err != nil {
				return err
			}
			signature[64] += 27
			signedItem.AuthorizationSignature = "0x" + hex.EncodeToString(signature)
		}
		bundle.Items = append(bundle.Items, signedItem)
	}
	if err := bundle.Finalize(); err != nil {
		return err
	}
	if err := bundle.Validate(time.Now().UTC()); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return err
	}
	if err := writeExclusiveFile(outputPath, encoded, 0o600); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(output, "Signed bundle checksum: %s\n", bundle.Checksum)
	return nil
}

func validateOfflineSweepFeePolicy(manifest cryptosweep.Manifest, maxFeeWei, maxPriorityFeeWei *big.Int) error {
	if maxFeeWei == nil || maxPriorityFeeWei == nil || maxFeeWei.Sign() <= 0 || maxPriorityFeeWei.Sign() <= 0 ||
		maxPriorityFeeWei.Cmp(maxFeeWei) > 0 {
		return fmt.Errorf("valid offline fee ceilings are required")
	}
	for _, item := range manifest.Items {
		itemMaxFee, ok := new(big.Int).SetString(item.MaxFeePerGas, 10)
		if !ok || itemMaxFee.Cmp(maxFeeWei) > 0 {
			return fmt.Errorf("item %s max fee exceeds offline ceiling", item.ItemID)
		}
		itemPriorityFee, ok := new(big.Int).SetString(item.MaxPriorityFeePerGas, 10)
		if !ok || itemPriorityFee.Cmp(maxPriorityFeeWei) > 0 {
			return fmt.Errorf("item %s priority fee exceeds offline ceiling", item.ItemID)
		}
	}
	return nil
}

func ptrCLIAddress(value common.Address) *common.Address { return &value }

func newCryptoSweepBroadcastCmd() *cobra.Command {
	var bundlePath, acknowledgement string
	var execute bool
	cmd := &cobra.Command{
		Use: "broadcast", Short: "Validate and broadcast a signed bundle (dry-run by default)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			payload, err := os.ReadFile(bundlePath)
			if err != nil {
				return err
			}
			var bundle cryptosweep.SignedBundle
			if err := json.Unmarshal(payload, &bundle); err != nil {
				return err
			}
			if execute && acknowledgement != bundle.Checksum {
				return fmt.Errorf("--ack must exactly equal signed bundle checksum %s", bundle.Checksum)
			}
			client, ctxCfg, cleanup, err := purserGRPCClientFromContext(cmd.Context())
			if err != nil {
				return err
			}
			defer cleanup()
			ctx, cancel := adminRPCContextTimeout(cmd.Context(), ctxCfg.Auth.JWT, 90*time.Second)
			defer cancel()
			response, err := client.BroadcastCryptoSweep(ctx, &purserpb.BroadcastCryptoSweepRequest{
				SignedBundleJson: payload, DryRun: !execute,
				CeremonyAck: sweepCeremonyAcknowledgementPrefix + acknowledgement,
			})
			if err != nil {
				return err
			}
			return printJSONOrText(cmd, response)
		},
	}
	cmd.Flags().StringVar(&bundlePath, "bundle", "", "signed bundle path (required)")
	cmd.Flags().BoolVar(&execute, "execute", false, "broadcast transactions and persist hashes")
	cmd.Flags().StringVar(&acknowledgement, "ack", "", "exact signed bundle checksum; required with --execute")
	_ = cmd.MarkFlagRequired("bundle")
	return cmd
}

func newCryptoSweepReconcileCmd() *cobra.Command {
	var batchID string
	var apply bool
	cmd := &cobra.Command{
		Use: "reconcile", Short: "Check canonical receipts and finalize sweep items (dry-run by default)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, ctxCfg, cleanup, err := purserGRPCClientFromContext(cmd.Context())
			if err != nil {
				return err
			}
			defer cleanup()
			ctx, cancel := adminRPCContextTimeout(cmd.Context(), ctxCfg.Auth.JWT, 60*time.Second)
			defer cancel()
			response, err := client.ReconcileCryptoSweep(ctx, &purserpb.ReconcileCryptoSweepRequest{BatchId: batchID, DryRun: !apply})
			if err != nil {
				return err
			}
			return printJSONOrText(cmd, response)
		},
	}
	cmd.Flags().StringVar(&batchID, "batch-id", "", "persisted sweep batch UUID")
	cmd.Flags().BoolVar(&apply, "apply", false, "persist confirmed/failed item states")
	_ = cmd.MarkFlagRequired("batch-id")
	return cmd
}

func newCryptoSweepReleaseCmd() *cobra.Command {
	var batchID, reason, acknowledgement string
	var execute bool
	cmd := &cobra.Command{
		Use: "release", Short: "Recover expired or provably unusable sweep claims (dry-run by default)",
		Long: "Rechecks canonical chain state before releasing claims. Signed or broadcast transactions whose outcome cannot be proven are quarantined and remain unavailable to new sweep plans.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(reason) == "" {
				return fmt.Errorf("--reason is required")
			}
			request, err := cryptoSweepReleaseRequest(batchID, reason, acknowledgement, execute)
			if err != nil {
				return err
			}
			client, ctxCfg, cleanup, err := purserGRPCClientFromContext(cmd.Context())
			if err != nil {
				return err
			}
			defer cleanup()
			ctx, cancel := adminRPCContextTimeout(cmd.Context(), ctxCfg.Auth.JWT, 90*time.Second)
			defer cancel()
			response, err := client.ReleaseCryptoSweep(ctx, request)
			if err != nil {
				return err
			}
			return printJSONOrText(cmd, response)
		},
	}
	cmd.Flags().StringVar(&batchID, "batch-id", "", "persisted sweep batch UUID")
	cmd.Flags().StringVar(&reason, "reason", "", "operator reason recorded in the immutable sweep event log")
	cmd.Flags().BoolVar(&execute, "execute", false, "apply eligible releases and quarantines")
	cmd.Flags().StringVar(&acknowledgement, "ack", "", "exact manifest checksum returned by the dry run; required with --execute")
	_ = cmd.MarkFlagRequired("batch-id")
	_ = cmd.MarkFlagRequired("reason")
	return cmd
}

func cryptoSweepReleaseRequest(batchID, reason, acknowledgement string, execute bool) (*purserpb.ReleaseCryptoSweepRequest, error) {
	acknowledgement = strings.TrimSpace(acknowledgement)
	if execute && acknowledgement == "" {
		return nil, fmt.Errorf("--ack must equal the manifest checksum returned by the dry run")
	}
	return &purserpb.ReleaseCryptoSweepRequest{
		BatchId: batchID, Reason: reason, DryRun: !execute,
		CeremonyAck: sweepCeremonyAcknowledgementPrefix + acknowledgement,
	}, nil
}

func writeExclusiveFile(path string, payload []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("create %s without overwrite: %w", path, err)
	}
	defer file.Close()
	if _, err := file.Write(payload); err != nil {
		return err
	}
	return file.Sync()
}

func printJSONOrText(cmd *cobra.Command, value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), string(encoded))
	return err
}
