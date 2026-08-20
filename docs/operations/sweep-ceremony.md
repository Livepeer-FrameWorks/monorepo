# Manual Crypto Sweep Ceremony

This runbook moves confirmed ETH and USDC from FrameWorks watch-only deposit
addresses to an approved cold treasury. Purser never receives the HD xprv.
Planning, broadcasting, and reconciliation are online; signing is performed on
an isolated machine.

Two operators should verify the manifest and treasury independently. Purser
accepts any configured non-zero EVM address; a cold multisig/Safe is an
operational recommendation, not an application requirement.

## Preconditions

- Purser reports a usable consensus-labelled `finalized` head. Production does
  not accept `safe` or a latest-minus-confirmations fallback.
- `CRYPTO_TREASURY_<NETWORK>` is the approved destination.
- USDC requires a funded, dedicated gas-only
  `CRYPTO_SWEEP_RELAYER_PRIVATE_KEY_<NETWORK>`. This is never the HD deposit key.
- The online operator has a platform-operator token and a configured CLI
  context.
- The offline machine has the matching external-chain xprv, the `frameworks`
  CLI, and a separately maintained treasury allowlist JSON file:

```json
{
  "base": "0xApprovedTreasuryAddress"
}
```

## 1. Plan online

Dry-run first. It does not reserve sources and cannot be signed:

```sh
frameworks crypto sweep plan --network base --out base-dry-run.json
```

After both operators compare the snapshot, item sources, amounts, fee ceilings,
chain ID, token contract, and treasury against independent records, persist a
new manifest:

```sh
frameworks crypto sweep plan --network base --out base-manifest.json --persist
```

The output path must not already exist. Copy the manifest to the offline machine
using the organization's approved removable-media procedure. Record the batch
ID and manifest checksum in the change ticket.

## 2. Sign offline

Disconnect networking before starting. Verify the CLI binary checksum and the
treasury allowlist from independent media. Supply the decrypted xprv through an
already-open file descriptor; never put it in argv, an environment variable, a
shell variable, or a plaintext manifest.

The offline signer must also receive independent operator-selected ceilings via
`--max-fee-gwei` and `--max-priority-fee-gwei`. These are not copied from the
online manifest: signing fails if any planned fee exceeds either offline limit.
The signer also rejects gas limits above 500,000.

One example using a local `age` identity and process file descriptor is:

```sh
frameworks crypto sweep sign \
  --manifest base-manifest.json \
  --treasury-allowlist treasury-allowlist.json \
  --secret-fd 3 \
  --max-fee-gwei 5 \
  --max-priority-fee-gwei 2 \
  --out base-signed.json \
  3< <(age --decrypt -i offline-identity.txt deposit-xprv.age)
```

The signer verifies the manifest checksum, expiry, xpub, every child-derived
source address, chain, token, and destination before writing a mode-0600 bundle.
Record the signed bundle checksum displayed by the CLI, securely erase transient
decryption material, and transfer only the signed bundle back online.

## 3. Validate and broadcast online

Run the default dry-run first:

```sh
frameworks crypto sweep broadcast --bundle base-signed.json
```

Both operators compare the decoded signed bundle to the persisted manifest and
approve the exact bundle checksum. Broadcast only with that checksum:

```sh
frameworks crypto sweep broadcast \
  --bundle base-signed.json \
  --execute \
  --ack <signed-bundle-checksum>
```

Purser revalidates the persisted batch, treasury, source signer, current chain,
nonce/balance, amount, token, and fee ceiling. A submit timeout is an unknown
outcome: do not create or sign a replacement batch and do not blindly rerun the
broadcast. Continue with reconciliation.

## 4. Reconcile finalized receipts

Check without changing state:

```sh
frameworks crypto sweep reconcile --batch-id <batch-uuid>
```

When the expected receipts are included at the network's finalized head and
their block hashes are canonical, persist the result:

```sh
frameworks crypto sweep reconcile --batch-id <batch-uuid> --apply
```

Verify the cold treasury receipt independently and attach transaction hashes,
finalized block hashes, CLI output, and both operator approvals to the change
ticket.

## Interrupted and partial batches

- Planning failed before `--persist`: discard the dry-run file and plan again.
- Persisted but unsigned: let the manifest expire, then dry-run
  `frameworks crypto sweep release --batch-id <uuid> --reason "expired offline ceremony"`.
  Execute only with `--execute --ack <manifest-checksum>` from that output. The
  server rechecks canonical balance before making the sources replannable.
- Signed but not broadcast: do not broadcast after expiry. Retain the bundle as
  sensitive audit evidence and use the approved expired-batch recovery.
- Broadcast returned an RPC error or timed out: treat every affected item as
  unknown. Reconcile by the precomputed transaction hash; never re-sign with a
  changed nonce until the existing intent is resolved.
- Partial broadcast: repeatedly reconcile the same batch. Do not create a new
  batch for its claimed sources. Escalate failed items with their immutable
  sweep events.
- Receipt reverted: reconciliation marks the item failed. Investigate chain,
  balance, fee, relayer gas, and token-domain evidence before any replacement.
- Receipt block becomes non-canonical before finality: the item remains pending.
  If a finalized receipt later disappears, stop new crypto money movement and
  open a custody incident; do not mark the batch complete manually.

Release never turns ambiguity into permission to retry. Expired, canonically
unused USDC authorizations and ETH intents whose source nonce has advanced can
be released; unresolved signed/broadcast items are quarantined. Every decision
is written to the immutable sweep event log.

Database rows and immutable sweep events are the operational source of truth.
Never repair a batch by editing its manifest, signed payload, transaction hash,
or status directly.

## Key and treasury rotation

`frameworks crypto wallet rotate --xpub-file new-deposit.xpub --network mainnet`
changes the public derivation key used only for future addresses. It does not
move funds. Every issued custody address stores its original xpub, and a sweep
manifest contains exactly one xpub, so repeat planning/signing with the matching
retired xprv until all old-key balances are empty. Never destroy a retired xprv
while an address derived from it may still receive or hold funds.

Changing `CRYPTO_TREASURY_<NETWORK>` changes only future sweep destinations.
Let unbroadcast manifests expire and create new ones after the change. Funds
already held by the old Safe move through a separately approved Safe
transaction; the deposit sweep service does not drain one treasury into another.

Monitor the `CryptoScannerLagging`, `CryptoScannerErrors`,
`CryptoCustodyBalanceAtRisk`, `CryptoSweepItemFailed`,
`CryptoSweepRelayerGasLow`, and `CryptoAllocatedDepositReorg` alerts throughout
the ceremony and until every item is finalized.

## Change record

The deterministic CLI and Purser test suites prove signing vectors, rejection
paths, duplicate broadcast replay, and reorg-safe reconciliation without
inventing a public testnet environment. Before moving customer value, capture
the live read-only readiness report. When funds actually need sweeping, retain
the approved production ceremony record with:

- source and cold-treasury transaction hashes;
- manifest and bundle checksums;
- finalized snapshot and receipt block hashes;
- interruption/unknown-outcome reconciliation evidence;
- wrong-destination and stale-manifest rejection evidence;
- two-person approval and CLI/build version.

This record proves which movement was approved and completed; it is not a
runtime environment-variable gate.
