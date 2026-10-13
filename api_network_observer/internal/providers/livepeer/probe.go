package livepeer

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	obs "github.com/Livepeer-FrameWorks/monorepo/pkg/networkobservation"
	lp "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/livepeer_observer"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/restream"
	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

type Probe struct {
	Address           []byte
	Signature         []byte
	SkipTLSVerify     bool
	Timeout           time.Duration
	destinationPolicy restream.DestinationPolicy
}

func NewProbe(address, signature string, skipTLS bool, timeout time.Duration) (*Probe, error) {
	a, err := hex.DecodeString(strings.TrimPrefix(address, "0x"))
	if err != nil || len(a) != 20 {
		return nil, fmt.Errorf("invalid Livepeer discovery broadcaster address")
	}
	sig, err := hex.DecodeString(strings.TrimPrefix(signature, "0x"))
	if err != nil || len(sig) != 65 {
		return nil, fmt.Errorf("invalid Livepeer discovery broadcaster signature")
	}
	if sig[64] == 27 || sig[64] == 28 {
		sig[64] -= 27
	}
	if !crypto.ValidateSignatureValues(sig[64], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:64]), true) {
		return nil, fmt.Errorf("invalid Livepeer discovery signature values")
	}
	addr := common.BytesToAddress(a)
	publicKey, recoverErr := crypto.SigToPub(DiscoveryDigest(addr), sig)
	if addr == (common.Address{}) || recoverErr != nil || crypto.PubkeyToAddress(*publicKey) != addr {
		return nil, fmt.Errorf("livepeer discovery signature does not match the broadcaster address")
	}
	sig[64] += 27
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Probe{Address: a, Signature: sig, SkipTLSVerify: skipTLS, Timeout: timeout}, nil
}

// AllowPrivateNetworks lets the probe dial orchestrators inside the given
// CIDRs, which the destination policy otherwise refuses as non-public.
func (p *Probe) AllowPrivateNetworks(cidrs []string) error {
	for _, raw := range cidrs {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		_, network, err := net.ParseCIDR(raw)
		if err != nil {
			return fmt.Errorf("invalid private orchestrator network %q", raw)
		}
		p.destinationPolicy.AllowedCIDRs = append(p.destinationPolicy.AllowedCIDRs, network)
	}
	return nil
}

// Livepeer signs the Keccak hash of the checksummed address as an Ethereum message.
func DiscoveryDigest(address common.Address) []byte {
	return accounts.TextHash(crypto.Keccak256([]byte(address.Hex())))
}
func (p *Probe) Check(ctx context.Context, a obs.Assignment) obs.ProbeResult {
	start := time.Now().UTC()
	ctx, deadlineCancel := context.WithDeadline(ctx, a.Deadline)
	defer deadlineCancel()
	ctx, cancel := context.WithTimeout(ctx, p.Timeout)
	defer cancel()
	result := obs.ProbeResult{Assignment: &a, ID: uuid.NewString(), AssignmentID: a.ID, EndpointID: a.Endpoint.ID, Check: obs.Check{ObserverID: a.ObserverID, ObservedAt: start, Qualifying: true}}
	u, err := url.Parse(a.Endpoint.Resource.URI)
	ip := net.ParseIP(a.Endpoint.IP)
	if err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || ip == nil {
		result.Check.FailureKind = "invalid_endpoint"
		result.Check.Qualifying = false
		return result
	}
	if p.destinationPolicy.ValidateHost(u.Hostname()) != nil || p.destinationPolicy.ValidateIP(ip) != nil {
		result.Check.FailureKind = "endpoint_policy"
		result.Check.Qualifying = false
		return result
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == "http" {
			port = "80"
		} else {
			port = "443"
		}
	}
	target := u.Host
	if u.Port() == "" {
		target = net.JoinHostPort(u.Hostname(), port)
	}
	// No ServerName: gRPC refuses a credentials ServerName that differs from the
	// authority option, and derives SNI from the authority's hostname.
	var transport = credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: p.SkipTLSVerify})
	if u.Scheme == "http" {
		transport = insecure.NewCredentials()
	}
	// The authority remains the advertised host and port while the socket is pinned.
	dialer := func(ctx context.Context, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), port))
	}
	conn, err := grpc.NewClient("passthrough:///"+target, grpc.WithTransportCredentials(transport), grpc.WithAuthority(u.Host), grpc.WithContextDialer(dialer))
	if err != nil {
		result.Check.FailureKind = "observer_configuration"
		result.Check.Qualifying = false
		return result
	}
	defer conn.Close()
	var response lp.DiscoveryResponse
	var remote peer.Peer
	err = conn.Invoke(ctx, "/net.Orchestrator/GetOrchestrator", &lp.DiscoveryRequest{Address: p.Address, Sig: p.Signature, IgnoreCapacityCheck: true}, &response, grpc.Peer(&remote))
	result.Check.LatencyMS = uint32(time.Since(start).Milliseconds())
	if remote.Addr != nil {
		ip, _, e := net.SplitHostPort(remote.Addr.String())
		if e == nil {
			result.Check.ActualIP = ip
		}
	}
	if err != nil {
		result.Check.FailureKind = Classify(err)
		result.Check.Qualifying = obs.DiscoveryQualifies(false, true, result.Check.FailureKind)
		return result
	}
	if !ip.Equal(net.ParseIP(result.Check.ActualIP)) {
		result.Check.FailureKind = "ambiguous_attribution"
		result.Check.Qualifying = false
		return result
	}
	addr := "0x" + hex.EncodeToString(response.Address)
	if response.Transcoder == "" || !strings.EqualFold(addr, a.Endpoint.Resource.ID) {
		result.Check.FailureKind = "identity_mismatch"
		return result
	}
	result.Check.Success = true
	if response.PriceInfo != nil && response.PriceInfo.PixelsPerUnit > 0 {
		result.Quote = &obs.Quote{Numerator: strconv.FormatInt(response.PriceInfo.PricePerUnit, 10), Denominator: strconv.FormatInt(response.PriceInfo.PixelsPerUnit, 10), Currency: "wei", Unit: "pixel", ObservedAt: start}
	}
	if response.Capabilities != nil {
		for i, bits := range response.Capabilities.Bitstring {
			result.Capabilities = append(result.Capabilities, fmt.Sprintf("caps:%d:%016x", i, bits))
		}
	}
	hardware := []string{}
	for _, item := range response.Hardware {
		raw, e := protojson.Marshal(item)
		if e == nil {
			hardware = append(hardware, string(raw))
		}
	}
	result.Hardware = "[" + strings.Join(hardware, ",") + "]"
	result.AdvertisedURIs = response.Nodes
	return result
}
func Classify(err error) string {
	// Livepeer's discovery handler returns plain Go errors as gRPC Unknown.
	message := strings.ToLower(status.Convert(err).Message())
	if strings.Contains(message, "insufficient sender reserve") {
		return "observer_payment"
	}
	if strings.Contains(message, "orchestrator req sig check failed") || strings.Contains(message, "authentication failed") {
		return "authentication"
	}
	if strings.Contains(message, "orchestrator is at capacity") {
		return "capacity"
	}
	switch status.Code(err) {
	case codes.DeadlineExceeded:
		return "timeout"
	case codes.Unavailable:
		return "unavailable"
	case codes.ResourceExhausted:
		return "capacity"
	case codes.Unauthenticated, codes.PermissionDenied:
		return "authentication"
	case codes.Canceled:
		return "observer_cancelled"
	default:
		return "rpc"
	}
}
