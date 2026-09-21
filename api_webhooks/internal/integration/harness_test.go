//go:build schema_verify

package integration

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_webhooks/internal/delivery"
	"frameworks/api_webhooks/internal/ledger"

	fieldcrypt "github.com/Livepeer-FrameWorks/monorepo/pkg/crypto"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	publicv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/public/v1"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/restream"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"

	"github.com/google/uuid"
)

// startPostgres runs the pinned PostgreSQL image, applies bosun.sql through
// the production driver configuration, and removes the container at the end
// of the test.
func startPostgres(t *testing.T) *sql.DB {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-bosun-realpg-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = dockerpg.CLI("rm", "-fv", name) })
	image, err := dockerpg.PostgresImage()
	if err != nil {
		t.Fatal(err)
	}
	if output, err := dockerpg.Run("run", "-d", "--name", name, "-P", "-e", "POSTGRES_PASSWORD=harness", image); err != nil {
		t.Fatalf("docker run: %v\n%s", err, output)
	}
	port, err := dockerpg.DiscoverPublishedHostPort(name, "5432/tcp")
	if err != nil {
		t.Fatal(err)
	}
	return connectWithBaseline(t, fmt.Sprintf("postgres://postgres:harness@127.0.0.1:%s/postgres?sslmode=disable", port), name, 90*time.Second)
}

// startYugabyte opens a fresh database on the suite-owned Yugabyte fixture
// when the contract harness provides one, and otherwise runs the pinned
// Yugabyte image for this test. Either way the test talks to it through the
// production driver configuration with bosun.sql applied.
func startYugabyte(t *testing.T) *sql.DB {
	t.Helper()
	if fixture, ok := dockerpg.OpenSharedYugabyteDatabase(t, "bosun"); ok {
		var name string
		if err := fixture.QueryRow(`SELECT current_database()`).Scan(&name); err != nil {
			t.Fatal(err)
		}
		dsn, err := url.Parse(os.Getenv(dockerpg.SharedYugabyteDSNEnv))
		if err != nil {
			t.Fatal(err)
		}
		dsn.Path = "/" + name
		return connectWithBaseline(t, dsn.String(), os.Getenv(dockerpg.SharedYugabyteContainerEnv), 90*time.Second)
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-bosun-yb-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = dockerpg.CLI("rm", "-fv", name) })
	image, err := dockerpg.YugabyteImage()
	if err != nil {
		t.Fatal(err)
	}
	if output, err := dockerpg.Run("run", "-d", "--name", name, "-P", "--hostname", name, image, "bash", "-c", `exec bin/yugabyted start --background=false --advertise_address="$(hostname -i)" --tserver_flags=yb_enable_read_committed_isolation=false`); err != nil {
		t.Fatalf("docker run: %v\n%s", err, output)
	}
	port, err := dockerpg.DiscoverPublishedHostPort(name, "5433/tcp")
	if err != nil {
		t.Fatal(err)
	}
	return connectWithBaseline(t, fmt.Sprintf("postgres://yugabyte@127.0.0.1:%s/yugabyte?sslmode=disable", port), name, 3*time.Minute)
}

// connectWithBaseline connects through database.Connect, the configuration
// main.go uses, and applies bosun.sql.
func connectWithBaseline(t *testing.T, dsn, container string, budget time.Duration) *sql.DB {
	t.Helper()
	cfg := database.DefaultConfig()
	cfg.URL = dsn
	var db *sql.DB
	var err error
	deadline := time.Now().Add(budget)
	for {
		db, err = database.Connect(cfg, logging.NewLogger())
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("connect: %v", err)
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := dockerpg.WaitReadyFor(db, container, budget); err != nil {
		t.Fatal(err)
	}
	schema, err := dbsql.Content.ReadFile("schema/bosun.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatalf("apply bosun baseline: %v", err)
	}
	return db
}

func newStore(t *testing.T, db *sql.DB) *ledger.Store {
	t.Helper()
	ring, err := fieldcrypt.NewFieldKeyring("test", []byte("bosun-test-field-key-0123456789"), nil, nil, "bosun-webhook-signing-secrets")
	if err != nil {
		t.Fatal(err)
	}
	return &ledger.Store{DB: db, Cipher: ring}
}

func newTenant() string { return uuid.NewString() }

// createEndpoint creates an endpoint directly in the ledger with the given
// URL and event types.
func createEndpoint(t *testing.T, store *ledger.Store, tenantID, url string, types ...string) ledger.Endpoint {
	t.Helper()
	if len(types) == 0 {
		types = []string{ledger.AllEventTypes}
	}
	ep, _, err := store.CreateEndpoint(context.Background(), tenantID, ledger.NewEndpoint{URL: url, EventTypes: types, APIVersion: ledger.APIVersionV1})
	if err != nil {
		t.Fatalf("create endpoint: %v", err)
	}
	return ep
}

// backdateEndpoints makes every endpoint older than any event a test emits,
// since events older than an endpoint are not fanned out to it.
func backdateEndpoints(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`UPDATE bosun.webhook_endpoints SET created_at = now() - interval '1 hour'`); err != nil {
		t.Fatal(err)
	}
}

// streamLiveRecord builds the domain.events record a producer's relay would
// publish through Decklog for a stream.live event of tenantID.
func streamLiveRecord(t *testing.T, tenantID, topic string) (events.Event, kafka.Message) {
	t.Helper()
	streamID := uuid.NewString()
	ev, err := events.New("foghorn", tenantID, streamID, &publicv1.StreamLive{StreamId: streamID})
	if err != nil {
		t.Fatal(err)
	}
	return ev, recordFor(t, ev, topic)
}

func recordFor(t *testing.T, ev events.Event, topic string) kafka.Message {
	t.Helper()
	key, headers, value, err := events.EncodeRecord(ev.Envelope())
	if err != nil {
		t.Fatal(err)
	}
	msg := kafka.Message{Key: key, Value: value, Topic: topic, Headers: map[string]string{}}
	for _, h := range headers {
		msg.Headers[h.Key] = string(h.Value)
	}
	return msg
}

// receiver is a local TLS webhook receiver that counts and keeps requests.
type receiver struct {
	server *httptest.Server
	mu     sync.Mutex
	hits   atomic.Int64
	status atomic.Int64
	bodies [][]byte
	heads  []http.Header
}

func newReceiver(t *testing.T) *receiver {
	t.Helper()
	r := &receiver{}
	r.status.Store(http.StatusOK)
	r.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		buf := make([]byte, 1<<20)
		n, _ := req.Body.Read(buf)
		for n < len(buf) {
			m, err := req.Body.Read(buf[n:])
			n += m
			if err != nil {
				break
			}
		}
		r.mu.Lock()
		r.bodies = append(r.bodies, append([]byte(nil), buf[:n]...))
		r.heads = append(r.heads, req.Header.Clone())
		r.mu.Unlock()
		r.hits.Add(1)
		w.WriteHeader(int(r.status.Load()))
		_, _ = w.Write([]byte(`{"received":true}`))
	}))
	t.Cleanup(r.server.Close)
	return r
}

func (r *receiver) request(i int) ([]byte, http.Header) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.bodies[i], r.heads[i]
}

// testClient is the delivery HTTP client with the only exception the tests
// need: the receiver's loopback address, and the receiver's certificate.
func (r *receiver) testClient(t *testing.T) *http.Client {
	t.Helper()
	host, _, err := net.SplitHostPort(r.server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	ip := net.ParseIP(host).To4()
	policy := restream.DestinationPolicy{AllowedCIDRs: []*net.IPNet{{IP: ip, Mask: net.CIDRMask(32, 32)}}}
	return delivery.NewHTTPClient(delivery.ClientOptions{
		Policy:      policy,
		RootCAs:     r.server.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs,
		OnlyAddress: r.server.Listener.Addr().String(),
	})
}

// productionClient is the client main.go builds.
func (r *receiver) productionClient(t *testing.T) *http.Client {
	t.Helper()
	return delivery.NewHTTPClient(delivery.ClientOptions{Policy: restream.PublicDestinationPolicy(), RootCAs: r.server.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs})
}

func countRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func fixedJitter() float64 { return 0.5 }
