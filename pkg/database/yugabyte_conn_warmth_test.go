//go:build yugabyte_measure

package database

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	schemasql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
)

// The Purser admission read, as sqlc generates it in api_billing/internal/database/purserdb.
const warmthAdmissionQuery = `SELECT
    ts.billing_model,
    ts.status AS subscription_status,
    pb.balance_cents,
    reservations.reserved_balance_cents,
    ts.payment_method,
    ts.stripe_subscription_id,
    ts.mollie_subscription_id,
    bt.tier_name,
    COALESCE(bt.tier_level, 0)::integer AS tier_level,
    ts.stripe_customer_id,
    EXISTS (
        SELECT 1 FROM purser.mollie_mandates mm
        WHERE mm.tenant_id = ts.tenant_id AND mm.status = 'valid'
    )::boolean AS has_valid_mollie_mandate,
    og.collection AS grant_collection,
    COALESCE(og.waive_usage, false)::boolean AS grant_waive_usage,
    COALESCE(og.base_price, bt.base_price)::text AS effective_base_price
FROM purser.tenant_subscriptions ts
JOIN purser.billing_tiers bt ON bt.id = ts.tier_id
LEFT JOIN purser.subscription_operator_grants og
    ON og.subscription_id = ts.id AND (og.expires_at IS NULL OR og.expires_at > NOW())
LEFT JOIN purser.prepaid_balances pb
    ON pb.tenant_id = ts.tenant_id AND pb.currency = $1
LEFT JOIN LATERAL (
    SELECT CEIL(COALESCE(SUM(reserved_amount_micro), 0)::numeric / 10000)::bigint
        AS reserved_balance_cents
    FROM purser.usage_reservations
    WHERE tenant_id = ts.tenant_id
      AND currency = $1
      AND updated_at >= NOW() - INTERVAL '3 minutes'
) reservations ON TRUE
WHERE ts.tenant_id = $2::text::uuid AND ts.status != 'cancelled'
ORDER BY ts.created_at DESC
LIMIT 1`

const warmthTenantID = "5f0c1e2a-0000-4000-8000-000000000001"

// TestYugabyteConnectionWarmthCost measures what a request pays when the pool hands it a connection whose YSQL backend
// has not yet loaded the catalog entries of the relations the request reads, against the same request on a warm
// backend, for the Purser admission read in a distributed and a colocated database on a three-node RF3 cluster.
// FRAMEWORKS_YB_WARMTH_NETEM_DELAY (for example 1ms) adds that one-way delay to every node's egress once the schema is
// loaded, so node to node and client to node round trips carry twice that, as between separate hosts.
// FRAMEWORKS_YB_WARMTH_TSERVER_FLAGS appends comma-separated tserver flags to the ones the cluster starts with.
func TestYugabyteConnectionWarmthCost(t *testing.T) {
	yugabyteHAExtraTServerFlags = strings.TrimSpace(os.Getenv("FRAMEWORKS_YB_WARMTH_TSERVER_FLAGS"))
	cluster := startYugabyteHACluster(t)
	admin := cluster.openSmartDriver(t)
	defer admin.Close()

	baseline, err := schemasql.Content.ReadFile("schema/purser.sql")
	if err != nil {
		t.Fatal(err)
	}
	iterations := 12
	if raw := strings.TrimSpace(os.Getenv("FRAMEWORKS_YB_WARMTH_ITERATIONS")); raw != "" {
		if iterations, err = strconv.Atoi(raw); err != nil || iterations < 1 {
			t.Fatalf("FRAMEWORKS_YB_WARMTH_ITERATIONS = %q", raw)
		}
	}

	layouts := []struct {
		name   string
		create string
	}{
		{name: "distributed", create: "CREATE DATABASE purser_warmth_distributed"},
		{name: "colocated", create: "CREATE DATABASE purser_warmth_colocated WITH COLOCATION = true"},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	for _, layout := range layouts {
		database := "purser_warmth_" + layout.name
		if _, err := admin.ExecContext(ctx, layout.create); err != nil {
			t.Fatalf("create %s: %v", database, err)
		}
		if out, err := cluster.ysqlshStdin(database, string(baseline)); err != nil {
			t.Fatalf("apply purser baseline to %s: %v\n%s", database, err, out)
		}
		db := cluster.openSmartDriverAt(t, database)
		seedWarmthAdmission(t, ctx, db)
		_ = db.Close()
	}
	if delay := strings.TrimSpace(os.Getenv("FRAMEWORKS_YB_WARMTH_NETEM_DELAY")); delay != "" {
		cluster.addEgressDelay(t, delay)
	}
	t.Logf("WARMTH master leader %s; tserver flags +%q", cluster.masterLeader(t), yugabyteHAExtraTServerFlags)
	for _, layout := range layouts {
		db := cluster.openSmartDriverAt(t, "purser_warmth_"+layout.name)
		measureWarmth(t, ctx, db, layout.name, iterations)
		_ = db.Close()
	}
}

func (cluster *yugabyteHACluster) masterLeader(t *testing.T) string {
	t.Helper()
	addresses := make([]string, 0, len(cluster.nodes))
	for _, node := range cluster.nodes {
		addresses = append(addresses, node.address+":7100")
	}
	out := dockerYugabyteHA(t, time.Minute, "exec", cluster.nodes[0].name, "bin/yb-admin",
		"-master_addresses", strings.Join(addresses, ","), "list_all_masters")
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "LEADER") {
			return strings.Join(strings.Fields(line), " ")
		}
	}
	return "unknown: " + out
}

func seedWarmthAdmission(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
WITH tier AS (
    INSERT INTO purser.billing_tiers (tier_name, display_name) VALUES ('warmth-probe', 'Warmth probe') RETURNING id
)
INSERT INTO purser.tenant_subscriptions (tenant_id, tier_id) SELECT $1::uuid, id FROM tier`, warmthTenantID); err != nil {
		t.Fatalf("seed admission rows: %v", err)
	}
}

type warmthSample struct {
	connect time.Duration
	cold    time.Duration
	warm    time.Duration
}

var catalogReadPattern = regexp.MustCompile(`Catalog Read Requests: (\d+)`)
var catalogReadTimePattern = regexp.MustCompile(`Catalog Read Execution Time: ([0-9.]+) ms`)

func measureWarmth(t *testing.T, ctx context.Context, db *sql.DB, layout string, iterations int) {
	t.Helper()
	db.SetMaxIdleConns(0)
	samples := make([]warmthSample, 0, iterations)
	run := func(connection *sql.Conn) (time.Duration, error) {
		started := time.Now()
		var model, status, tier, base string
		var tierLevel int32
		var balance, reserved sql.NullInt64
		var payment, stripeSub, mollieSub, stripeCustomer, grantCollection sql.NullString
		var mandate, waive bool
		err := connection.QueryRowContext(ctx, warmthAdmissionQuery, "EUR", warmthTenantID).Scan(&model, &status, &balance,
			&reserved, &payment, &stripeSub, &mollieSub, &tier, &tierLevel, &stripeCustomer, &mandate, &grantCollection, &waive, &base)
		return time.Since(started), err
	}
	for range iterations {
		started := time.Now()
		connection, err := db.Conn(ctx)
		if err != nil {
			t.Fatalf("open connection: %v", err)
		}
		if err := connection.PingContext(ctx); err != nil {
			t.Fatalf("ping fresh connection: %v", err)
		}
		sample := warmthSample{connect: time.Since(started)}
		if sample.cold, err = run(connection); err != nil {
			t.Fatalf("cold admission read: %v", err)
		}
		warm := make([]time.Duration, 0, 5)
		for range 5 {
			elapsed, err := run(connection)
			if err != nil {
				t.Fatalf("warm admission read: %v", err)
			}
			warm = append(warm, elapsed)
		}
		slices.Sort(warm)
		sample.warm = warm[len(warm)/2]
		var address string
		_ = connection.QueryRowContext(ctx, "SELECT host(inet_server_addr())").Scan(&address)
		_ = connection.Close()
		t.Logf("WARMTH layout=%s sample node=%s connect=%s cold=%s warm=%s", layout, address, sample.connect, sample.cold, sample.warm)
		samples = append(samples, sample)
	}

	connection, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("open explain connection: %v", err)
	}
	coldPlan := explainDist(t, ctx, connection)
	warmPlan := explainDist(t, ctx, connection)
	_ = connection.Close()

	pick := func(field func(warmthSample) time.Duration) (time.Duration, time.Duration, time.Duration) {
		values := make([]time.Duration, 0, len(samples))
		for _, sample := range samples {
			values = append(values, field(sample))
		}
		slices.Sort(values)
		return values[0], values[len(values)/2], values[len(values)-1]
	}
	connectMin, connectMedian, connectMax := pick(func(s warmthSample) time.Duration { return s.connect })
	coldMin, coldMedian, coldMax := pick(func(s warmthSample) time.Duration { return s.cold })
	warmMin, warmMedian, warmMax := pick(func(s warmthSample) time.Duration { return s.warm })
	t.Logf("WARMTH layout=%s n=%d connect min/median/max=%s/%s/%s", layout, len(samples), connectMin, connectMedian, connectMax)
	t.Logf("WARMTH layout=%s cold admission min/median/max=%s/%s/%s", layout, coldMin, coldMedian, coldMax)
	t.Logf("WARMTH layout=%s warm admission min/median/max=%s/%s/%s", layout, warmMin, warmMedian, warmMax)
	t.Logf("WARMTH layout=%s cold EXPLAIN catalog reads=%s (%s ms); warm=%s (%s ms)", layout,
		firstMatch(catalogReadPattern, coldPlan), firstMatch(catalogReadTimePattern, coldPlan),
		firstMatch(catalogReadPattern, warmPlan), firstMatch(catalogReadTimePattern, warmPlan))
}

func explainDist(t *testing.T, ctx context.Context, connection *sql.Conn) string {
	t.Helper()
	rows, err := connection.QueryContext(ctx, "EXPLAIN (ANALYZE, DIST, SUMMARY) "+warmthAdmissionQuery, "EUR", warmthTenantID)
	if err != nil {
		t.Fatalf("explain admission read: %v", err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func firstMatch(pattern *regexp.Regexp, text string) string {
	match := pattern.FindStringSubmatch(text)
	if match == nil {
		return "absent"
	}
	return match[1]
}

func (cluster *yugabyteHACluster) ysqlshStdin(database, script string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "docker", "exec", "-i", cluster.nodes[0].name, "bin/ysqlsh", "-X", "-h",
		cluster.nodes[0].name, "-U", "yugabyte", "-d", database, "-v", "ON_ERROR_STOP=1", "-q")
	command.Stdin = strings.NewReader(script)
	out, err := command.CombinedOutput()
	return string(out), err
}

func (cluster *yugabyteHACluster) addEgressDelay(t *testing.T, delay string) {
	t.Helper()
	// The engine image ships no tc, so a throwaway container joins each node's network namespace to set the qdisc.
	for _, node := range cluster.nodes {
		dockerYugabyteHA(t, 2*time.Minute, "run", "--rm", "--net", "container:"+node.name, "--cap-add", "NET_ADMIN",
			"alpine:3.20", "sh", "-c", "apk add -q iproute2-tc && tc qdisc add dev eth0 root netem delay "+delay)
	}
	t.Logf("WARMTH netem egress delay %s on every node", delay)
}
