package store

import (
	"context"
	"database/sql"
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
