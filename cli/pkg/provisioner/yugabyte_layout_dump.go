package provisioner

import (
	"errors"
	"sort"
)

// RewriteDumpForLayout rewrites ysql_dump schema output for a database created with a colocated layout. Distributed
// tables get the colocation opt-out and keep their key sharding. Colocated tables, and every index and key constraint
// on them, lose their HASH key markers: a colocated database rejects hash-partitioned relations and range shards
// them instead. Tables extraColocated accepts, such as the runtime ledgers, are placed colocated without a layout entry.
// A table, index, or key constraint whose placement cannot be resolved is an error.
func RewriteDumpForLayout(layout *DatabaseLayout, sql string, extraColocated func(table string) bool) (string, error) {
	if !layout.Colocated() {
		return "", errors.New("dump rewriting requires a colocated layout")
	}
	placementOf := func(table string) (TablePlacement, bool) {
		if placement, ok := layout.Placement(table); ok {
			return placement, true
		}
		if extraColocated != nil && extraColocated(table) {
			return TablePlacementColocated, true
		}
		return "", false
	}
	statements, err := sqlStatements(sql)
	if err != nil {
		return "", err
	}
	var edits []sqlEdit
	for _, stmt := range statements {
		creation, scanErr := scanStatement(sql, stmt)
		if scanErr != nil {
			return "", scanErr
		}
		if creation != nil {
			placement, ok := placementOf(creation.table)
			if !ok {
				return "", sqlPositionError(sql, stmt[0].start, "table %s is not classified in the %s layout", creation.table, layout.Database)
			}
			if placement == TablePlacementDistributed {
				edits = append(edits, sqlEdit{start: creation.optionOffset, end: creation.optionOffset, replacement: yugabyteColocationOptOut})
				continue
			}
			keyEdits, keyErr := colocatedTableKeyEdits(sql, stmt, creation.open, creation.close)
			if keyErr != nil {
				return "", keyErr
			}
			edits = append(edits, keyEdits...)
			continue
		}
		table, open, found, keyErr := dumpKeyList(sql, stmt)
		if keyErr != nil {
			return "", keyErr
		}
		if !found {
			continue
		}
		placement, ok := placementOf(table)
		if !ok {
			return "", sqlPositionError(sql, stmt[0].start, "index or key constraint on %s, which the %s layout does not classify", table, layout.Database)
		}
		if placement != TablePlacementColocated {
			continue
		}
		keyEdits, rangeErr := rangeKeyEdits(sql, stmt, open)
		if rangeErr != nil {
			return "", rangeErr
		}
		edits = append(edits, keyEdits...)
	}
	return applySQLEdits(sql, edits)
}

type sqlEdit struct {
	start       int
	end         int
	replacement string
}

func applySQLEdits(src string, edits []sqlEdit) (string, error) {
	sort.SliceStable(edits, func(i, j int) bool { return edits[i].start < edits[j].start })
	var out []byte
	last := 0
	for _, edit := range edits {
		if edit.start < last {
			return "", sqlPositionError(src, edit.start, "overlapping rewrite")
		}
		out = append(out, src[last:edit.start]...)
		out = append(out, edit.replacement...)
		last = edit.end
	}
	return string(append(out, src[last:]...)), nil
}

// colocatedTableKeyEdits converts the PRIMARY KEY and UNIQUE key lists inside a CREATE TABLE column list.
func colocatedTableKeyEdits(src string, stmt []sqlToken, open, close int) ([]sqlEdit, error) {
	var edits []sqlEdit
	for j := open + 1; j < close; j++ {
		keyOpen := -1
		switch {
		case isSQLWord(stmt[j], "key") && isSQLWord(stmt[j-1], "primary") && j+1 < close && isSQLPunct(stmt[j+1], "("):
			keyOpen = j + 1
		case isSQLWord(stmt[j], "unique"):
			if k := skipUniqueNulls(stmt, j+1); k < close && isSQLPunct(stmt[k], "(") {
				keyOpen = k
			}
		}
		if keyOpen < 0 {
			continue
		}
		keyEdits, err := rangeKeyEdits(src, stmt, keyOpen)
		if err != nil {
			return nil, err
		}
		edits = append(edits, keyEdits...)
	}
	return edits, nil
}

// dumpKeyList finds the key list of CREATE [UNIQUE] INDEX and ALTER TABLE ... ADD [CONSTRAINT name] PRIMARY KEY or
// UNIQUE, returning the indexed table and the token index of the list's opening parenthesis.
func dumpKeyList(src string, stmt []sqlToken) (string, int, bool, error) {
	if len(stmt) < 3 {
		return "", 0, false, nil
	}
	if isSQLWord(stmt[0], "create") {
		i := 1
		if isSQLWord(stmt[i], "unique") {
			i++
		}
		if i >= len(stmt) || !isSQLWord(stmt[i], "index") {
			return "", 0, false, nil
		}
		i++
		if i < len(stmt) && (isSQLWord(stmt[i], "concurrently") || isSQLWord(stmt[i], "nonconcurrently")) {
			i++
		}
		if i+2 < len(stmt) && isSQLWord(stmt[i], "if") && isSQLWord(stmt[i+1], "not") && isSQLWord(stmt[i+2], "exists") {
			i += 3
		}
		if i < len(stmt) && !isSQLWord(stmt[i], "on") {
			i++
		}
		if i >= len(stmt) || !isSQLWord(stmt[i], "on") {
			return "", 0, false, sqlPositionError(src, stmt[0].start, "CREATE INDEX has no ON clause")
		}
		i++
		if i < len(stmt) && isSQLWord(stmt[i], "only") {
			i++
		}
		table, next, err := parseQualifiedTableName(src, stmt, i)
		if err != nil {
			return "", 0, false, err
		}
		if next+1 < len(stmt) && isSQLWord(stmt[next], "using") {
			next += 2
		}
		if next >= len(stmt) || !isSQLPunct(stmt[next], "(") {
			return "", 0, false, sqlPositionError(src, stmt[0].start, "index on %s has no key list", table)
		}
		return table, next, true, nil
	}
	if !isSQLWord(stmt[0], "alter") || !isSQLWord(stmt[1], "table") {
		return "", 0, false, nil
	}
	i := 2
	if i+1 < len(stmt) && isSQLWord(stmt[i], "if") && isSQLWord(stmt[i+1], "exists") {
		i += 2
	}
	if i < len(stmt) && isSQLWord(stmt[i], "only") {
		i++
	}
	add := -1
	for j := i; j < len(stmt); j++ {
		if isSQLWord(stmt[j], "add") {
			add = j
			break
		}
	}
	if add < 0 {
		return "", 0, false, nil
	}
	next := add + 1
	if next+1 < len(stmt) && isSQLWord(stmt[next], "constraint") {
		next += 2
	}
	switch {
	case next+1 < len(stmt) && isSQLWord(stmt[next], "primary") && isSQLWord(stmt[next+1], "key"):
		next += 2
	case next < len(stmt) && isSQLWord(stmt[next], "unique"):
		next = skipUniqueNulls(stmt, next+1)
	default:
		return "", 0, false, nil
	}
	if next >= len(stmt) || !isSQLPunct(stmt[next], "(") {
		return "", 0, false, nil
	}
	table, _, err := parseQualifiedTableName(src, stmt, i)
	if err != nil {
		return "", 0, false, err
	}
	return table, next, true, nil
}

func skipUniqueNulls(stmt []sqlToken, i int) int {
	if i < len(stmt) && isSQLWord(stmt[i], "nulls") {
		if i+1 < len(stmt) && isSQLWord(stmt[i+1], "not") {
			return i + 3
		}
		return i + 2
	}
	return i
}

// rangeKeyEdits removes the trailing HASH marker from every element of the key list opening at stmt[open]. A plain
// column group such as (id) HASH is unwrapped to id, because PRIMARY KEY((id)) is not valid; an expression key such
// as ((1) HASH) keeps its parentheses.
func rangeKeyEdits(src string, stmt []sqlToken, open int) ([]sqlEdit, error) {
	closeIndex := matchingSQLParen(stmt, open)
	if closeIndex < 0 {
		return nil, sqlPositionError(src, stmt[open].start, "unterminated key list")
	}
	var edits []sqlEdit
	elementStart, depth := open+1, 0
	for i := open + 1; i <= closeIndex; i++ {
		boundary := i == closeIndex
		switch {
		case boundary:
		case isSQLPunct(stmt[i], "("):
			depth++
		case isSQLPunct(stmt[i], ")"):
			depth--
		case isSQLPunct(stmt[i], ",") && depth == 0:
			boundary = true
		}
		if !boundary {
			continue
		}
		edits = append(edits, hashElementEdits(stmt[elementStart:i])...)
		elementStart = i + 1
	}
	return edits, nil
}

func hashElementEdits(element []sqlToken) []sqlEdit {
	n := len(element)
	if n < 2 || !isSQLWord(element[n-1], "hash") {
		return nil
	}
	edits := []sqlEdit{{start: element[n-2].end, end: element[n-1].end}}
	if isSQLPunct(element[0], "(") && isSQLPunct(element[n-2], ")") && matchingSQLParen(element, 0) == n-2 && isPlainIdentifierList(element[1:n-2]) {
		edits = append(edits,
			sqlEdit{start: element[0].start, end: element[0].end},
			sqlEdit{start: element[n-2].start, end: element[n-2].end})
	}
	return edits
}

func isPlainIdentifierList(tokens []sqlToken) bool {
	if len(tokens) == 0 || len(tokens)%2 == 0 {
		return false
	}
	for i, tok := range tokens {
		if i%2 == 1 {
			if !isSQLPunct(tok, ",") {
				return false
			}
			continue
		}
		if tok.kind != sqlTokenWord && tok.kind != sqlTokenQuotedIdentifier {
			return false
		}
	}
	return true
}
