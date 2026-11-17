//go:build stack_verify

package handlers_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	billingtax "frameworks/api_billing/internal/tax"
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
	var originalCurrency, units string
	var originalGross, quotedGross, quotedCredit int64
	var snapshot []byte
	err = db.QueryRowContext(ctx, `SELECT original_currency,original_amount_cents,eur_amount_cents,credit_amount_cents,fx_units_per_eur::text,tax_profile_snapshot FROM purser.x402_payment_quotes WHERE tenant_id=$1 AND id=$2`, tenant, q.QuoteId).
		Scan(&originalCurrency, &originalGross, &quotedGross, &quotedCredit, &units, &snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var retained billingtax.BillingProfile
	if err = json.Unmarshal(snapshot, &retained); err != nil || retained.TaxAssessment == nil || retained.TaxAssessment.Advance == nil {
		t.Fatalf("missing retained advance assessment: %v", err)
	}
	money := retained.TaxAssessment.Advance
	if err = checkStackCryptoTaxMoney(net, vat, gross, money, units, rate); err != nil {
		t.Fatal(err)
	}
	atomic := new(big.Int).Mul(big.NewInt(originalGross), big.NewInt(10000)).String()
	if originalCurrency != "USD" || originalCurrency != money.Currency || originalGross != money.GrossOriginal || quotedGross != money.GrossEUR || quotedCredit != money.NetEUR || q.Amount != atomic {
		t.Fatalf("accepted payment differs from retained advance: currency=%s original=%d EUR=%d credit=%d atomic=%s advance=%+v", originalCurrency, originalGross, quotedGross, quotedCredit, q.Amount, money)
	}
	if count != 1 || status != "ready" || net <= 0 || credit != net || settled.CreditedCents != net {
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

func checkStackCryptoTaxMoney(net, vat, gross int64, money *billingtax.AdvanceMoney, units string, rate int64) error {
	fxRate, valid := new(big.Rat).SetString(units)
	if money == nil || !valid || fxRate.Sign() <= 0 || rate < 0 || rate > 10000 {
		return fmt.Errorf("missing retained advance or valid conversion rate")
	}
	if money.Currency == "" || money.NetOriginal <= 0 || money.RateBPS != int(rate) {
		return fmt.Errorf("retained native currency, net or VAT rate differs")
	}
	round := func(numerator, denominator *big.Int) (int64, error) {
		twice := new(big.Int).Lsh(new(big.Int).Set(numerator), 1)
		twice.Add(twice, denominator)
		result := twice.Quo(twice, new(big.Int).Lsh(new(big.Int).Set(denominator), 1))
		if !result.IsInt64() {
			return 0, fmt.Errorf("retained money exceeds minor-unit range")
		}
		return result.Int64(), nil
	}
	wantNativeVAT, err := round(new(big.Int).Mul(big.NewInt(money.NetOriginal), big.NewInt(rate)), big.NewInt(10000))
	nativeGross := new(big.Int).Add(big.NewInt(money.NetOriginal), big.NewInt(wantNativeVAT))
	if err != nil || !nativeGross.IsInt64() || money.VATOriginal != wantNativeVAT || money.GrossOriginal != nativeGross.Int64() {
		return fmt.Errorf("retained native VAT or gross differs from its accepted rate")
	}
	toEUR := func(original int64) (int64, error) {
		return round(new(big.Int).Mul(big.NewInt(original), fxRate.Denom()), fxRate.Num())
	}
	wantGross, err := toEUR(money.GrossOriginal)
	if err != nil {
		return err
	}
	wantVAT, err := toEUR(money.VATOriginal)
	if err != nil {
		return err
	}
	// Native VAT is rounded before conversion. The EUR net retains the
	// conversion remainder so invoice cash, VAT and service credit reconcile.
	if money.GrossEUR != wantGross || money.VATEUR != wantVAT || money.NetEUR != wantGross-wantVAT || money.NetEUR <= 0 {
		return fmt.Errorf("retained EUR totals differ from the locked conversion")
	}
	if net != money.NetEUR || vat != money.VATEUR || gross != money.GrossEUR || gross != net+vat {
		return fmt.Errorf("invoice differs from retained native tax and FX: net=%d vat=%d gross=%d advance=%+v", net, vat, gross, money)
	}
	return nil
}
