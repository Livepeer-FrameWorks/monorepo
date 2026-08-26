package quartermasterdb

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestGetMarketplaceClusterAllowsUnlistedDirectLookup(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now()
	mock.ExpectQuery("visibility IN \\('public', 'unlisted'\\)").WithArgs("unlisted-1").
		WillReturnRows(sqlmock.NewRows([]string{
			"cluster_id", "cluster_name", "short_description", "visibility", "requires_approval",
			"max_concurrent_streams", "max_concurrent_viewers", "owner_name", "subscription_status", "is_subscribed", "created_at",
		}).AddRow("unlisted-1", "Direct link", nil, "unlisted", false, 0, 0, "Provider", "", false, now))

	row, err := New(db).GetMarketplaceCluster(context.Background(), "unlisted-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if row.ClusterID != "unlisted-1" || row.Visibility != "unlisted" || row.IsSubscribed {
		t.Fatalf("unexpected unlisted lookup: %+v", row)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
