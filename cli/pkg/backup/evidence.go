package backup

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// DumpEvidence is what the data section of a PostgreSQL/YugabyteDB dump proves about the database it came from.
type DumpEvidence struct {
	Ledger    []LedgerRow
	RowCounts map[string]int64
}

// ParseDataSection reads a plain-text data section (pg_dump or ysql_dump --section=data) and counts the rows of every
// COPY block. The rows of the `_migrations` table become the ledger, so the ledger and the row counts describe the
// same snapshot as the dump. A dump without a `_migrations` table yields an empty ledger.
func ParseDataSection(r io.Reader) (*DumpEvidence, error) {
	evidence := &DumpEvidence{RowCounts: map[string]int64{}}
	br := bufio.NewReaderSize(r, 1<<20)
	var (
		table     string
		ledger    bool
		columns   map[string]int
		inCopy    bool
		lineCount int
	)
	for {
		line, err := br.ReadString('\n')
		if line != "" {
			lineCount++
			text := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
			switch {
			case inCopy && text == `\.`:
				inCopy = false
			case inCopy:
				evidence.RowCounts[table]++
				if ledger {
					row, rowErr := ledgerRowFromCopy(text, columns)
					if rowErr != nil {
						return nil, fmt.Errorf("line %d: %w", lineCount, rowErr)
					}
					evidence.Ledger = append(evidence.Ledger, row)
				}
			case strings.HasPrefix(text, "COPY ") && strings.HasSuffix(text, "FROM stdin;"):
				name, cols, parseErr := parseCopyHeader(text)
				if parseErr != nil {
					return nil, fmt.Errorf("line %d: %w", lineCount, parseErr)
				}
				table, columns, inCopy = name, cols, true
				ledger = isLedgerTable(name)
				evidence.RowCounts[table] += 0
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
	}
	if inCopy {
		return nil, fmt.Errorf("data section ends inside the COPY block of %s", table)
	}
	return evidence, nil
}

func isLedgerTable(name string) bool {
	return name == "_migrations" || strings.HasSuffix(name, "._migrations")
}

// parseCopyHeader reads `COPY <table> (<col>, ...) FROM stdin;`.
func parseCopyHeader(text string) (string, map[string]int, error) {
	body := strings.TrimSuffix(strings.TrimPrefix(text, "COPY "), " FROM stdin;")
	open := strings.Index(body, " (")
	if open < 0 || !strings.HasSuffix(body, ")") {
		return strings.TrimSpace(body), nil, nil
	}
	name := strings.TrimSpace(body[:open])
	columns := map[string]int{}
	for i, col := range strings.Split(body[open+2:len(body)-1], ",") {
		col = strings.TrimSpace(col)
		col = strings.Trim(col, `"`)
		columns[col] = i
	}
	if name == "" {
		return "", nil, fmt.Errorf("COPY header without a table: %q", text)
	}
	return name, columns, nil
}

func ledgerRowFromCopy(text string, columns map[string]int) (LedgerRow, error) {
	fields := strings.Split(text, "\t")
	get := func(name string) (string, error) {
		idx, ok := columns[name]
		if !ok || idx >= len(fields) {
			return "", fmt.Errorf("_migrations row lacks column %q", name)
		}
		return unescapeCopy(fields[idx]), nil
	}
	var row LedgerRow
	var err error
	if row.Version, err = get("version"); err != nil {
		return row, err
	}
	if row.Phase, err = get("phase"); err != nil {
		return row, err
	}
	seq, err := get("seq")
	if err != nil {
		return row, err
	}
	if row.Seq, err = strconv.Atoi(seq); err != nil {
		return row, fmt.Errorf("_migrations seq %q: %w", seq, err)
	}
	if row.Checksum, err = get("checksum"); err != nil {
		return row, err
	}
	return row, nil
}

// unescapeCopy reverses the COPY text-format escapes that can occur in ledger values.
func unescapeCopy(v string) string {
	if !strings.Contains(v, `\`) {
		return v
	}
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		if v[i] != '\\' || i+1 == len(v) {
			b.WriteByte(v[i])
			continue
		}
		i++
		switch v[i] {
		case 't':
			b.WriteByte('\t')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		default:
			b.WriteByte(v[i])
		}
	}
	return b.String()
}

// CountLines counts newline-terminated lines, which is the row count of a TabSeparated stream.
func CountLines(r io.Reader) (int64, error) {
	buf := make([]byte, 1<<20)
	var n int64
	for {
		k, err := r.Read(buf)
		n += int64(bytes.Count(buf[:k], []byte{'\n'}))
		if err != nil {
			if errors.Is(err, io.EOF) {
				return n, nil
			}
			return n, err
		}
	}
}
