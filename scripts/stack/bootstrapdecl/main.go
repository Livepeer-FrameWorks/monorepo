// Declared-tenant reader for the stack runners. It loads a bootstrap document
// with the CLI's own strict types, so a runner reads the same declaration that
// `frameworks cluster release apply` reconciles or, for the compose stack's
// fixture, that the service bootstrap commands consume.
//
//	bootstrapdecl [-rendered] <bootstrap.yaml> <tenant alias>
//
// prints one tab-separated line: the first account user's email, the
// customer_billing tier, the operator grant's base fee and its usage waiver
// (empty fee and "false" when the row declares no grant), and that user's
// declared wallet (empty when none). The file is a gitops overlay, whose
// passwords are secret references; with -rendered it is a rendered document
// (scripts/stack/bootstrap.yaml) and the line carries a sixth field, the user's
// plaintext password. It exits 1 when the file does not parse or declares no
// customer account for the alias.
package main

import (
	"bytes"
	"fmt"
	"os"

	"frameworks/cli/pkg/bootstrap"

	"gopkg.in/yaml.v3"
)

func main() {
	args := os.Args[1:]
	rendered := len(args) > 0 && args[0] == "-rendered"
	if rendered {
		args = args[1:]
	}
	if len(args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: bootstrapdecl [-rendered] <bootstrap.yaml> <tenant alias>")
		os.Exit(2)
	}
	var line string
	var err error
	if rendered {
		line, err = renderedDeclaration(args[0], args[1])
	} else {
		line, err = declaration(args[0], args[1])
	}
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
	email, wallet := "", ""
	for _, account := range overlay.Accounts {
		if account.Tenant == ref && len(account.Users) > 0 {
			email, wallet = account.Users[0].Email, account.Users[0].Wallet
			break
		}
	}
	if email == "" {
		return "", fmt.Errorf("%s declares no account user for %s", path, ref.Ref)
	}
	return fmt.Sprintf("%s\t%s\t%s", email, billing(overlay.Purser, ref), wallet), nil
}

func renderedDeclaration(path, alias string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var doc bootstrap.Rendered
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil {
		return "", fmt.Errorf("parse rendered %s: %w", path, err)
	}
	ref := bootstrap.TenantRefAlias(alias)
	var user *bootstrap.AccountUserRendered
	for _, account := range doc.Accounts {
		if account.Tenant == ref && len(account.Users) > 0 {
			user = &account.Users[0]
			break
		}
	}
	if user == nil || user.Email == "" {
		return "", fmt.Errorf("%s declares no account user for %s", path, ref.Ref)
	}
	return fmt.Sprintf("%s\t%s\t%s\t%s", user.Email, billing(doc.Purser, ref), user.Wallet, user.Password), nil
}

// billing is the tenant's customer_billing tier, grant base fee and usage
// waiver, tab-separated.
func billing(purser bootstrap.PurserSection, ref bootstrap.TenantRef) string {
	tier, fee, waive := "", "", false
	for _, row := range purser.CustomerBilling {
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
	return fmt.Sprintf("%s\t%s\t%t", tier, fee, waive)
}
