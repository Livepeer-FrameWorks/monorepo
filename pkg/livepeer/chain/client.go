package chain

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"

	"github.com/ethereum/go-ethereum/common"
)

const TicketBrokerAddress = "0xa8bB618B1520E284046F3dFc448851A1Ff26e41B"

var getSenderInfoSelector = common.Hex2Bytes("e7a47fa1")

type SenderInfo struct {
	Deposit               *big.Int
	WithdrawRound         *big.Int
	Reserve               *big.Int
	ClaimedInCurrentRound *big.Int
}

type Client struct {
	RPCURL     string
	HTTPClient *http.Client
}

func NewClient(rpcURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{RPCURL: strings.TrimSpace(rpcURL), HTTPClient: httpClient}
}

func (c *Client) ETHBalance(ctx context.Context, address string) (*big.Int, error) {
	if !common.IsHexAddress(address) {
		return nil, fmt.Errorf("invalid Ethereum address %q", address)
	}
	var result string
	if err := c.call(ctx, "eth_getBalance", []any{common.HexToAddress(address).Hex(), "latest"}, &result); err != nil {
		return nil, err
	}
	return decodeHexInt(result)
}

func (c *Client) GetSenderInfo(ctx context.Context, address string) (SenderInfo, error) {
	if !common.IsHexAddress(address) {
		return SenderInfo{}, fmt.Errorf("invalid Ethereum address %q", address)
	}
	callData := append(append([]byte{}, getSenderInfoSelector...), common.LeftPadBytes(common.HexToAddress(address).Bytes(), 32)...)
	var result string
	if err := c.call(ctx, "eth_call", []any{map[string]string{
		"to": TicketBrokerAddress, "data": "0x" + hex.EncodeToString(callData),
	}, "latest"}, &result); err != nil {
		return SenderInfo{}, err
	}
	data := common.FromHex(result)
	if len(data) < 128 {
		return SenderInfo{}, fmt.Errorf("TicketBroker getSenderInfo returned %d bytes, want at least 128", len(data))
	}
	return SenderInfo{
		Deposit: new(big.Int).SetBytes(data[0:32]), WithdrawRound: new(big.Int).SetBytes(data[32:64]),
		Reserve: new(big.Int).SetBytes(data[64:96]), ClaimedInCurrentRound: new(big.Int).SetBytes(data[96:128]),
	}, nil
}

func (c *Client) call(ctx context.Context, method string, params []any, out any) error {
	if c == nil || strings.TrimSpace(c.RPCURL) == "" {
		return errors.New("arbitrum RPC URL is required")
	}
	payload, err := json.Marshal(struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Method  string `json:"method"`
		Params  []any  `json:"params"`
	}{JSONRPC: "2.0", ID: 1, Method: method, Params: params})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.RPCURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("ethereum RPC returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("decode Ethereum RPC response: %w", err)
	}
	if envelope.Error != nil {
		return fmt.Errorf("ethereum RPC %s failed (%d): %s", method, envelope.Error.Code, envelope.Error.Message)
	}
	if len(envelope.Result) == 0 || string(envelope.Result) == "null" {
		return fmt.Errorf("ethereum RPC %s returned no result", method)
	}
	if err := json.Unmarshal(envelope.Result, out); err != nil {
		return fmt.Errorf("decode Ethereum RPC %s result: %w", method, err)
	}
	return nil
}

func decodeHexInt(value string) (*big.Int, error) {
	value = strings.TrimSpace(strings.TrimPrefix(value, "0x"))
	if value == "" {
		return nil, errors.New("empty hexadecimal integer")
	}
	n := new(big.Int)
	if _, ok := n.SetString(value, 16); !ok {
		return nil, fmt.Errorf("invalid hexadecimal integer %q", value)
	}
	return n, nil
}

func WeiToETH(value *big.Int) string {
	if value == nil {
		return "0"
	}
	r := new(big.Rat).SetFrac(value, new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
	return strings.TrimRight(strings.TrimRight(r.FloatString(18), "0"), ".")
}
