// Declared-tenant reader for the deployed-target runner. It loads a gitops
// bootstrap overlay with the CLI's own strict loader, so the runner reads the
// same declaration that `frameworks cluster release apply` reconciles.
//
//	bootstrapdecl <bootstrap.yaml> <tenant alias>
//
// prints one tab-separated line: the first account user's email, the
// customer_billing tier, the operator grant's base fee and its usage waiver
// (empty fee and "false" when the row declares no grant). It exits 1 when the
// overlay does not parse or declares no customer account for the alias.
package main

import (
	"fmt"
	"os"

	"frameworks/cli/pkg/bootstrap"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: bootstrapdecl <bootstrap.yaml> <tenant alias>")
		os.Exit(2)
	}
	line, err := declaration(os.Args[1], os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, "bootstrapdecl:", err)
		os.Exit(1)
	}
	fmt.Println(line)
}

func declaration(path, alias string) (string, error) {
	overlay, err := bootstrap.LoadOverlay(path)
	if err != nil {
		return "", err
	}
	if overlay == nil {
		return "", fmt.Errorf("no overlay path")
	}
	ref := bootstrap.TenantRefAlias(alias)
	email := ""
	for _, account := range overlay.Accounts {
		if account.Tenant == ref && len(account.Users) > 0 {
			email = account.Users[0].Email
			break
		}
	}
	if email == "" {
		return "", fmt.Errorf("%s declares no account user for %s", path, ref.Ref)
	}
	tier, fee, waive := "", "", false
	for _, row := range overlay.Purser.CustomerBilling {
		if row.Tenant != ref {
			continue
		}
		tier = row.Tier
		if row.OperatorGrant != nil {
			waive = row.OperatorGrant.WaiveUsage
			if row.OperatorGrant.BaseFee != nil {
				fee = *row.OperatorGrant.BaseFee
			}
		}
		break
	}
	return fmt.Sprintf("%s\t%s\t%s\t%t", email, tier, fee, waive), nil
}
