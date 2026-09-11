package federation

import (
	"context"
	"database/sql"
	"io"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/sirupsen/logrus"
)

func relayTestLogger() logrus.FieldLogger {
	l := logrus.New()
	l.SetOutput(io.Discard)
	l.SetLevel(logrus.PanicLevel)
	return l
}

// The minted grant authorizes exactly the paths in the returned URLs, and the
// origin's own edge validates it. A path built from a fabricated extension
// would either serve the wrong bytes or 404, so an unusable input must fail
// closed rather than guess.
func TestPeerRelayArtifactPath(t *testing.T) {
	cases := []struct {
		name         string
		artifactType string
		hash         string
		format       string
		stream       string
		want         string
	}{
		{"vod is flat", "vod", "hash-1", "mp4", "", "/internal/artifact/vod/hash-1.mp4"},
		{"chapter shares the vod route", "chapter", "hash-2", "mkv", "", "/internal/artifact/vod/hash-2.mkv"},
		{"empty type is treated as vod", "", "hash-3", "webm", "", "/internal/artifact/vod/hash-3.webm"},
		{"type is case and space insensitive", "  VOD ", "hash-4", "mp4", "", "/internal/artifact/vod/hash-4.mp4"},
		{"clip nests the stream", "clip", "hash-5", "mp4", "live+abc", "/internal/artifact/clip/live+abc/hash-5.mp4"},
		{"leading dot on format is stripped", "vod", "hash-6", ".mp4", "", "/internal/artifact/vod/hash-6.mp4"},
		{"empty format fails closed", "vod", "hash-7", "", "", ""},
		{"whitespace format fails closed", "vod", "hash-8", "   ", "", ""},
		{"clip without a stream fails closed", "clip", "hash-9", "mp4", "", ""},
		{"clip with a blank stream fails closed", "clip", "hash-10", "mp4", "   ", ""},
		{"unsupported type fails closed", "thumbnail", "hash-11", "jpg", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := peerRelayArtifactPath(tc.artifactType, tc.hash, tc.format, tc.stream)
			if got != tc.want {
				t.Fatalf("peerRelayArtifactPath(%q, %q, %q, %q) = %q, want %q",
					tc.artifactType, tc.hash, tc.format, tc.stream, got, tc.want)
			}
		})
	}
}

// A stream name that is not URL-safe must produce the same bytes here as the
// local relay-URL builder produces, or the minted grant covers a path the peer
// never requests and the pull is refused.
func TestPeerRelayArtifactPathEscapesStreamSegment(t *testing.T) {
	got := peerRelayArtifactPath("clip", "hash", "mp4", "live+a b/c")
	segments := strings.Split(strings.TrimPrefix(got, "/internal/artifact/clip/"), "/")
	// A raw slash in the stream name would split into an extra path segment and
	// misroute; the escaped form keeps it inside one segment.
	if len(segments) != 2 {
		t.Fatalf("stream segment was not escaped, so the path gained a route boundary: %q", got)
	}
	if got != "/internal/artifact/clip/live+a%20b%2Fc/hash.mp4" {
		t.Fatalf("escaped path = %q", got)
	}
}

func originRows(nodeID, baseURL string) *sqlmock.Rows {
	return sqlmock.NewRows([]string{"node_id", "base_url"}).AddRow(nodeID, baseURL)
}

// The happy path mints one grant covering both the media URL and its .dtsh
// sidecar, and addresses the origin's relay rather than its playback base.
func TestMaybePeerRelayMintsGrantForOriginRelay(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT an.node_id")).
		WithArgs("art-1").
		WillReturnRows(originRows("node-origin", "https://edge-1.example.com/view"))

	srv := NewFederationServer(FederationServerConfig{Logger: testLogger(), ClusterID: "cluster-a", DB: db})
	got, ok := srv.maybePeerRelay(context.Background(), "art-1", "mp4", "vod", "", "cluster-b", relayTestLogger())
	if !ok {
		t.Fatal("origin row with a usable base URL did not produce a relay")
	}
	// /view is the playback base and routes to Mist; the relay lives at the host root.
	if got.url != "https://edge-1.example.com/internal/artifact/vod/art-1.mp4" {
		t.Fatalf("relay URL = %q", got.url)
	}
	if got.dtshURL != got.url+".dtsh" {
		t.Fatalf("dtsh URL = %q, want the media URL plus .dtsh", got.dtshURL)
	}
	// The grant is what the origin's own edge validates when the requesting
	// cluster pulls; without one the URLs are unusable. That it covers both the
	// media path and its sidecar is asserted where the store is visible, in
	// control.TestRelayGrantMintLookupAndAuthorize.
	if got.grantID == "" {
		t.Fatal("no grant was minted, so the origin edge would refuse the pull")
	}
}

// Each refusal returns no grant at all. Minting one for a path the origin will
// not serve leaves a live credential behind for bytes that do not exist.
func TestMaybePeerRelayRefusalsMintNothing(t *testing.T) {
	cases := []struct {
		name    string
		hash    string
		format  string
		artType string
		stream  string
		arrange func(mock sqlmock.Sqlmock)
	}{
		{
			name: "no live origin row", hash: "art-none", format: "mp4", artType: "vod",
			arrange: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta("SELECT an.node_id")).
					WithArgs("art-none").WillReturnError(sql.ErrNoRows)
			},
		},
		{
			name: "origin has no addressable base url", hash: "art-nourl", format: "mp4", artType: "vod",
			arrange: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta("SELECT an.node_id")).
					WithArgs("art-nourl").WillReturnRows(originRows("node-origin", ""))
			},
		},
		{
			name: "base url is not a usable origin", hash: "art-badurl", format: "mp4", artType: "vod",
			arrange: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta("SELECT an.node_id")).
					WithArgs("art-badurl").WillReturnRows(originRows("node-origin", "edge-1.example.com/view"))
			},
		},
		{
			name: "unknown format", hash: "art-nofmt", format: "", artType: "vod",
			arrange: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta("SELECT an.node_id")).
					WithArgs("art-nofmt").WillReturnRows(originRows("node-origin", "https://edge-1.example.com"))
			},
		},
		{
			name: "clip without a stream", hash: "art-noclip", format: "mp4", artType: "clip",
			arrange: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta("SELECT an.node_id")).
					WithArgs("art-noclip").WillReturnRows(originRows("node-origin", "https://edge-1.example.com"))
			},
		},
		{
			name: "lookup failed", hash: "art-err", format: "mp4", artType: "vod",
			arrange: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta("SELECT an.node_id")).
					WithArgs("art-err").WillReturnError(sql.ErrConnDone)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			defer db.Close()
			tc.arrange(mock)
			srv := NewFederationServer(FederationServerConfig{Logger: testLogger(), ClusterID: "cluster-a", DB: db})
			got, ok := srv.maybePeerRelay(context.Background(), tc.hash, tc.format, tc.artType, tc.stream, "cluster-b", relayTestLogger())
			if ok {
				t.Fatalf("refusal case produced a relay: %+v", got)
			}
			if got.grantID != "" {
				t.Fatalf("a refused relay minted grant %q", got.grantID)
			}
		})
	}
}

// Without a database there is nothing to look up, and the caller must fall
// through to S3 rather than receive an unusable relay.
func TestMaybePeerRelayWithoutDatabase(t *testing.T) {
	srv := NewFederationServer(FederationServerConfig{Logger: testLogger(), ClusterID: "cluster-a"})
	if _, ok := srv.maybePeerRelay(context.Background(), "art-1", "mp4", "vod", "", "cluster-b", relayTestLogger()); ok {
		t.Fatal("relay returned without a database")
	}
}
