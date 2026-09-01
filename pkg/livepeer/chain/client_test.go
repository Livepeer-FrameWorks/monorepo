package chain

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func TestClientReadsBalanceAndSenderInfo(t *testing.T) {
	address := "0x1111111111111111111111111111111111111111"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "eth_getBalance":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0xde0b6b3a7640000"}`))
		case "eth_call":
			var call map[string]string
			if err := json.Unmarshal(req.Params[0], &call); err != nil {
				t.Fatal(err)
			}
			wantData := "0x" + hex.EncodeToString(append(append([]byte{}, getSenderInfoSelector...), common.LeftPadBytes(common.HexToAddress(address).Bytes(), 32)...))
			if call["to"] != TicketBrokerAddress || call["data"] != wantData {
				t.Fatalf("unexpected eth_call: %#v", call)
			}
			words := make([]byte, 128)
			copy(words[31:32], []byte{2})
			copy(words[63:64], []byte{7})
			copy(words[95:96], []byte{3})
			copy(words[127:128], []byte{1})
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x` + hex.EncodeToString(words) + `"}`))
		default:
			t.Fatalf("unexpected RPC method %s", req.Method)
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, server.Client())
	balance, err := client.ETHBalance(context.Background(), address)
	if err != nil || balance.Cmp(new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)) != 0 {
		t.Fatalf("balance=%v err=%v", balance, err)
	}
	info, err := client.GetSenderInfo(context.Background(), address)
	if err != nil {
		t.Fatal(err)
	}
	if info.Deposit.Int64() != 2 || info.WithdrawRound.Int64() != 7 || info.Reserve.Int64() != 3 || info.ClaimedInCurrentRound.Int64() != 1 {
		t.Fatalf("unexpected sender info: %+v", info)
	}
}

func TestClientRejectsRPCErrorAndShortSenderInfo(t *testing.T) {
	address := "0x1111111111111111111111111111111111111111"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x00"}`))
	}))
	defer server.Close()
	if _, err := NewClient(server.URL, server.Client()).GetSenderInfo(context.Background(), address); err == nil {
		t.Fatal("accepted truncated getSenderInfo response")
	}
}
