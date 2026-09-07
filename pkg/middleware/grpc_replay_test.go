package middleware

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestDelegatedJWTReplayInterceptorConsumesJTIOnce(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	expiresAt := time.Now().Add(time.Minute)
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "api_token")
	ctx = context.WithValue(ctx, ctxkeys.KeyJWTID, "jti-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyJWTExpiresAt, expiresAt)
	query := regexp.QuoteMeta("WITH claimed AS")
	mock.ExpectQuery(query).WithArgs("jti-1", expiresAt).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(query).WithArgs("jti-1", expiresAt).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

	interceptor := DelegatedJWTReplayInterceptor(db, "purser")
	called := 0
	handler := func(context.Context, any) (any, error) { called++; return "ok", nil }
	info := &grpc.UnaryServerInfo{FullMethod: "/purser.Test/Call"}
	if _, err := interceptor(ctx, nil, info, handler); err != nil {
		t.Fatal(err)
	}
	if _, err := interceptor(ctx, nil, info, handler); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("replay error = %v", err)
	}
	if called != 1 {
		t.Fatalf("handler calls = %d, want 1", called)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDelegatedJWTReplayInterceptorFailsClosedOnStoreError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "api_token")
	ctx = context.WithValue(ctx, ctxkeys.KeyJWTID, "jti-2")
	ctx = context.WithValue(ctx, ctxkeys.KeyJWTExpiresAt, time.Now().Add(time.Minute))
	mock.ExpectQuery(regexp.QuoteMeta("WITH claimed AS")).WillReturnError(context.DeadlineExceeded)
	_, err = DelegatedJWTReplayInterceptor(db, "quartermaster")(ctx, nil, &grpc.UnaryServerInfo{}, func(context.Context, any) (any, error) {
		t.Fatal("handler called while replay store was unavailable")
		return nil, nil
	})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("error = %v, want Unavailable", err)
	}
}

func TestDelegatedJWTReplayInterceptorIgnoresInteractiveJWT(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "jwt")
	called := false
	_, err := DelegatedJWTReplayInterceptor(nil, "commodore")(ctx, nil, &grpc.UnaryServerInfo{}, func(context.Context, any) (any, error) {
		called = true
		return nil, nil
	})
	if err != nil || !called {
		t.Fatalf("interactive request err=%v called=%t", err, called)
	}
}

func TestDelegatedJWTReplayInterceptorSupportsPeriscopeStore(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	expiresAt := time.Now().Add(time.Minute)
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "api_token")
	ctx = context.WithValue(ctx, ctxkeys.KeyJWTID, "periscope-jti")
	ctx = context.WithValue(ctx, ctxkeys.KeyJWTExpiresAt, expiresAt)
	mock.ExpectQuery(`INSERT INTO periscope\.delegated_jwt_replays`).WithArgs("periscope-jti", expiresAt).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	called := false
	_, err = DelegatedJWTReplayInterceptor(db, "periscope")(ctx, nil, &grpc.UnaryServerInfo{}, func(context.Context, any) (any, error) {
		called = true
		return nil, nil
	})
	if err != nil || !called {
		t.Fatalf("periscope replay store = called %v, err %v", called, err)
	}
}

func TestDelegatedJWTStreamReplayInterceptorConsumesJTIOnce(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	expiresAt := time.Now().Add(time.Minute)
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "api_token")
	ctx = context.WithValue(ctx, ctxkeys.KeyJWTID, "stream-jti")
	ctx = context.WithValue(ctx, ctxkeys.KeyJWTExpiresAt, expiresAt)
	query := regexp.QuoteMeta("WITH claimed AS")
	mock.ExpectQuery(query).WithArgs("stream-jti", expiresAt).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(query).WithArgs("stream-jti", expiresAt).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

	interceptor := DelegatedJWTStreamReplayInterceptor(db, "quartermaster")
	stream := &fakeServerStream{ctx: ctx}
	called := 0
	handler := func(any, grpc.ServerStream) error { called++; return nil }
	info := &grpc.StreamServerInfo{FullMethod: "/quartermaster.Test/Watch"}
	if err := interceptor(nil, stream, info, handler); err != nil {
		t.Fatal(err)
	}
	if err := interceptor(nil, stream, info, handler); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("stream replay error = %v", err)
	}
	if called != 1 {
		t.Fatalf("handler calls = %d, want 1", called)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
