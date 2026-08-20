package handlers

import (
	"context"
	"database/sql"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/btcsuite/btcd/btcutil/hdkeychain"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/sirupsen/logrus"
)

const (
	testXpub = "xpub6DZ3xpo1ixWwwNDQ7KFTamRVM46FQtgcDxsmAyeBpTHEo79E1n1LuWiZSMSRhqMQmrHaqJpek2TbtTzbAdNWJm9AhGdv7iJUpDjA6oJD84b"
)

func TestDeriveAddressFromXpub(t *testing.T) {
	tests := []struct {
		name      string
		xpub      string
		index     uint32
		expected  string
		wantError bool
	}{
		{
			name:     "index_zero",
			xpub:     testXpub,
			index:    0,
			expected: "0x022b971dff0c43305e691ded7a14367af19d6407",
		},
		{
			name:     "index_one",
			xpub:     testXpub,
			index:    1,
			expected: "0xbb7a182240010703dc81d6b1eff630ca02a169fd",
		},
		{
			name:      "invalid_xpub",
			xpub:      "not-a-key",
			index:     0,
			wantError: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			address, err := DeriveAddressFromXpub(test.xpub, test.index)
			if test.wantError {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if address != test.expected {
				t.Fatalf("expected %q, got %q", test.expected, address)
			}
		})
	}
}

func TestDeriveAddressFromXpubRejectsXprv(t *testing.T) {
	seed, err := hex.DecodeString("000102030405060708090a0b0c0d0e0f")
	if err != nil {
		t.Fatalf("failed to decode seed: %v", err)
	}
	master, err := hdkeychain.NewMaster(seed, &chaincfg.MainNetParams)
	if err != nil {
		t.Fatalf("failed to create master key: %v", err)
	}
	purpose, _ := master.Derive(hdkeychain.HardenedKeyStart + 44)
	coin, _ := purpose.Derive(hdkeychain.HardenedKeyStart + 60)
	account, _ := coin.Derive(hdkeychain.HardenedKeyStart + 0)
	change, _ := account.Derive(0)

	if _, err := DeriveAddressFromXpub(change.String(), 0); err == nil {
		t.Fatalf("expected error for xprv input")
	}
}

func TestRotateHDWalletPreservesNextIndex(t *testing.T) {
	seed, err := hex.DecodeString("101112131415161718191a1b1c1d1e1f")
	if err != nil {
		t.Fatal(err)
	}
	master, err := hdkeychain.NewMaster(seed, &chaincfg.MainNetParams)
	if err != nil {
		t.Fatal(err)
	}
	purpose, _ := master.Derive(hdkeychain.HardenedKeyStart + 44)
	coin, _ := purpose.Derive(hdkeychain.HardenedKeyStart + 60)
	account, _ := coin.Derive(hdkeychain.HardenedKeyStart)
	external, _ := account.Derive(0)
	newXpub, err := external.Neuter()
	if err != nil {
		t.Fatal(err)
	}

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT xpub, next_index FROM purser.hd_wallet_state").
		WillReturnRows(sqlmock.NewRows([]string{"xpub", "next_index"}).AddRow(testXpub, int64(42)))
	mock.ExpectExec("UPDATE purser.hd_wallet_state SET xpub").
		WithArgs(newXpub.String(), "mainnet").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	wallet := NewHDWallet(db, logrus.New())
	previous, next, changed, err := wallet.RotateHDWallet(context.Background(), newXpub.String(), "mainnet")
	if err != nil {
		t.Fatal(err)
	}
	if previous != testXpub || next != 42 || !changed {
		t.Fatalf("unexpected rotation result: previous=%q next=%d changed=%t", previous, next, changed)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateXpub(t *testing.T) {
	tests := []struct {
		name       string
		setupRow   func(*sqlmock.Sqlmock)
		wantErrMsg string
	}{
		{
			name: "missing_state",
			setupRow: func(mock *sqlmock.Sqlmock) {
				(*mock).ExpectQuery("SELECT xpub FROM purser.hd_wallet_state WHERE id = 1").
					WillReturnError(sql.ErrNoRows)
			},
			wantErrMsg: "hd_wallet_state not initialized",
		},
		{
			name: "invalid_xpub",
			setupRow: func(mock *sqlmock.Sqlmock) {
				rows := sqlmock.NewRows([]string{"xpub"}).AddRow("not-a-key")
				(*mock).ExpectQuery("SELECT xpub FROM purser.hd_wallet_state WHERE id = 1").
					WillReturnRows(rows)
			},
			wantErrMsg: "invalid xpub",
		},
		{
			name: "xprv_in_db",
			setupRow: func(mock *sqlmock.Sqlmock) {
				seed, err := hex.DecodeString("000102030405060708090a0b0c0d0e0f")
				if err != nil {
					t.Fatalf("failed to decode seed: %v", err)
				}
				master, err := hdkeychain.NewMaster(seed, &chaincfg.MainNetParams)
				if err != nil {
					t.Fatalf("failed to create master key: %v", err)
				}
				rows := sqlmock.NewRows([]string{"xpub"}).AddRow(master.String())
				(*mock).ExpectQuery("SELECT xpub FROM purser.hd_wallet_state WHERE id = 1").
					WillReturnRows(rows)
			},
			wantErrMsg: "CRITICAL: stored key is xprv",
		},
		{
			name: "valid_xpub",
			setupRow: func(mock *sqlmock.Sqlmock) {
				rows := sqlmock.NewRows([]string{"xpub"}).AddRow(testXpub)
				(*mock).ExpectQuery("SELECT xpub FROM purser.hd_wallet_state WHERE id = 1").
					WillReturnRows(rows)
			},
			wantErrMsg: "",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mockDB, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("failed to create sqlmock: %v", err)
			}
			defer mockDB.Close()

			test.setupRow(&mock)

			wallet := &HDWallet{db: mockDB, logger: logrus.New()}
			err = wallet.ValidateXpub(context.Background())
			if test.wantErrMsg == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			} else {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", test.wantErrMsg)
				}
				if !strings.Contains(err.Error(), test.wantErrMsg) {
					t.Fatalf("expected error containing %q, got %v", test.wantErrMsg, err)
				}
			}

			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet expectations: %v", err)
			}
		})
	}
}
