// Wallet login signer for the deployed-target runner. It reads a hex
// secp256k1 key from STACK_WALLET_KEY so the key never appears in argv.
//
//	walletsign address        prints the key's checksummed address
//	walletsign sign <message> prints the EIP-191 signature of the message
package main

import (
	"fmt"
	"os"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
)

func main() {
	key := os.Getenv("STACK_WALLET_KEY")
	if key == "" || len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: STACK_WALLET_KEY=<hex> walletsign address | sign <message>")
		os.Exit(2)
	}
	message := ""
	if os.Args[1] == "sign" && len(os.Args) == 3 {
		message = os.Args[2]
	} else if os.Args[1] != "address" || len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: STACK_WALLET_KEY=<hex> walletsign address | sign <message>")
		os.Exit(2)
	}
	address, signature, err := auth.SignWalletMessage(key, message)
	if err != nil {
		fmt.Fprintln(os.Stderr, "walletsign:", err)
		os.Exit(1)
	}
	if os.Args[1] == "address" {
		fmt.Println(address)
		return
	}
	fmt.Println(signature)
}
