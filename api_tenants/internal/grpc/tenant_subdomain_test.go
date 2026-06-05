package grpc

import (
	"context"
	"regexp"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestGenerateAvailableTenantSubdomainUsesGeneratedSuffix(t *testing.T) {
	cases := []struct {
		name      string
		tenant    string
		wantRegex string
	}{
		{name: "normal name", tenant: "Acme Live", wantRegex: `^acme-live-[0-9a-f]{8}$`},
		{name: "reserved brand", tenant: "FrameWorks", wantRegex: `^tenant-[0-9a-f]{8}$`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			defer func() { _ = db.Close() }()

			server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
			mock.ExpectQuery(`SELECT cluster_id FROM quartermaster\.infrastructure_clusters`).
				WillReturnRows(sqlmock.NewRows([]string{"cluster_id"}))
			mock.ExpectQuery(`SELECT EXISTS \(`).
				WithArgs(sqlmock.AnyArg()).
				WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

			got, genErr := server.generateAvailableTenantSubdomain(context.Background(), tc.tenant)
			if genErr != nil {
				t.Fatalf("generateAvailableTenantSubdomain: %v", genErr)
			}
			if ok, _ := regexp.MatchString(tc.wantRegex, got); !ok {
				t.Fatalf("generated subdomain = %q, want pattern %s", got, tc.wantRegex)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("expectations: %v", err)
			}
		})
	}
}
