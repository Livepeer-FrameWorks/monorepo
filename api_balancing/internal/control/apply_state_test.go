package control

import (
	"context"
	"database/sql"
	"math"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/proto"
)

func TestSeedVersionMonotonicPerNode(t *testing.T) {
	c := newSeedVersionCounter()
	if v := c.next("a"); v != 1 {
		t.Fatalf("a#1 = %d, want 1", v)
	}
	if v := c.next("a"); v != 2 {
		t.Fatalf("a#2 = %d, want 2", v)
	}
	if v := c.next("b"); v != 1 {
		t.Fatalf("b#1 = %d, want 1 (independent per node)", v)
	}
	if v := c.next("a"); v != 3 {
		t.Fatalf("a#3 = %d, want 3", v)
	}
}

func TestAllocateAndPersistConfigSeedUsesOneTransaction(t *testing.T) {
	testDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer testDB.Close()
	previous := GetDB()
	SetDB(testDB)
	t.Cleanup(func() { SetDB(previous) })

	seed := &ipcpb.ConfigSeed{NodeId: "node-1", FoghornBalancerBase: "https://foghorn.example"}
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO foghorn.node_config_seeds")).
		WithArgs("node-1").WillReturnRows(sqlmock.NewRows([]string{"version_counter"}).AddRow(int64(9)))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COALESCE(seed_version, 0)::bigint AS seed_version, seed_payload")).
		WithArgs("node-1").WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(regexp.QuoteMeta("UPDATE foghorn.node_config_seeds")).
		WithArgs(int64(9), sqlmock.AnyArg(), "node-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	version, err := allocateAndPersistConfigSeed(context.Background(), "node-1", seed)
	if err != nil {
		t.Fatal(err)
	}
	if version != 9 || seed.GetSeedVersion() != 9 {
		t.Fatalf("version = %d, seed_version = %d", version, seed.GetSeedVersion())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAllocateAndPersistConfigSeedRetainsMonotonicFloorWhenPayloadWriteFails(t *testing.T) {
	testDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer testDB.Close()
	previous := GetDB()
	SetDB(testDB)
	t.Cleanup(func() { SetDB(previous) })

	seed := &ipcpb.ConfigSeed{NodeId: "node-rollback"}
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO foghorn.node_config_seeds")).
		WithArgs("node-rollback").WillReturnRows(sqlmock.NewRows([]string{"version_counter"}).AddRow(int64(12)))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COALESCE(seed_version, 0)::bigint AS seed_version, seed_payload")).
		WithArgs("node-rollback").WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(regexp.QuoteMeta("UPDATE foghorn.node_config_seeds")).
		WithArgs(int64(12), sqlmock.AnyArg(), "node-rollback").WillReturnError(sql.ErrConnDone)
	mock.ExpectRollback()

	if _, err := allocateAndPersistConfigSeed(context.Background(), "node-rollback", seed); err == nil {
		t.Fatal("payload write failure must fail the allocation transaction")
	}
	if seed.GetSeedVersion() != 0 {
		t.Fatalf("failed transaction mutated caller seed version to %d", seed.GetSeedVersion())
	}
	if floor := seedVersions.current("node-rollback"); floor != 12 {
		t.Fatalf("in-memory fallback floor = %d, want 12", floor)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareAndPersistConfigSeedMutatesLatestPayloadUnderVersionLock(t *testing.T) {
	testDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer testDB.Close()
	previous := GetDB()
	SetDB(testDB)
	t.Cleanup(func() { SetDB(previous) })

	latest := &ipcpb.ConfigSeed{
		NodeId: "node-latest", SeedVersion: 6,
		TlsBundles: []*ipcpb.TLSCertBundle{{BundleId: "tenant:fresh", CertPem: "fresh-cert"}},
	}
	encoded, err := proto.MarshalOptions{Deterministic: true}.Marshal(latest)
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO foghorn.node_config_seeds")).
		WithArgs("node-latest").WillReturnRows(sqlmock.NewRows([]string{"version_counter"}).AddRow(int64(7)))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COALESCE(seed_version, 0)::bigint AS seed_version, seed_payload")).
		WithArgs("node-latest").WillReturnRows(sqlmock.NewRows([]string{"seed_version", "seed_payload"}).AddRow(int64(6), encoded))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE foghorn.node_config_seeds")).
		WithArgs(int64(7), sqlmock.AnyArg(), "node-latest").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	persisted, err := prepareAndPersistConfigSeed(context.Background(), "node-latest", func(lockedLatest *ipcpb.ConfigSeed) (*ipcpb.ConfigSeed, error) {
		if lockedLatest == nil || lockedLatest.GetSeedVersion() != 6 {
			t.Fatalf("producer did not receive latest locked payload: %+v", lockedLatest)
		}
		candidate := proto.CloneOf(lockedLatest)
		candidate.FoghornBalancerBase = "https://new-balancer.example"
		return candidate, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if persisted.GetSeedVersion() != 7 || persisted.GetFoghornBalancerBase() != "https://new-balancer.example" ||
		len(persisted.GetTlsBundles()) != 1 || persisted.GetTlsBundles()[0].GetCertPem() != "fresh-cert" {
		t.Fatalf("locked read-modify-write lost latest fields: %+v", persisted)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCanRetainAppliedConfigSeedRequiresAuthenticatedReconnect(t *testing.T) {
	for _, tc := range []struct {
		name                string
		fingerprintResolved bool
		version             uint64
		want                bool
	}{
		{name: "known node with applied seed", fingerprintResolved: true, version: 41, want: true},
		{name: "fresh enrollment", fingerprintResolved: false, version: 41, want: false},
		{name: "known node without seed", fingerprintResolved: true, version: 0, want: false},
		{name: "poisoned counter", fingerprintResolved: true, version: uint64(math.MaxInt64/2) + 1, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := canRetainAppliedConfigSeed(tc.fingerprintResolved, tc.version); got != tc.want {
				t.Fatalf("canRetainAppliedConfigSeed() = %v, want %v", got, tc.want)
			}
		})
	}
}
