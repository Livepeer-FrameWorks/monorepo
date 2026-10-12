package redis

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

// silentRedis answers connection setup and PING, and never answers any other
// command: a server that accepted a command and stalled.
func silentRedis(t *testing.T) string {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveSilentRedis(conn)
		}
	}()
	return ln.Addr().String()
}

func serveSilentRedis(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	for {
		args, err := readRESPCommand(r)
		if err != nil {
			return
		}
		if len(args) == 0 {
			continue
		}
		var reply string
		switch strings.ToUpper(args[0]) {
		case "HELLO":
			reply = "-ERR unknown command 'HELLO'\r\n"
		case "PING":
			reply = "+PONG\r\n"
		case "CLIENT", "SELECT", "AUTH":
			reply = "+OK\r\n"
		default:
			continue
		}
		if _, err := io.WriteString(conn, reply); err != nil {
			return
		}
	}
}

func readRESPCommand(r *bufio.Reader) ([]string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimRight(line, "\r\n")
	if !strings.HasPrefix(line, "*") {
		return strings.Fields(line), nil
	}
	n, err := strconv.Atoi(line[1:])
	if err != nil {
		return nil, err
	}
	args := make([]string, 0, n)
	for range n {
		header, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		size, err := strconv.Atoi(strings.TrimRight(header, "\r\n")[1:])
		if err != nil {
			return nil, err
		}
		buf := make([]byte, size+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		args = append(args, string(buf[:size]))
	}
	return args, nil
}

// A command the server never answers returns when the caller's context
// deadline passes, not after the client's read timeout.
func TestCommandRespectsCallerDeadline(t *testing.T) {
	addr := silentRedis(t)
	const readTimeout = 3 * time.Second
	const callerBudget = 100 * time.Millisecond

	single, err := NewUniversalClient(context.Background(), Config{Mode: ModeSingle, Addrs: []string{addr}, ReadTimeout: readTimeout})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = single.Close() })
	fromURL, err := NewClientFromURL(context.Background(), fmt.Sprintf("redis://%s/0?read_timeout=%s", addr, readTimeout))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fromURL.Close() })

	for name, get := range map[string]func(context.Context) error{
		"universal single": func(ctx context.Context) error { return single.Get(ctx, "k").Err() },
		"from url":         func(ctx context.Context) error { return fromURL.Get(ctx, "k").Err() },
	} {
		ctx, cancel := context.WithTimeout(context.Background(), callerBudget)
		began := time.Now()
		err := get(ctx)
		elapsed := time.Since(began)
		cancel()
		if err == nil {
			t.Fatalf("%s: GET against a silent server succeeded", name)
		}
		if elapsed > callerBudget+time.Second {
			t.Fatalf("%s: GET returned after %s (%v), want close to the caller's %s deadline", name, elapsed, err, callerBudget)
		}
		if !errors.Is(err, context.DeadlineExceeded) && !isTimeout(err) {
			t.Fatalf("%s: err = %v, want a deadline error", name, err)
		}
	}
}

func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}
