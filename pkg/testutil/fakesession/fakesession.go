// Package fakesession is a database/sql driver for tests that need to tell
// physical database sessions apart. Each session the pool opens gets a number
// starting at 1, every statement it runs is answered by a test-supplied
// function, and the driver records which sessions were closed. sqlmock hands
// the pool one shared connection, so it cannot show whether a poisoned session
// went back to the pool or was discarded.
package fakesession

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"sync"

	"github.com/yugabyte/pgx/v5/pgconn"
)

// Answer returns the single boolean a statement yields on session, or the
// error the session reports for it.
type Answer func(session int, query string) (bool, error)

// Server tracks the sessions a fake database opened and closed.
type Server struct {
	answer Answer

	mu         sync.Mutex
	opened     int
	closed     map[int]bool
	statements map[int][]string
}

// New returns a pool over a fake database whose sessions answer through answer.
func New(answer Answer) (*sql.DB, *Server) {
	s := &Server{answer: answer, closed: map[int]bool{}, statements: map[int][]string{}}
	return sql.OpenDB(connector{s}), s
}

// ExpiredSessionError is the error YugabyteDB returns for every statement on
// a backend whose session-level transaction it expired.
func ExpiredSessionError() error {
	return &pgconn.PgError{Code: "40001", Message: "current transaction is expired or aborted"}
}

// Opened returns how many physical sessions the pool opened.
func (s *Server) Opened() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.opened
}

// Closed reports whether the pool closed session.
func (s *Server) Closed(session int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed[session]
}

// Statements returns the statements session ran, in order.
func (s *Server) Statements(session int) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.statements[session]...)
}

func (s *Server) run(session int, query string) (bool, error) {
	s.mu.Lock()
	s.statements[session] = append(s.statements[session], query)
	s.mu.Unlock()
	return s.answer(session, query)
}

type connector struct{ s *Server }

func (c connector) Connect(context.Context) (driver.Conn, error) {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	c.s.opened++
	return &conn{s: c.s, id: c.s.opened}, nil
}

func (c connector) Driver() driver.Driver { return fakeDriver{} }

type fakeDriver struct{}

func (fakeDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("fakesession: open through the connector")
}

type conn struct {
	s  *Server
	id int
}

func (c *conn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("fakesession: prepared statements are not supported")
}

func (c *conn) Begin() (driver.Tx, error) {
	return nil, errors.New("fakesession: transactions are not supported")
}

func (c *conn) Close() error {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	c.s.closed[c.id] = true
	return nil
}

func (c *conn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	v, err := c.s.run(c.id, query)
	if err != nil {
		return nil, err
	}
	return &rows{value: v}, nil
}

func (c *conn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	if _, err := c.s.run(c.id, query); err != nil {
		return nil, err
	}
	return driver.RowsAffected(1), nil
}

type rows struct {
	value bool
	done  bool
}

func (r *rows) Columns() []string { return []string{"value"} }

func (r *rows) Close() error { return nil }

func (r *rows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	dest[0] = r.value
	return nil
}
