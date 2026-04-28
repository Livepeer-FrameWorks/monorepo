package bootstrap

import "fmt"

// Check is the read-only validation pass `commodore bootstrap --check` runs
// after parse. It exercises every reference resolvable from the file alone:
//
//   - account.kind is recognized;
//   - tenant refs match the QM ref grammar (alias resolution to a UUID is the
//     apply-path job, via Quartermaster gRPC);
//   - each user has a non-empty email + role + password.
//
// The cross-service alias→UUID call is intentionally not made here so --check
// stays offline and can be run from any host with the rendered file.
func Check(desired DesiredState) error {
	for _, acc := range desired.Accounts {
		if err := validateAccount(acc); err != nil {
			return err
		}
		if _, err := AliasFromRef(acc.Tenant.Ref); err != nil {
			return fmt.Errorf("account %s: %w", acc.Tenant.Ref, err)
		}
		for _, u := range acc.Users {
			if err := validateUser(u); err != nil {
				return fmt.Errorf("account %s: %w", acc.Tenant.Ref, err)
			}
		}
	}
	return nil
}
