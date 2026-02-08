package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestStoreGetCertificateTenantScoping(t *testing.T) {
	now := time.Now()

	t.Run("platform", func(t *testing.T) {
		db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer db.Close()

		rows := sqlmock.NewRows([]string{"id", "tenant_id", "domain", "cert_pem", "key_pem", "expires_at", "created_at", "updated_at"}).
			AddRow("cert-1", nil, "platform.example.com", "cert", "key", now, now, now)

		mock.ExpectQuery(`FROM navigator\.certificates\s+WHERE tenant_id IS NULL AND domain = \$1`).
			WithArgs("platform.example.com").
			WillReturnRows(rows)

		store := NewStore(db)
		cert, err := store.GetCertificate(context.Background(), "", "platform.example.com")
		if err != nil {
			t.Fatalf("GetCertificate: %v", err)
		}
		if cert.Domain != "platform.example.com" {
			t.Fatalf("unexpected domain: %s", cert.Domain)
		}
		if cert.TenantID.Valid {
			t.Fatalf("expected platform certificate")
		}

		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("expectations: %v", err)
		}
	})

	t.Run("tenant", func(t *testing.T) {
		db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer db.Close()

		rows := sqlmock.NewRows([]string{"id", "tenant_id", "domain", "cert_pem", "key_pem", "expires_at", "created_at", "updated_at"}).
			AddRow("cert-2", "tenant-123", "tenant.example.com", "cert", "key", now, now, now)

		mock.ExpectQuery(`FROM navigator\.certificates\s+WHERE tenant_id = \$1 AND domain = \$2`).
			WithArgs("tenant-123", "tenant.example.com").
			WillReturnRows(rows)

		store := NewStore(db)
		cert, err := store.GetCertificate(context.Background(), "tenant-123", "tenant.example.com")
		if err != nil {
			t.Fatalf("GetCertificate: %v", err)
		}
		if cert.Domain != "tenant.example.com" {
			t.Fatalf("unexpected domain: %s", cert.Domain)
		}
		if !cert.TenantID.Valid || cert.TenantID.String != "tenant-123" {
			t.Fatalf("unexpected tenant id: %#v", cert.TenantID)
		}

		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("expectations: %v", err)
		}
	})
}

func TestListExpiringCertificates(t *testing.T) {
	now := time.Now()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{"id", "tenant_id", "domain", "cert_pem", "key_pem", "expires_at", "created_at", "updated_at"}).
		AddRow("cert-1", nil, "example.com", "cert", "key", now.Add(12*time.Hour), now, now).
		AddRow("cert-2", "tenant-1", "t1.example.com", "cert2", "key2", now.Add(23*time.Hour), now, now)

	mock.ExpectQuery(`FROM navigator\.certificates\s+WHERE expires_at < \$1\s+ORDER BY expires_at ASC`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(rows)

	store := NewStore(db)
	certs, err := store.ListExpiringCertificates(context.Background(), 24*time.Hour)
	if err != nil {
		t.Fatalf("ListExpiringCertificates: %v", err)
	}
	if len(certs) != 2 {
		t.Fatalf("expected 2 certificates, got %d", len(certs))
	}
	if certs[0].Domain != "example.com" {
		t.Fatalf("unexpected domain: %s", certs[0].Domain)
	}
	if certs[1].Domain != "t1.example.com" {
		t.Fatalf("unexpected domain: %s", certs[1].Domain)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestListExpiringCertificatesEmpty(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{"id", "tenant_id", "domain", "cert_pem", "key_pem", "expires_at", "created_at", "updated_at"})

	mock.ExpectQuery(`FROM navigator\.certificates\s+WHERE expires_at < \$1\s+ORDER BY expires_at ASC`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(rows)

	store := NewStore(db)
	certs, err := store.ListExpiringCertificates(context.Background(), 24*time.Hour)
	if err != nil {
		t.Fatalf("ListExpiringCertificates: %v", err)
	}
	if len(certs) != 0 {
		t.Fatalf("expected 0 certificates, got %d", len(certs))
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestListCertificatesForTenantPlatform(t *testing.T) {
	now := time.Now()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{"id", "tenant_id", "domain", "cert_pem", "key_pem", "expires_at", "created_at", "updated_at"}).
		AddRow("cert-1", nil, "platform.example.com", "cert", "key", now, now, now)

	mock.ExpectQuery(`FROM navigator\.certificates\s+WHERE tenant_id IS NULL\s+ORDER BY domain`).
		WillReturnRows(rows)

	store := NewStore(db)
	certs, err := store.ListCertificatesForTenant(context.Background(), "")
	if err != nil {
		t.Fatalf("ListCertificatesForTenant: %v", err)
	}
	if len(certs) != 1 {
		t.Fatalf("expected 1 certificate, got %d", len(certs))
	}
	if certs[0].TenantID.Valid {
		t.Fatalf("expected platform certificate (tenant_id NULL)")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestListCertificatesForTenantSpecific(t *testing.T) {
	now := time.Now()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{"id", "tenant_id", "domain", "cert_pem", "key_pem", "expires_at", "created_at", "updated_at"}).
		AddRow("cert-2", "t1", "t1.example.com", "cert", "key", now, now, now)

	mock.ExpectQuery(`FROM navigator\.certificates\s+WHERE tenant_id = \$1\s+ORDER BY domain`).
		WithArgs("t1").
		WillReturnRows(rows)

	store := NewStore(db)
	certs, err := store.ListCertificatesForTenant(context.Background(), "t1")
	if err != nil {
		t.Fatalf("ListCertificatesForTenant: %v", err)
	}
	if len(certs) != 1 {
		t.Fatalf("expected 1 certificate, got %d", len(certs))
	}
	if !certs[0].TenantID.Valid || certs[0].TenantID.String != "t1" {
		t.Fatalf("unexpected tenant id: %#v", certs[0].TenantID)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestGetACMEAccountNotFound(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(`FROM navigator\.acme_accounts\s+WHERE tenant_id IS NULL AND email = \$1`).
		WithArgs("admin@example.com").
		WillReturnError(sql.ErrNoRows)

	store := NewStore(db)
	_, err = store.GetACMEAccount(context.Background(), "", "admin@example.com")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestSaveACMEAccount(t *testing.T) {
	now := time.Now()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	acc := &ACMEAccount{
		Email:         "admin@example.com",
		Registration:  `{"status":"valid"}`,
		PrivateKeyPEM: "private-key-pem",
	}

	mock.ExpectQuery(`INSERT INTO navigator\.acme_accounts \(tenant_id, email, registration_json, private_key_pem\)`).
		WithArgs("t1", acc.Email, acc.Registration, acc.PrivateKeyPEM).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "created_at"}).AddRow("acme-1", sql.NullString{String: "t1", Valid: true}, now))

	store := NewStore(db)
	if err := store.SaveACMEAccount(context.Background(), "t1", acc); err != nil {
		t.Fatalf("SaveACMEAccount: %v", err)
	}
	if acc.ID != "acme-1" {
		t.Fatalf("unexpected acme account id: %s", acc.ID)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestStoreSaveCertificateUpsert(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	now := time.Now()
	cert := &Certificate{
		Domain:    "tenant.example.com",
		CertPEM:   "cert",
		KeyPEM:    "key",
		ExpiresAt: now,
	}

	mock.ExpectQuery(`INSERT INTO navigator\.certificates \(tenant_id, domain, cert_pem, key_pem, expires_at, updated_at\)`).
		WithArgs("tenant-123", cert.Domain, cert.CertPEM, cert.KeyPEM, cert.ExpiresAt).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "created_at"}).AddRow("cert-1", sql.NullString{String: "tenant-123", Valid: true}, now))

	store := NewStore(db)
	if err := store.SaveCertificate(context.Background(), "tenant-123", cert); err != nil {
		t.Fatalf("SaveCertificate: %v", err)
	}
	if cert.ID != "cert-1" {
		t.Fatalf("unexpected cert id: %s", cert.ID)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
