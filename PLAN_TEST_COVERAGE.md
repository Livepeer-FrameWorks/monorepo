# Plan: Fix Mutation Testing Findings

## Status
- Owner: Codex
- State: Implemented
- Last updated: 2025-02-14

## Scope
Address mutation testing findings from PLAN_MUTATION_TESTING.md, focusing on LIVED mutations and coverage gaps in security- and money-critical paths.

## Phase 1: LIVED Mutations (quick wins)
### 1.1 pkg/auth/middleware.go — WebSocket bypass
- File: `pkg/auth/middleware_test.go`
- Add cases:
  - Upgrade: websocket + Connection: Upgrade, no auth → 200
  - Upgrade: websocket only, no auth → 401
  - Connection: Upgrade only, no auth → 401
  - Upgrade: h2c + Connection: Upgrade → 401

### 1.2 pkg/auth/wallet.go — Timestamp boundaries
- File: `pkg/auth/wallet_test.go`
- Add boundary cases:
  - exactly 1 minute future → fail
  - 59 seconds future → pass
  - exactly 5 minutes old → fail
  - 4:59 old → pass

### 1.3 pkg/x402/x402.go — Empty metadata array
- File: `pkg/x402/x402_test.go`
- Add cases:
  - metadata key with empty []string{} → returns ""
  - whitespace-only value → returns ""
  - fallback to payment-signature when x-payment empty

### 1.4 pkg/x402/x402.go — Base64 fallback order
- File: `pkg/x402/x402_test.go`
- Add cases:
  - RawStdEncoding works as final fallback
  - StdEncoding tried before URL variants (use + to distinguish)

## Phase 2: Security-critical coverage
### 2.1 pkg/auth/api_tokens.go — ValidateAPIToken, HasPermission
- Create: `pkg/auth/api_tokens_test.go`
- Add cases:
  - valid active token → returns *APIToken
  - token not found → ErrInvalidAPIToken
  - expired token → ErrExpiredAPIToken
  - inactive token → ErrInvalidAPIToken
  - database error → wrapped error
  - HasPermission: exact match true, missing false, empty/nil false
  - HashToken: deterministic, distinct inputs, 64 hex chars
- Dependency: `github.com/DATA-DOG/go-sqlmock`

### 2.2 pkg/auth/wallet.go — VerifyEthSignature, VerifyWalletAuth
- File: `pkg/auth/wallet_test.go`
- Add cases with Ethereum test vectors:
  - valid signature with V=27 → true
  - valid signature with V=28 → true
  - signature from wrong address → false
  - invalid signature length (<65 bytes) → error
  - invalid V value (>28) → error
  - malformed hex signature → error
  - VerifyWalletAuth: invalid address format, expired message, valid flow

## Phase 3: Money-critical coverage
### 3.1 Create mocks
- Create: `pkg/x402/mocks_test.go`
- Mock clients:
  - `MockPurserClient`: Verify/Settle responses and errors
  - `MockCommodoreClient`: Resolve* responses and errors

### 3.2 Settlement tests
- Create: `pkg/x402/settlement_test.go`
- Add cases:
  - IsAuthOnlyPayment: nil payload, nil inner payload, nil authorization, value "0" true, non-zero false, empty false, non-numeric false
  - SettleX402Payment: nil purser, missing payment, auth-only rejected, verification error, billing detail error, settlement error, success path
  - ResolveResource: empty resource error; graphql://; mcp:// converts; viewer:// with/without commodore; stream://; ingest valid/invalid key; clip/dvr/vod

## Files Summary
- Modify: `pkg/auth/middleware_test.go`
- Modify: `pkg/auth/wallet_test.go`
- Create: `pkg/auth/api_tokens_test.go`
- Modify: `pkg/x402/x402_test.go`
- Create: `pkg/x402/mocks_test.go`
- Create: `pkg/x402/settlement_test.go`

## Verification
- After each phase:
  - `cd pkg && go test -v ./auth/... ./x402/...`
- Mutation tests:
  - `./scripts/mutation-test.sh pkg/auth`
  - `./scripts/mutation-test.sh pkg/x402`
- Full verification:
  - `make verify`

## Sign-off
- Status: Implemented in code and tests; ready for review.
