package periscopeingestdb

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	pkgdatabase "github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/google/uuid"
)

// Rows is the subset of a ClickHouse result set the ingest reads need.
type Rows interface {
	Next() bool
	Close() error
	Scan(dest ...interface{}) error
	Err() error
}

// BufferConn is the connection a WriteBuffer inserts and deduplicates through.
type BufferConn interface {
	BatchPreparer
	Query(ctx context.Context, query string, args ...interface{}) (Rows, error)
}

// maxPartitionsPerInsert is ClickHouse's default max_partitions_per_insert_block.
// Most ingest tables partition by (month, tenant_id), so one INSERT carries the
// rows of at most this many (tenant, month) pairs.
const maxPartitionsPerInsert = 100

// maxIDsPerLookup bounds the ids one duplicate lookup names.
const maxIDsPerLookup = 5000

var insertTablePattern = regexp.MustCompile(`(?is)^\s*INSERT\s+INTO\s+(?:periscope\.)?([A-Za-z0-9_]+)`)

func insertTable(query string) string {
	if m := insertTablePattern.FindStringSubmatch(query); m != nil {
		return m[1]
	}
	return ""
}

// ingestIDNamespace scopes the ingest_id UUIDs derived from source records.
var ingestIDNamespace = uuid.MustParse("6f2b8f0e-6a55-4c1e-9d43-7a3c1f0b9e21")

// RecordIdentity is implemented by a row origin that names its source record.
// Two deliveries of the same record, including a mirrored copy, must return
// the same identity.
type RecordIdentity interface {
	IngestIdentity() string
}

// IngestID is the ingest_id of the index-th row a record with the given
// identity writes to table.
func IngestID(recordIdentity, table string, index int) uuid.UUID {
	return uuid.NewSHA1(ingestIDNamespace, []byte(recordIdentity+"\x00"+table+"\x00"+strconv.Itoa(index)))
}

// WriteBufferOptions configures a WriteBuffer.
type WriteBufferOptions struct {
	// IsRetryable reports whether an insert or duplicate lookup failure is
	// transient. A transient failure aborts Flush and keeps the rows pending;
	// any other insert failure is charged to the rows that caused it.
	IsRetryable func(error) bool
	// OnDuplicate observes rows dropped because their table already holds,
	// or this batch already carries, their ingest_id.
	OnDuplicate func(table string, rows int)
}

// RowFailure is a buffered row ClickHouse rejected permanently.
type RowFailure struct {
	Origin any
	Table  string
	Err    error
}

// WriteBuffer collects the rows that typed writers append while one Kafka poll
// batch is handled and inserts each statement's rows with one native batch per
// Flush. Rows keep the order they were sent in. It is not safe for concurrent
// use: one poll batch is handled on one goroutine.
type WriteBuffer struct {
	db         BufferConn
	opts       WriteBufferOptions
	statements []*pendingStatement
	byQuery    map[string]*pendingStatement
	// written counts, per origin and table, the identified rows the origin
	// sent, which numbers each row's ingest_id.
	written  map[any]map[string]int
	failures []RowFailure
}

// statementIdentity marks a statement whose last column is ingest_id and says
// how its duplicate lookup is bounded.
type statementIdentity struct {
	tenant     bool
	timeColumn string
	timeField  string
	scope      func(id *statementIdentity, row any) rowScope
}

type pendingStatement struct {
	query    string
	table    string
	identity *statementIdentity
	rows     []pendingRow
}

type pendingRow struct {
	origin any
	typed  any
	values []interface{}
	id     uuid.UUID
	scope  rowScope
}

// NewWriteBuffer returns an empty buffer that inserts through db.
func NewWriteBuffer(db BufferConn, opts WriteBufferOptions) *WriteBuffer {
	return &WriteBuffer{db: db, opts: opts, byQuery: make(map[string]*pendingStatement), written: make(map[any]map[string]int)}
}

type writeBufferKey struct{}

type rowOriginKey struct{}

// WithWriteBuffer makes every typed writer prepared with the returned context
// append to b instead of inserting.
func WithWriteBuffer(ctx context.Context, b *WriteBuffer) context.Context {
	return context.WithValue(ctx, writeBufferKey{}, b)
}

// WriteBufferFrom returns the buffer typed writers append to under ctx, or nil.
func WriteBufferFrom(ctx context.Context) *WriteBuffer {
	if b, ok := ctx.Value(writeBufferKey{}).(*WriteBuffer); ok {
		return b
	}
	return nil
}

// WithRowOrigin tags the rows written under the returned context with origin,
// the source record a discard or a RowFailure refers to. Origins are compared
// with ==, so each source record needs a distinct value; an origin that
// implements RecordIdentity also gives its rows their ingest_id.
func WithRowOrigin(ctx context.Context, origin any) context.Context {
	return context.WithValue(ctx, rowOriginKey{}, origin)
}

func rowOriginFrom(ctx context.Context) any {
	return ctx.Value(rowOriginKey{})
}

func (b *WriteBuffer) newBatch(query string, origin any, identity *statementIdentity) *bufferedBatch {
	return &bufferedBatch{buffer: b, query: query, origin: origin, identity: identity}
}

func (b *WriteBuffer) add(query string, origin any, identity *statementIdentity, rows []pendingRow) {
	if len(rows) == 0 {
		return
	}
	st := b.byQuery[query]
	if st == nil {
		st = &pendingStatement{query: query, table: insertTable(query), identity: identity}
		b.byQuery[query] = st
		b.statements = append(b.statements, st)
	}
	recordIdentity, identified := origin.(RecordIdentity)
	for i := range rows {
		rows[i].origin = origin
		if identity == nil || !identified {
			continue
		}
		counts := b.written[origin]
		if counts == nil {
			counts = make(map[string]int)
			b.written[origin] = counts
		}
		rows[i].id = IngestID(recordIdentity.IngestIdentity(), st.table, counts[st.table])
		counts[st.table]++
		rows[i].values[len(rows[i].values)-1] = rows[i].id
		rows[i].scope = identity.scope(identity, rows[i].typed)
	}
	st.rows = append(st.rows, rows...)
}

// DiscardOrigin drops origin's rows that have not been inserted and restarts
// its ingest_id numbering, so a retried handler writes the same ids again.
func (b *WriteBuffer) DiscardOrigin(origin any) {
	for _, st := range b.statements {
		st.rows = filterRows(st.rows, func(r pendingRow) bool { return r.origin != origin })
	}
	delete(b.written, origin)
}

// HasPending reports whether a row not yet inserted into table satisfies
// match; a nil match accepts every row. Rows are the typed values passed to
// Writer.Append.
func (b *WriteBuffer) HasPending(table string, match func(row any) bool) bool {
	for _, st := range b.statements {
		if st.table != table {
			continue
		}
		for _, r := range st.rows {
			if match == nil || match(r.typed) {
				return true
			}
		}
	}
	return false
}

// Pending returns the number of rows not yet inserted.
func (b *WriteBuffer) Pending() int {
	n := 0
	for _, st := range b.statements {
		n += len(st.rows)
	}
	return n
}

// TakeFailures returns and forgets the rows ClickHouse rejected permanently.
func (b *WriteBuffer) TakeFailures() []RowFailure {
	failures := b.failures
	b.failures = nil
	return failures
}

// Flush drops rows whose ingest_id is already stored or repeated in the batch,
// then inserts every pending statement in first-written order. A row
// ClickHouse rejects permanently is recorded as a RowFailure and the rest of
// its statement is still inserted. A transient failure returns the error;
// statements already inserted are not resent. Cancelling ctx does not
// interrupt a flush: a shutdown that stopped half-way would leave part of a
// batch inserted for a redelivery cut differently.
func (b *WriteBuffer) Flush(ctx context.Context) error {
	ctx = context.WithoutCancel(ctx)
	if err := b.dropStoredRows(ctx); err != nil {
		return err
	}
	for len(b.statements) > 0 {
		st := b.statements[0]
		if err := b.insertStatement(ctx, st); err != nil {
			return err
		}
		b.statements = b.statements[1:]
		delete(b.byQuery, st.query)
	}
	return nil
}

func (b *WriteBuffer) retryable(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	return b.opts.IsRetryable != nil && b.opts.IsRetryable(err)
}

// dropStoredRows removes identified rows whose ingest_id repeats an earlier
// pending row or is already stored in their table, looked up with one query
// per table and id chunk.
func (b *WriteBuffer) dropStoredRows(ctx context.Context) error {
	type tableRows struct {
		identity *statementIdentity
		ids      []uuid.UUID
		scopes   []rowScope
	}
	byTable := make(map[string]*tableRows)
	var tables []string
	seen := make(map[uuid.UUID]struct{})
	repeated := make(map[string]int)
	for _, st := range b.statements {
		if st.identity == nil {
			continue
		}
		st.rows = filterRows(st.rows, func(r pendingRow) bool {
			if r.id == uuid.Nil {
				return true
			}
			if _, dup := seen[r.id]; dup {
				repeated[st.table]++
				return false
			}
			seen[r.id] = struct{}{}
			return true
		})
		for _, r := range st.rows {
			if r.id == uuid.Nil {
				continue
			}
			t := byTable[st.table]
			if t == nil {
				t = &tableRows{identity: st.identity}
				byTable[st.table] = t
				tables = append(tables, st.table)
			}
			t.ids = append(t.ids, r.id)
			t.scopes = append(t.scopes, r.scope)
		}
	}
	for table, n := range repeated {
		if b.opts.OnDuplicate != nil {
			b.opts.OnDuplicate(table, n)
		}
	}
	for _, table := range tables {
		t := byTable[table]
		stored := make(map[uuid.UUID]struct{})
		for start := 0; start < len(t.ids); start += maxIDsPerLookup {
			end := min(start+maxIDsPerLookup, len(t.ids))
			if err := b.storedIngestIDs(ctx, table, t.identity.timeColumn, t.ids[start:end], t.scopes[start:end], stored); err != nil {
				pkgdatabase.ObserveDatabaseError("periscope-ingest", pkgdatabase.EngineClickHouse, err, false)
				if b.retryable(err) {
					return fmt.Errorf("%s duplicate lookup: %w", table, err)
				}
				// A lookup ClickHouse rejects outright treats the rows as new,
				// as a failed per-event duplicate check does.
			}
		}
		if len(stored) == 0 {
			continue
		}
		dropped := 0
		for _, st := range b.statements {
			if st.table != table {
				continue
			}
			st.rows = filterRows(st.rows, func(r pendingRow) bool {
				if _, dup := stored[r.id]; dup && r.id != uuid.Nil {
					dropped++
					return false
				}
				return true
			})
		}
		if b.opts.OnDuplicate != nil && dropped > 0 {
			b.opts.OnDuplicate(table, dropped)
		}
	}
	return nil
}

// storedIngestIDs adds to stored the ids table already holds. The lookup is
// bounded by the rows' tenants and, when the table has a time column, by the
// rows' time range, so ClickHouse reads only their partitions and key range
// before the ingest_id skip index.
func (b *WriteBuffer) storedIngestIDs(ctx context.Context, table, timeColumn string, ids []uuid.UUID, scopes []rowScope, stored map[uuid.UUID]struct{}) error {
	args := make([]interface{}, 0, len(ids)+8)
	for _, id := range ids {
		args = append(args, id)
	}
	where := []string{fmt.Sprintf("ingest_id IN (%s)", placeholders(len(ids)))}

	tenants := make([]interface{}, 0, 1)
	seenTenant := make(map[uuid.UUID]struct{})
	allTenants, allTimes := true, timeColumn != ""
	var from, to time.Time
	for i, s := range scopes {
		if s.tenant == uuid.Nil {
			allTenants = false
		} else if _, ok := seenTenant[s.tenant]; !ok {
			seenTenant[s.tenant] = struct{}{}
			tenants = append(tenants, s.tenant)
		}
		if s.at.IsZero() {
			allTimes = false
			continue
		}
		if i == 0 || s.at.Before(from) {
			from = s.at
		}
		if i == 0 || s.at.After(to) {
			to = s.at
		}
	}
	if allTenants && len(tenants) > 0 {
		where = append(where, fmt.Sprintf("tenant_id IN (%s)", placeholders(len(tenants))))
		args = append(args, tenants...)
	}
	if allTimes && !from.IsZero() {
		// Time columns store whole seconds or coarser; widen the bounds so a
		// truncated stored value still matches.
		where = append(where, fmt.Sprintf("%s >= ? AND %s <= ?", timeColumn, timeColumn))
		args = append(args, from.Truncate(time.Second).Add(-time.Second), to.Truncate(time.Second).Add(time.Second))
	}
	rows, err := b.db.Query(ctx, fmt.Sprintf("SELECT DISTINCT ingest_id FROM %s WHERE %s", table, strings.Join(where, " AND ")), args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return err
		}
		stored[id] = struct{}{}
	}
	return rows.Err()
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

func (b *WriteBuffer) insertStatement(ctx context.Context, st *pendingStatement) error {
	chunks := partitionChunks(st.rows)
	for i, chunk := range chunks {
		remaining, err := b.send(ctx, st, chunk)
		if err != nil {
			rest := remaining
			for _, c := range chunks[i+1:] {
				rest = append(rest, c...)
			}
			st.rows = rest
			return err
		}
	}
	st.rows = nil
	return nil
}

// send inserts rows with one native batch. A row the driver cannot encode is
// failed and the batch rebuilt without it; a batch the server rejects
// permanently is retried row by row so only the rows it rejects fail. On a
// transient error it returns the rows that are still not inserted.
func (b *WriteBuffer) send(ctx context.Context, st *pendingStatement, rows []pendingRow) ([]pendingRow, error) {
	for len(rows) > 0 {
		batch, err := b.db.PrepareBatch(ctx, st.query)
		if err != nil {
			pkgdatabase.ObserveDatabaseError("periscope-ingest", pkgdatabase.EngineClickHouse, err, false)
			observeInsert(st.table, len(rows), err)
			if b.retryable(err) {
				return rows, err
			}
			b.fail(st.table, rows, err)
			return nil, nil
		}
		poison, appendErr := -1, error(nil)
		for i, r := range rows {
			if rowErr := batch.Append(r.values...); rowErr != nil {
				poison, appendErr = i, rowErr
				break
			}
		}
		if poison >= 0 {
			if closer, ok := batch.(interface{ Close() error }); ok {
				_ = closer.Close()
			}
			pkgdatabase.ObserveDatabaseError("periscope-ingest", pkgdatabase.EngineClickHouse, appendErr, false)
			b.fail(st.table, rows[poison:poison+1], appendErr)
			rows = append(append([]pendingRow(nil), rows[:poison]...), rows[poison+1:]...)
			continue
		}
		err = batch.Send()
		observeInsert(st.table, len(rows), err)
		if err != nil {
			pkgdatabase.ObserveDatabaseError("periscope-ingest", pkgdatabase.EngineClickHouse, err, false)
			if b.retryable(err) {
				return rows, err
			}
			if len(rows) == 1 {
				b.fail(st.table, rows, err)
				return nil, nil
			}
			for i := range rows {
				if remaining, err := b.send(ctx, st, rows[i:i+1]); err != nil {
					return append(remaining, rows[i+1:]...), err
				}
			}
			return nil, nil
		}
		return nil, nil
	}
	return nil, nil
}

func (b *WriteBuffer) fail(table string, rows []pendingRow, err error) {
	for _, r := range rows {
		b.failures = append(b.failures, RowFailure{Origin: r.origin, Table: table, Err: err})
	}
}

func filterRows(rows []pendingRow, keep func(pendingRow) bool) []pendingRow {
	kept := rows[:0]
	for _, r := range rows {
		if keep(r) {
			kept = append(kept, r)
		}
	}
	return kept
}

// partitionChunks splits rows so each chunk spans at most
// maxPartitionsPerInsert (tenant, month) pairs. A pair's rows stay in one
// chunk in their original order.
func partitionChunks(rows []pendingRow) [][]pendingRow {
	chunkOf := make(map[string]int)
	var chunks [][]pendingRow
	var keysInLast int
	for _, r := range rows {
		key := partitionKey(r.typed)
		idx, ok := chunkOf[key]
		if !ok {
			if len(chunks) == 0 || keysInLast == maxPartitionsPerInsert {
				chunks = append(chunks, nil)
				keysInLast = 0
			}
			idx = len(chunks) - 1
			chunkOf[key] = idx
			keysInLast++
		}
		chunks[idx] = append(chunks[idx], r)
	}
	return chunks
}

// partitionKey is the row's TenantID and the month of its Timestamp, the
// partition key of the tenant-partitioned ingest tables. A row without those
// fields contributes an empty component.
func partitionKey(row any) string {
	v := reflect.ValueOf(row)
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return ""
	}
	key := ""
	if f := v.FieldByName("TenantID"); f.IsValid() {
		key = fmt.Sprint(f.Interface())
	}
	if f := v.FieldByName("Timestamp"); f.IsValid() {
		if ts, ok := f.Interface().(time.Time); ok {
			key += "|" + ts.UTC().Format("200601")
		}
	}
	return key
}

// bufferedBatch is the Batch a typed writer gets under a WriteBuffer. Its
// rows join the buffer on Send, so a handler that fails before Send leaves
// nothing behind.
type bufferedBatch struct {
	buffer   *WriteBuffer
	query    string
	origin   any
	identity *statementIdentity
	rows     []pendingRow
	sent     bool
}

func (b *bufferedBatch) Append(v ...interface{}) error {
	return b.appendRow(nil, v)
}

func (b *bufferedBatch) appendRow(typed any, values []interface{}) error {
	if b.sent {
		return errors.New("periscopeingestdb: append to a sent batch")
	}
	b.rows = append(b.rows, pendingRow{typed: typed, values: values})
	return nil
}

func (b *bufferedBatch) Send() error {
	if b.sent {
		return errors.New("periscopeingestdb: batch already sent")
	}
	b.sent = true
	b.buffer.add(b.query, b.origin, b.identity, b.rows)
	b.rows = nil
	return nil
}

func (b *bufferedBatch) Close() error {
	b.rows = nil
	return nil
}
