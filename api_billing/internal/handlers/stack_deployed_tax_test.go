//go:build stack_verify

package handlers_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	x402pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/x402"
	"github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/signer/core/apitypes"
	_ "github.com/lib/pq"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// Only deployed RPCs create quotes, settle receipts and issue documents. The
// wallet authorizes an isolated external simulator; no real chain sees it.
func TestStackDeployedCryptoTax(t *testing.T) {
	tenant := os.Getenv("STACK_BILLING_TENANT_ID")
	if tenant == "" || os.Getenv("STACK_BILLING_DEPLOYED") != "1" {
		t.Skip("run through stack scenario 54")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+os.Getenv("SERVICE_TOKEN"))
	conn, err := grpc.NewClient("purser:19003", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := purserpb.NewX402ServiceClient(conn)
	required, err := client.GetPaymentRequirements(ctx, &purserpb.GetPaymentRequirementsRequest{TenantId: tenant, Resource: "mcp://stack-financials", ClientIp: "203.0.113.1"})
	if err != nil || len(required.GetAccepts()) == 0 {
		t.Fatalf("deployed quote: %v %+v", err, required)
	}
	q := required.Accepts[0]
	var extra map[string]interface{}
	if err = json.Unmarshal(q.ExtraJson, &extra); err != nil {
		t.Fatal(err)
	}
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, 32)
	if _, err = rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	nonceHex := "0x" + hex.EncodeToString(nonce)
	payer := crypto.PubkeyToAddress(key.PublicKey).Hex()
	now := time.Now().Unix()
	auth := map[string]interface{}{"from": payer, "to": q.PayTo, "value": q.Amount, "validAfter": strconv.FormatInt(now-60, 10), "validBefore": strconv.FormatInt(now+600, 10), "nonce": nonceHex}
	chain, ok := new(big.Int).SetString(strings.TrimPrefix(q.Network, "eip155:"), 10)
	if !ok {
		t.Fatalf("invalid advertised network %s", q.Network)
	}
	name, _ := extra["name"].(string)
	version, _ := extra["version"].(string)
	typed := apitypes.TypedData{Types: apitypes.Types{
		"EIP712Domain":              {{Name: "name", Type: "string"}, {Name: "version", Type: "string"}, {Name: "chainId", Type: "uint256"}, {Name: "verifyingContract", Type: "address"}},
		"TransferWithAuthorization": {{Name: "from", Type: "address"}, {Name: "to", Type: "address"}, {Name: "value", Type: "uint256"}, {Name: "validAfter", Type: "uint256"}, {Name: "validBefore", Type: "uint256"}, {Name: "nonce", Type: "bytes32"}},
	}, PrimaryType: "TransferWithAuthorization", Domain: apitypes.TypedDataDomain{Name: name, Version: version, ChainId: (*math.HexOrDecimal256)(chain), VerifyingContract: q.Asset}, Message: auth}
	digest, _, err := apitypes.TypedDataAndHash(typed)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := crypto.Sign(digest, key)
	if err != nil {
		t.Fatal(err)
	}
	signature := "0x" + hex.EncodeToString(sig)
	accepted := map[string]interface{}{"scheme": q.Scheme, "network": q.Network, "asset": q.Asset, "amount": q.Amount, "payTo": q.PayTo, "maxTimeoutSeconds": q.MaxTimeoutSeconds, "extra": extra}
	canonical, err := json.Marshal(map[string]interface{}{"x402Version": 2, "accepted": accepted, "payload": map[string]interface{}{"signature": signature, "authorization": auth}})
	if err != nil {
		t.Fatal(err)
	}
	payment := &x402pb.X402PaymentPayload{X402Version: 2, Scheme: q.Scheme, Network: q.Network, QuoteId: q.QuoteId, CanonicalPayloadJson: canonical,
		Accepted: &x402pb.X402AcceptedRequirements{Scheme: q.Scheme, Network: q.Network, Asset: q.Asset, Amount: q.Amount, PayTo: q.PayTo, MaxTimeoutSeconds: q.MaxTimeoutSeconds, ExtraJson: q.ExtraJson},
		Payload:  &x402pb.X402ExactPayload{Signature: signature, Authorization: &x402pb.X402Authorization{From: payer, To: q.PayTo, Value: q.Amount, ValidAfter: auth["validAfter"].(string), ValidBefore: auth["validBefore"].(string), Nonce: nonceHex}}}
	verified, err := client.VerifyX402Payment(ctx, &purserpb.VerifyX402PaymentRequest{TenantId: tenant, Payment: payment, ClientIp: "203.0.113.1"})
	if err != nil || !verified.GetValid() {
		t.Fatalf("deployed verify: %v %+v", err, verified)
	}
	settled, err := client.SettleX402Payment(ctx, &purserpb.SettleX402PaymentRequest{TenantId: tenant, Payment: payment, ClientIp: "203.0.113.1"})
	if err != nil || !settled.GetSuccess() || settled.GetInvoiceNumber() == "" {
		t.Fatalf("deployed settle: %v %+v", err, settled)
	}
	retry, err := client.SettleX402Payment(ctx, &purserpb.SettleX402PaymentRequest{TenantId: tenant, Payment: payment, ClientIp: "203.0.113.1"})
	if err != nil || !retry.GetSuccess() || retry.GetInvoiceNumber() != settled.InvoiceNumber {
		t.Fatalf("idempotent deployed settle: %v %+v", err, retry)
	}
	db, err := sql.Open("postgres", os.Getenv("STACK_BILLING_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	var net, vat, gross, credit int64
	var status string
	err = db.QueryRowContext(ctx, `SELECT count(*),min(net_eur_cents),min(vat_eur_cents),min(gross_eur_cents),min(tax_status) FROM purser.billing_invoices WHERE tenant_id=$1 AND source_reference_type='x402_payment' AND source_reference_id=$2`, tenant, settled.TxHash).Scan(&count, &net, &vat, &gross, &status)
	if err != nil {
		t.Fatal(err)
	}
	err = db.QueryRowContext(ctx, `SELECT sum(amount_cents) FROM purser.balance_transactions WHERE tenant_id=$1 AND reference_type='x402_payment' AND reference_id=(SELECT id FROM purser.x402_nonces WHERE tenant_id=$1 AND tx_hash=$2)`, tenant, settled.TxHash).Scan(&credit)
	if err != nil {
		t.Fatal(err)
	}
	rate, err := strconv.ParseInt(os.Getenv("STACK_BILLING_RATE_BPS"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || status != "ready" || net <= 0 || vat != (net*rate+5000)/10000 || gross != net+vat || credit != net || settled.CreditedCents != net {
		t.Fatalf("document/credit count=%d status=%s net=%d vat=%d gross=%d credit=%d settlement=%+v", count, status, net, vat, gross, credit, settled)
	}
	err = db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM purser.simplified_invoices WHERE tenant_id=$1 AND reference_id=$2)+(SELECT count(*) FROM purser.crypto_invoices WHERE tenant_id=$1 AND reference_id=$2)`, tenant, settled.TxHash).Scan(&count)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("duplicate SI/CI tax documents: %d", count)
	}
	t.Logf("deployed quote/verify/settle/retry: %s net=%d VAT=%d gross=%d credited=%d", settled.InvoiceNumber, net, vat, gross, credit)
}
