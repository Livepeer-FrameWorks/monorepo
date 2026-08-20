package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"frameworks/api_gateway/internal/clients/clientstest"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func walletToolsCtx() context.Context {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, "tenant-1")
	return context.WithValue(ctx, ctxkeys.KeyUserID, "user-1")
}

func TestHandleRequestWalletChallenge(t *testing.T) {
	expiresAt := time.Unix(1_800_000_000, 0)
	var gotAddress string
	var gotChainID uint64
	commo := &clientstest.FakeCommodore{
		IssueWalletChallengeFn: func(_ context.Context, address string, chainID uint64) (*commodorepb.IssueWalletChallengeResponse, error) {
			gotAddress, gotChainID = address, chainID
			return &commodorepb.IssueWalletChallengeResponse{
				Message:   "sign exactly this",
				ExpiresAt: timestamppb.New(expiresAt),
			}, nil
		},
	}
	sc := clientstest.Clients(clientstest.WithCommodore(commo))
	res, out, err := handleRequestWalletChallenge(context.Background(), RequestWalletChallengeInput{
		Address: " 0xabc ", ChainID: 8453,
	}, sc, clientstest.DiscardLogger())
	if err != nil || res == nil || res.IsError {
		t.Fatalf("challenge = (%v, %v), want success", res, err)
	}
	result, ok := out.(WalletChallengeResult)
	if !ok || result.Message != "sign exactly this" || result.ExpiresAt != expiresAt.UTC().Format(time.RFC3339) {
		t.Fatalf("challenge result = %T %+v", out, out)
	}
	if gotAddress != "0xabc" || gotChainID != 8453 {
		t.Fatalf("challenge args = %q/%d", gotAddress, gotChainID)
	}
}

func TestHandleRequestWalletChallengeRejectsIncompleteInput(t *testing.T) {
	commo := &clientstest.FakeCommodore{}
	sc := clientstest.Clients(clientstest.WithCommodore(commo))
	res, _, err := handleRequestWalletChallenge(context.Background(), RequestWalletChallengeInput{}, sc, clientstest.DiscardLogger())
	if err != nil || res == nil || !res.IsError {
		t.Fatalf("invalid challenge request = (%v, %v), want tool error", res, err)
	}
	if commo.Calls != 0 {
		t.Fatalf("invalid request reached Commodore %d times", commo.Calls)
	}
}

func TestHandleListLinkedWallets(t *testing.T) {
	createdAt := time.Unix(1_700_000_000, 0)
	lastAuthAt := createdAt.Add(time.Hour)
	commo := &clientstest.FakeCommodore{
		ListWalletsFn: func(context.Context) (*commodorepb.ListWalletsResponse, error) {
			return &commodorepb.ListWalletsResponse{Wallets: []*commodorepb.WalletIdentity{{
				Id: "wallet-1", WalletAddress: "0xabc",
				CreatedAt: timestamppb.New(createdAt), LastAuthAt: timestamppb.New(lastAuthAt),
			}}}, nil
		},
	}
	sc := clientstest.Clients(clientstest.WithCommodore(commo))
	res, out, err := handleListLinkedWallets(walletToolsCtx(), sc, clientstest.DiscardLogger())
	if err != nil || res == nil || res.IsError {
		t.Fatalf("list wallets = (%v, %v), want success", res, err)
	}
	result, ok := out.(LinkedWalletsResult)
	if !ok || len(result.Wallets) != 1 || result.Wallets[0].ID != "wallet-1" || result.Wallets[0].LastAuthAt == "" {
		t.Fatalf("list result = %T %+v", out, out)
	}
}

func TestWalletManagementRequiresAuthenticatedUser(t *testing.T) {
	commo := &clientstest.FakeCommodore{}
	sc := clientstest.Clients(clientstest.WithCommodore(commo))
	tenantOnly := context.WithValue(context.Background(), ctxkeys.KeyTenantID, "tenant-1")

	if _, _, err := handleListLinkedWallets(tenantOnly, sc, clientstest.DiscardLogger()); err == nil {
		t.Fatal("list without user should return an auth error")
	}
	if _, _, err := handleLinkWallet(tenantOnly, LinkWalletInput{Address: "x", Message: "m", Signature: "s"}, sc, clientstest.DiscardLogger()); err == nil {
		t.Fatal("link without user should return an auth error")
	}
	if _, _, err := handleUnlinkWallet(tenantOnly, UnlinkWalletInput{WalletID: "wallet-1"}, sc, clientstest.DiscardLogger()); err == nil {
		t.Fatal("unlink without user should return an auth error")
	}
	if commo.Calls != 0 {
		t.Fatalf("auth failures reached Commodore %d times", commo.Calls)
	}
}

func TestHandleLinkAndUnlinkWallet(t *testing.T) {
	commo := &clientstest.FakeCommodore{
		LinkWalletFn: func(_ context.Context, address, message, signature string) (*commodorepb.WalletIdentity, error) {
			if address != "0xabc" || message != "message" || signature != "signature" {
				t.Fatalf("link args = %q/%q/%q", address, message, signature)
			}
			return &commodorepb.WalletIdentity{Id: "wallet-2", WalletAddress: address}, nil
		},
		UnlinkWalletFn: func(_ context.Context, walletID string) (*commodorepb.UnlinkWalletResponse, error) {
			if walletID != "wallet-2" {
				t.Fatalf("unlink wallet = %q", walletID)
			}
			return &commodorepb.UnlinkWalletResponse{Success: true}, nil
		},
	}
	sc := clientstest.Clients(clientstest.WithCommodore(commo))
	linkRes, linkOut, linkErr := handleLinkWallet(walletToolsCtx(), LinkWalletInput{
		Address: " 0xabc ", Message: " message ", Signature: " signature ",
	}, sc, clientstest.DiscardLogger())
	if linkErr != nil || linkRes.IsError {
		t.Fatalf("link = (%v, %v)", linkRes, linkErr)
	}
	linked, ok := linkOut.(WalletMutationResult)
	if !ok || !linked.Success || linked.Wallet == nil || linked.Wallet.ID != "wallet-2" {
		t.Fatalf("link result = %T %+v", linkOut, linkOut)
	}

	unlinkRes, unlinkOut, unlinkErr := handleUnlinkWallet(walletToolsCtx(), UnlinkWalletInput{WalletID: " wallet-2 "}, sc, clientstest.DiscardLogger())
	if unlinkErr != nil || unlinkRes.IsError {
		t.Fatalf("unlink = (%v, %v)", unlinkRes, unlinkErr)
	}
	unlinked, ok := unlinkOut.(WalletMutationResult)
	if !ok || !unlinked.Success || !strings.Contains(unlinked.Message, "unlinked") {
		t.Fatalf("unlink result = %T %+v", unlinkOut, unlinkOut)
	}
}
