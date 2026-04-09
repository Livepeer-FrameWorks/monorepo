package preflight

import (
	"context"
	"fmt"
	"net"
	"time"
)

const infraDialTimeout = 3 * time.Second

var infraDialer = net.Dialer{Timeout: infraDialTimeout}

// PostgresConnectivity dials a Postgres TCP port and checks for readiness.
func PostgresConnectivity(ctx context.Context, host string, port int) Check {
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	conn, err := infraDialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return Check{Name: "Postgres", OK: false, Detail: addr, Error: err.Error()}
	}
	conn.Close()
	return Check{Name: "Postgres", OK: true, Detail: fmt.Sprintf("%s reachable", addr)}
}

// ClickHouseConnectivity dials a ClickHouse native TCP port.
func ClickHouseConnectivity(ctx context.Context, host string, port int) Check {
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	conn, err := infraDialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return Check{Name: "ClickHouse", OK: false, Detail: addr, Error: err.Error()}
	}
	conn.Close()
	return Check{Name: "ClickHouse", OK: true, Detail: fmt.Sprintf("%s reachable", addr)}
}

// KafkaBrokerHealth dials each broker and reports aggregate health.
func KafkaBrokerHealth(ctx context.Context, brokers []string) Check {
	if len(brokers) == 0 {
		return Check{Name: "Kafka", OK: false, Detail: "no brokers configured", Error: "empty broker list"}
	}
	healthy := 0
	for _, b := range brokers {
		conn, err := infraDialer.DialContext(ctx, "tcp", b)
		if err == nil {
			conn.Close()
			healthy++
		}
	}
	detail := fmt.Sprintf("%d/%d brokers reachable", healthy, len(brokers))
	if healthy == 0 {
		return Check{Name: "Kafka", OK: false, Detail: detail, Error: "no brokers reachable"}
	}
	return Check{Name: "Kafka", OK: true, Detail: detail}
}

// RedisConnectivity dials a Redis TCP port.
func RedisConnectivity(ctx context.Context, host string, port int) Check {
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	conn, err := infraDialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return Check{Name: "Redis", OK: false, Detail: addr, Error: err.Error()}
	}
	conn.Close()
	return Check{Name: "Redis", OK: true, Detail: fmt.Sprintf("%s reachable", addr)}
}
