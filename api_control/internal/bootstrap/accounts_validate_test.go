package bootstrap

import "testing"

func TestValidateAccount(t *testing.T) {
	tests := []struct {
		name    string
		account Account
		wantErr bool
	}{
		{
			name:    "valid_system_operator",
			account: Account{Kind: AccountSystemOperator, Tenant: TenantRef{Ref: "tenant-a"}},
		},
		{
			name:    "valid_customer",
			account: Account{Kind: AccountCustomer, Tenant: TenantRef{Ref: "tenant-b"}},
		},
		{
			name:    "missing_tenant_ref",
			account: Account{Kind: AccountCustomer},
			wantErr: true,
		},
		{
			name:    "unknown_kind",
			account: Account{Kind: "reseller", Tenant: TenantRef{Ref: "tenant-c"}},
			wantErr: true,
		},
		{
			name:    "empty_kind",
			account: Account{Tenant: TenantRef{Ref: "tenant-d"}},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAccount(tt.account)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateAccount() err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
