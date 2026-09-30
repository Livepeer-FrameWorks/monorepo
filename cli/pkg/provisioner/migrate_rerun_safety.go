package provisioner

import (
	"fmt"
	"strings"
)

// YugabyteDB applies a transactional migration file one statement at a time, in autocommit, and records it only after
// its last statement: with transactional DDL disabled (the 2026.1 default), a transaction that wrote rows is aborted
// by a later schema change on the same colocated database, and DDL is never rolled back with the transaction around
// it. A statement that failed, or an interrupted apply, leaves every statement before it applied and the item
// unrecorded, so the rerun executes the whole file again over that state. Every statement of a post-floor migration
// therefore has to succeed when its effect is already present, and validateMigrationRerunSafety enforces it:
//
//   - CREATE uses IF NOT EXISTS or OR REPLACE, or follows a DROP ... IF EXISTS of the same object in the same file;
//   - DROP uses IF EXISTS;
//   - ALTER TABLE ADD COLUMN uses IF NOT EXISTS, DROP COLUMN/CONSTRAINT uses IF EXISTS, and ADD CONSTRAINT (or ADD
//     PRIMARY KEY) follows a DROP CONSTRAINT IF EXISTS of that constraint on that table, earlier in the file or in the
//     same statement;
//   - nothing is renamed or moved to another schema;
//   - INSERT has ON CONFLICT or a NOT EXISTS guard, and UPDATE does not assign a column an expression that adds to,
//     subtracts from, or appends to the same column;
//   - DO blocks are accepted as written: their bodies must check the catalog before they change anything.
//
// Statements of shipped files that break the rule are executed under a guard with the same effect instead, listed in
// yugabyteRerunGuards; the guarded form is what the validator checks.

// rerunGuardKind names how a guarded statement is made rerun-safe.
type rerunGuardKind string

const (
	// rerunGuardConstraintAbsent runs ALTER TABLE <table> ADD CONSTRAINT <name> only while <table> has no constraint
	// named <name>, as IF NOT EXISTS would.
	rerunGuardConstraintAbsent rerunGuardKind = "constraint-absent"
	// rerunGuardDropTriggerIfExists executes DROP TRIGGER <name> ON <table> as DROP TRIGGER IF EXISTS.
	rerunGuardDropTriggerIfExists rerunGuardKind = "drop-trigger-if-exists"
	// rerunGuardIndexRenamePending guards the pair DROP INDEX IF EXISTS <schema>.<new>; ALTER INDEX <schema>.<old>
	// RENAME TO <new>, named by the rename: both run only while <schema>.<old> exists, so a rerun after the rename
	// keeps the renamed index instead of dropping it and then failing on the missing old name.
	rerunGuardIndexRenamePending rerunGuardKind = "index-rename-pending"
)

// rerunGuard guards one statement (1-based, in the file's statement order) of a shipped migration.
type rerunGuard struct {
	statement int
	kind      rerunGuardKind
}

// rerunGuardedFile pins the guards of a shipped migration to the file's checksum: the file is immutable, so a
// different checksum means the guards no longer describe it.
type rerunGuardedFile struct {
	checksum string
	guards   []rerunGuard
}

// yugabyteRerunGuards lists the statements of shipped migrations that cannot be executed twice as written. Files in a
// shipped or release-candidate tag are immutable, so YugabyteDB executes these statements under an equivalent guard.
// New migrations must be rerun-safe as written; validateMigrationRerunSafety rejects a guard that no longer matches
// its statement, or one whose statement is already rerun-safe.
var yugabyteRerunGuards = map[string]rerunGuardedFile{
	"migrations/commodore/v0.3.11/expand/019_restream_media_authority_refresh.sql": {
		checksum: "c2ef4efa3f9dddcaf59826b0ce2b6bc8d157762f6932ab3830484e0a54b16849",
		guards:   []rerunGuard{{2, rerunGuardDropTriggerIfExists}, {4, rerunGuardDropTriggerIfExists}},
	},
	"migrations/foghorn/v0.3.0/contract/003_finalize_admission_state_indexes.sql": {
		checksum: "f904b46e3bc66441b3478f6597d19455f019aa50dcd226e03d29ee016fe04e1f",
		guards:   []rerunGuard{{2, rerunGuardIndexRenamePending}, {4, rerunGuardIndexRenamePending}, {6, rerunGuardIndexRenamePending}},
	},
	"migrations/purser/v0.3.0/expand/001_dimensioned_usage_reports.sql": {
		checksum: "2a1e5972c3d47d78e98d4e31845cfca67e13eeeb58f5bb8122f8e95325d484ac",
		guards:   []rerunGuard{{3, rerunGuardConstraintAbsent}, {5, rerunGuardConstraintAbsent}},
	},
	"migrations/purser/v0.3.0/expand/003_crypto_payment_safety.sql": {
		checksum: "7ea988c746f9e013e8429921f2480b48cc07dbf41d6a0f82a8d9a6a7b3f3c544",
		guards:   []rerunGuard{{1, rerunGuardConstraintAbsent}},
	},
	"migrations/purser/v0.3.0/contract/003_crypto_payment_safety.sql": {
		checksum: "eb9176bac38774e217443e438c8d3d4e31109e231da4f27cc7e67d1f32fed19e",
		guards:   []rerunGuard{{2, rerunGuardConstraintAbsent}},
	},
}

// rerunGuardDollarTags quote a guarded statement and the block that runs it. A statement containing either tag cannot
// be guarded.
const (
	rerunGuardBlockTag     = "$frameworks_rerun_guard$"
	rerunGuardStatementTag = "$frameworks_rerun_statement$"
)

// applyRerunGuards returns the statements of m with its listed guards applied. statements holds the text of each
// statement and tokens its tokens, in file order.
func applyRerunGuards(m Migration, statements []string, tokens [][]sqlToken) ([]string, error) {
	file, ok := yugabyteRerunGuards[m.Path]
	if !ok {
		return statements, nil
	}
	if file.checksum != m.Checksum {
		return nil, fmt.Errorf("rerun guards are pinned to checksum %s but the file has %s; shipped migrations are immutable", file.checksum, m.Checksum)
	}
	out := append([]string(nil), statements...)
	for _, guard := range file.guards {
		index := guard.statement - 1
		if index < 0 || index >= len(statements) {
			return nil, fmt.Errorf("rerun guard names statement %d of %d", guard.statement, len(statements))
		}
		switch guard.kind {
		case rerunGuardConstraintAbsent:
			table, name, err := singleAddConstraint(tokens[index])
			if err != nil {
				return nil, fmt.Errorf("rerun guard on statement %d: %w", guard.statement, err)
			}
			condition := fmt.Sprintf("NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = to_regclass(%s) AND conname = %s)", sqlLiteral(table), sqlLiteral(name))
			if out[index], err = guardedStatement(condition, statements[index]); err != nil {
				return nil, fmt.Errorf("rerun guard on statement %d: %w", guard.statement, err)
			}
		case rerunGuardDropTriggerIfExists:
			words := statementWords(tokens[index])
			if len(words) < 5 || words[0] != "drop" || words[1] != "trigger" || words[2] == "if" {
				return nil, fmt.Errorf("rerun guard on statement %d: want DROP TRIGGER <name> ON <table> without IF EXISTS", guard.statement)
			}
			// The statement text starts at its first token, so IF EXISTS goes right after DROP TRIGGER.
			triggerEnd := tokens[index][1].end - tokens[index][0].start
			out[index] = statements[index][:triggerEnd] + " IF EXISTS" + statements[index][triggerEnd:]
		case rerunGuardIndexRenamePending:
			source, target, err := indexRename(tokens[index])
			if err != nil {
				return nil, fmt.Errorf("rerun guard on statement %d: %w", guard.statement, err)
			}
			if index == 0 {
				return nil, fmt.Errorf("rerun guard on statement %d: no DROP INDEX precedes the rename", guard.statement)
			}
			dropped, err := dropIndexIfExists(tokens[index-1])
			if err != nil || dropped != schemaOf(source)+"."+target {
				return nil, fmt.Errorf("rerun guard on statement %d: the statement before the rename must be DROP INDEX IF EXISTS %s.%s", guard.statement, schemaOf(source), target)
			}
			condition := fmt.Sprintf("to_regclass(%s) IS NOT NULL", sqlLiteral(source))
			for _, at := range []int{index - 1, index} {
				if out[at], err = guardedStatement(condition, statements[at]); err != nil {
					return nil, fmt.Errorf("rerun guard on statement %d: %w", at+1, err)
				}
			}
		default:
			return nil, fmt.Errorf("unknown rerun guard %q", guard.kind)
		}
	}
	return out, nil
}

// guardedStatement runs statement only while condition holds, in one DO block.
func guardedStatement(condition, statement string) (string, error) {
	if strings.Contains(statement, rerunGuardBlockTag) || strings.Contains(statement, rerunGuardStatementTag) {
		return "", fmt.Errorf("statement contains a guard quoting tag")
	}
	return "DO " + rerunGuardBlockTag + "\nBEGIN\n  IF " + condition + " THEN\n    EXECUTE " +
		rerunGuardStatementTag + statement + rerunGuardStatementTag + ";\n  END IF;\nEND\n" + rerunGuardBlockTag, nil
}

func sqlLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func schemaOf(qualified string) string {
	if dot := strings.LastIndex(qualified, "."); dot >= 0 {
		return qualified[:dot]
	}
	return ""
}

// statementWords returns the lowercased words and quoted identifiers of a statement, with punctuation, strings and
// dollar bodies as "" placeholders, so positions still line up with the tokens.
func statementWords(tokens []sqlToken) []string {
	words := make([]string, len(tokens))
	for i, tok := range tokens {
		switch tok.kind {
		case sqlTokenWord, sqlTokenQuotedIdentifier:
			words[i] = tok.text
		case sqlTokenPunct:
			words[i] = tok.text
		}
	}
	return words
}

// qualifiedName reads [schema.]name at tokens[i:] and returns it with the index after it.
func qualifiedName(tokens []sqlToken, i int) (string, int, bool) {
	ident := func(at int) bool {
		return at < len(tokens) && (tokens[at].kind == sqlTokenWord || tokens[at].kind == sqlTokenQuotedIdentifier)
	}
	if !ident(i) {
		return "", i, false
	}
	name := tokens[i].text
	i++
	for i+1 < len(tokens) && isSQLPunct(tokens[i], ".") && ident(i+1) {
		name += "." + tokens[i+1].text
		i += 2
	}
	return name, i, true
}

func unqualified(name string) string {
	if dot := strings.LastIndex(name, "."); dot >= 0 {
		return name[dot+1:]
	}
	return name
}

// alterTableTarget reads ALTER TABLE [IF EXISTS] [ONLY] <table> and returns the table and the index of its first
// subcommand.
func alterTableTarget(tokens []sqlToken) (string, int, bool) {
	if len(tokens) < 3 || !isSQLWord(tokens[0], "alter") || !isSQLWord(tokens[1], "table") {
		return "", 0, false
	}
	i := 2
	if i+1 < len(tokens) && isSQLWord(tokens[i], "if") && isSQLWord(tokens[i+1], "exists") {
		i += 2
	}
	if i < len(tokens) && isSQLWord(tokens[i], "only") {
		i++
	}
	return qualifiedName(tokens, i)
}

// singleAddConstraint recognizes ALTER TABLE <table> ADD CONSTRAINT <name> ... with no other subcommand.
func singleAddConstraint(tokens []sqlToken) (string, string, error) {
	table, i, ok := alterTableTarget(tokens)
	if !ok {
		return "", "", fmt.Errorf("want ALTER TABLE <table> ADD CONSTRAINT <name>")
	}
	subcommands := splitTopLevel(tokens[i:], ",")
	if len(subcommands) != 1 {
		return "", "", fmt.Errorf("want one ADD CONSTRAINT subcommand, found %d subcommands", len(subcommands))
	}
	sub := subcommands[0]
	if len(sub) < 3 || !isSQLWord(sub[0], "add") || !isSQLWord(sub[1], "constraint") {
		return "", "", fmt.Errorf("want ALTER TABLE <table> ADD CONSTRAINT <name>")
	}
	return table, sub[2].text, nil
}

// indexRename recognizes ALTER INDEX [IF EXISTS] <schema.old> RENAME TO <new>.
func indexRename(tokens []sqlToken) (string, string, error) {
	if len(tokens) < 3 || !isSQLWord(tokens[0], "alter") || !isSQLWord(tokens[1], "index") {
		return "", "", fmt.Errorf("want ALTER INDEX <schema.old> RENAME TO <new>")
	}
	i := 2
	if i+1 < len(tokens) && isSQLWord(tokens[i], "if") && isSQLWord(tokens[i+1], "exists") {
		i += 2
	}
	source, i, ok := qualifiedName(tokens, i)
	if !ok || schemaOf(source) == "" || i+3 != len(tokens) || !isSQLWord(tokens[i], "rename") || !isSQLWord(tokens[i+1], "to") {
		return "", "", fmt.Errorf("want ALTER INDEX <schema.old> RENAME TO <new>")
	}
	return source, tokens[i+2].text, nil
}

// dropIndexIfExists recognizes DROP INDEX IF EXISTS <schema.name>.
func dropIndexIfExists(tokens []sqlToken) (string, error) {
	if len(tokens) < 5 || !isSQLWord(tokens[0], "drop") || !isSQLWord(tokens[1], "index") || !isSQLWord(tokens[2], "if") || !isSQLWord(tokens[3], "exists") {
		return "", fmt.Errorf("want DROP INDEX IF EXISTS <schema.name>")
	}
	name, i, ok := qualifiedName(tokens, 4)
	if !ok || i != len(tokens) {
		return "", fmt.Errorf("want DROP INDEX IF EXISTS <schema.name>")
	}
	return name, nil
}

// splitTopLevel splits tokens on sep outside parentheses.
func splitTopLevel(tokens []sqlToken, sep string) [][]sqlToken {
	var parts [][]sqlToken
	depth, start := 0, 0
	for i, tok := range tokens {
		if tok.kind != sqlTokenPunct {
			continue
		}
		switch tok.text {
		case "(":
			depth++
		case ")":
			depth--
		case sep:
			if depth == 0 {
				parts = append(parts, tokens[start:i])
				start = i + 1
			}
		}
	}
	return append(parts, tokens[start:])
}

// rerunDrops records what earlier statements of a file dropped with IF EXISTS.
type rerunDrops map[string]bool

func (d rerunDrops) add(kind, name string)      { d[kind+"\x00"+name] = true }
func (d rerunDrops) has(kind, name string) bool { return d[kind+"\x00"+name] }

// constraintKey and triggerKey name a table's constraint or trigger by the table's unqualified name, so a drop and a
// later add match whether or not either statement qualifies the table.
func constraintKey(table, name string) string { return unqualified(table) + "." + name }
func triggerKey(table, trigger string) string { return unqualified(table) + "." + trigger }

func hasWordPair(tokens []sqlToken, a, b string) bool {
	for i := 0; i+1 < len(tokens); i++ {
		if isSQLWord(tokens[i], a) && isSQLWord(tokens[i+1], b) {
			return true
		}
	}
	return false
}

func hasWord(tokens []sqlToken, word string) bool {
	for _, tok := range tokens {
		if isSQLWord(tok, word) {
			return true
		}
	}
	return false
}

// rerunSafetyProblem returns why a statement cannot be executed again after it already applied, or "" when it can.
// drops holds the objects earlier statements of the file dropped with IF EXISTS; the statement's own drops are added.
func rerunSafetyProblem(tokens []sqlToken, drops rerunDrops) string {
	if len(tokens) == 0 {
		return ""
	}
	switch {
	case tokens[0].kind != sqlTokenWord:
		return "statement is not recognized as rerun-safe"
	case isSQLWord(tokens[0], "do"), isSQLWord(tokens[0], "select"), isSQLWord(tokens[0], "set"), isSQLWord(tokens[0], "reset"),
		isSQLWord(tokens[0], "comment"), isSQLWord(tokens[0], "grant"), isSQLWord(tokens[0], "revoke"), isSQLWord(tokens[0], "delete"),
		isSQLWord(tokens[0], "analyze"), isSQLWord(tokens[0], "refresh"), isSQLWord(tokens[0], "truncate"):
		return ""
	case isSQLWord(tokens[0], "with"):
		return rerunSafetyOfWith(tokens)
	case isSQLWord(tokens[0], "insert"):
		return rerunSafetyOfInsert(tokens)
	case isSQLWord(tokens[0], "update"):
		return rerunSafetyOfUpdate(tokens)
	case isSQLWord(tokens[0], "create"):
		return rerunSafetyOfCreate(tokens, drops)
	case isSQLWord(tokens[0], "drop"):
		return rerunSafetyOfDrop(tokens, drops)
	case isSQLWord(tokens[0], "alter"):
		return rerunSafetyOfAlter(tokens, drops)
	}
	return fmt.Sprintf("%s statements are not recognized as rerun-safe", strings.ToUpper(tokens[0].text))
}

// rerunSafetyOfWith checks the statement a WITH clause leads into, and every INSERT or UPDATE among its queries.
func rerunSafetyOfWith(tokens []sqlToken) string {
	depth := 0
	for i, tok := range tokens {
		switch {
		case isSQLPunct(tok, "("):
			depth++
		case isSQLPunct(tok, ")"):
			depth--
		case depth == 0 && (isSQLWord(tok, "insert") || isSQLWord(tok, "update")):
			if isSQLWord(tok, "insert") {
				return rerunSafetyOfInsert(tokens[i:])
			}
			return rerunSafetyOfUpdate(tokens[i:])
		case depth == 0 && (isSQLWord(tok, "select") || isSQLWord(tok, "delete")):
			if hasWord(tokens[:i], "insert") {
				return "INSERT inside WITH is not recognized as rerun-safe"
			}
			return ""
		}
	}
	return "WITH statement is not recognized as rerun-safe"
}

func rerunSafetyOfInsert(tokens []sqlToken) string {
	if hasWordPair(tokens, "on", "conflict") || hasWordPair(tokens, "not", "exists") {
		return ""
	}
	return "INSERT must use ON CONFLICT or a NOT EXISTS guard so a rerun does not insert twice"
}

// rerunSafetyOfUpdate rejects an assignment that adds to, subtracts from, or appends to the column it assigns, the
// forms that change the row again on every execution.
func rerunSafetyOfUpdate(tokens []sqlToken) string {
	set := -1
	depth := 0
	for i, tok := range tokens {
		switch {
		case isSQLPunct(tok, "("):
			depth++
		case isSQLPunct(tok, ")"):
			depth--
		case depth == 0 && isSQLWord(tok, "set"):
			set = i
		}
		if set >= 0 {
			break
		}
	}
	if set < 0 {
		return "UPDATE without SET is not recognized as rerun-safe"
	}
	end := len(tokens)
	depth = 0
	for i := set + 1; i < len(tokens); i++ {
		switch {
		case isSQLPunct(tokens[i], "("):
			depth++
		case isSQLPunct(tokens[i], ")"):
			depth--
		case depth == 0 && (isSQLWord(tokens[i], "from") || isSQLWord(tokens[i], "where") || isSQLWord(tokens[i], "returning")):
			end = i
		}
		if end != len(tokens) {
			break
		}
	}
	for _, assignment := range splitTopLevel(tokens[set+1:end], ",") {
		if len(assignment) < 3 || !isSQLPunct(assignment[1], "=") {
			continue
		}
		column := assignment[0].text
		expression := assignment[2:]
		for i, tok := range expression {
			if tok.kind != sqlTokenWord && tok.kind != sqlTokenQuotedIdentifier || tok.text != column {
				continue
			}
			if i > 0 && isSQLPunct(expression[i-1], ".") {
				continue
			}
			if updateStepsColumn(expression, i) {
				return fmt.Sprintf("UPDATE steps %s by a literal, which changes the row again on a rerun", column)
			}
		}
	}
	return ""
}

// updateStepsColumn reports whether the column at expression[at] is combined with a number or string literal by +,
// -, or ||, as in n = n + 1 or s = s || 'x'. The operand types are unknown here, so a jsonb key removal
// (features - ARRAY[...]) or a column-to-column expression is not reported; its idempotence is the author's to keep.
func updateStepsColumn(expression []sqlToken, at int) bool {
	literal := func(i int) bool {
		return i >= 0 && i < len(expression) && (expression[i].kind == sqlTokenNumber || expression[i].kind == sqlTokenString)
	}
	operator := func(i, direction int) (int, bool) {
		if i < 0 || i >= len(expression) || expression[i].kind != sqlTokenPunct {
			return 0, false
		}
		switch expression[i].text {
		case "+", "-":
			return i + direction, true
		case "|":
			next := i + direction
			if next >= 0 && next < len(expression) && isSQLPunct(expression[next], "|") {
				return next + direction, true
			}
		}
		return 0, false
	}
	if operand, ok := operator(at+1, 1); ok && literal(operand) {
		return true
	}
	if operand, ok := operator(at-1, -1); ok && literal(operand) {
		return true
	}
	return false
}

func rerunSafetyOfCreate(tokens []sqlToken, drops rerunDrops) string {
	i := 1
	if i+1 < len(tokens) && isSQLWord(tokens[i], "or") && isSQLWord(tokens[i+1], "replace") {
		return ""
	}
	for i < len(tokens) && (isSQLWord(tokens[i], "unique") || isSQLWord(tokens[i], "constraint") || isSQLWord(tokens[i], "materialized")) {
		i++
	}
	if i >= len(tokens) {
		return "CREATE statement is not recognized as rerun-safe"
	}
	kind := tokens[i].text
	i++
	if kind == "index" && i < len(tokens) && isSQLWord(tokens[i], "concurrently") {
		i++
	}
	if i+2 < len(tokens) && isSQLWord(tokens[i], "if") && isSQLWord(tokens[i+1], "not") && isSQLWord(tokens[i+2], "exists") {
		return ""
	}
	name, next, ok := qualifiedName(tokens, i)
	if !ok {
		return fmt.Sprintf("CREATE %s must use IF NOT EXISTS", strings.ToUpper(kind))
	}
	switch kind {
	case "trigger":
		for j := next; j+1 < len(tokens); j++ {
			if isSQLWord(tokens[j], "on") {
				if table, _, found := qualifiedName(tokens, j+1); found && drops.has("trigger", triggerKey(table, name)) {
					return ""
				}
				break
			}
		}
		return fmt.Sprintf("CREATE TRIGGER %s must follow DROP TRIGGER IF EXISTS %s ON its table, or use CREATE OR REPLACE", name, name)
	case "index", "view", "function", "procedure", "type", "domain", "policy":
		if drops.has(kind, unqualified(name)) {
			return ""
		}
		return fmt.Sprintf("CREATE %s %s must use IF NOT EXISTS or OR REPLACE, or follow DROP %s IF EXISTS %s", strings.ToUpper(kind), name, strings.ToUpper(kind), name)
	}
	return fmt.Sprintf("CREATE %s must use IF NOT EXISTS", strings.ToUpper(kind))
}

func rerunSafetyOfDrop(tokens []sqlToken, drops rerunDrops) string {
	i := 1
	for i < len(tokens) && (isSQLWord(tokens[i], "materialized") || isSQLWord(tokens[i], "foreign")) {
		i++
	}
	if i >= len(tokens) {
		return "DROP statement is not recognized as rerun-safe"
	}
	kind := tokens[i].text
	i++
	if i < len(tokens) && isSQLWord(tokens[i], "concurrently") {
		i++
	}
	if i+1 >= len(tokens) || !isSQLWord(tokens[i], "if") || !isSQLWord(tokens[i+1], "exists") {
		return fmt.Sprintf("DROP %s must use IF EXISTS", strings.ToUpper(kind))
	}
	name, next, ok := qualifiedName(tokens, i+2)
	if !ok {
		return ""
	}
	if kind == "trigger" {
		if next+1 < len(tokens) && isSQLWord(tokens[next], "on") {
			if table, _, found := qualifiedName(tokens, next+1); found {
				drops.add("trigger", triggerKey(table, name))
			}
		}
		return ""
	}
	drops.add(kind, unqualified(name))
	return ""
}

func rerunSafetyOfAlter(tokens []sqlToken, drops rerunDrops) string {
	if len(tokens) < 2 {
		return "ALTER statement is not recognized as rerun-safe"
	}
	if !isSQLWord(tokens[1], "table") {
		if hasWord(tokens, "rename") {
			return fmt.Sprintf("ALTER %s ... RENAME fails when rerun after the rename", strings.ToUpper(tokens[1].text))
		}
		if hasWordPair(tokens, "set", "schema") {
			return "SET SCHEMA fails when rerun after the move"
		}
		if isSQLWord(tokens[1], "type") && hasWordPair(tokens, "add", "value") && !hasWordPair(tokens, "not", "exists") {
			return "ALTER TYPE ... ADD VALUE must use IF NOT EXISTS"
		}
		return ""
	}
	table, i, ok := alterTableTarget(tokens)
	if !ok {
		return "ALTER TABLE statement is not recognized as rerun-safe"
	}
	for _, sub := range splitTopLevel(tokens[i:], ",") {
		if problem := rerunSafetyOfAlterTableSubcommand(table, sub, drops); problem != "" {
			return problem
		}
	}
	return ""
}

func rerunSafetyOfAlterTableSubcommand(table string, sub []sqlToken, drops rerunDrops) string {
	if len(sub) == 0 {
		return "empty ALTER TABLE subcommand"
	}
	switch {
	case isSQLWord(sub[0], "add"):
		rest := sub[1:]
		if len(rest) > 0 && isSQLWord(rest[0], "column") {
			rest = rest[1:]
		}
		if len(rest) >= 3 && isSQLWord(rest[0], "if") && isSQLWord(rest[1], "not") && isSQLWord(rest[2], "exists") {
			return ""
		}
		if len(rest) >= 2 && isSQLWord(rest[0], "constraint") {
			name := rest[1].text
			if drops.has("constraint", constraintKey(table, name)) {
				return ""
			}
			return fmt.Sprintf("ADD CONSTRAINT %s on %s must follow DROP CONSTRAINT IF EXISTS %s on the same table", name, table, name)
		}
		if len(rest) >= 2 && isSQLWord(rest[0], "primary") && isSQLWord(rest[1], "key") {
			if drops.has("constraint", constraintKey(table, unqualified(table)+"_pkey")) {
				return ""
			}
			return fmt.Sprintf("ADD PRIMARY KEY on %s must follow DROP CONSTRAINT IF EXISTS %s_pkey", table, unqualified(table))
		}
		if len(rest) > 0 && (isSQLWord(rest[0], "unique") || isSQLWord(rest[0], "check") || isSQLWord(rest[0], "foreign") || isSQLWord(rest[0], "exclude")) {
			return fmt.Sprintf("an unnamed constraint on %s is added again on every rerun; name it and drop it with IF EXISTS first", table)
		}
		return fmt.Sprintf("ADD COLUMN on %s must use IF NOT EXISTS", table)
	case isSQLWord(sub[0], "drop"):
		rest := sub[1:]
		if len(rest) > 0 && (isSQLWord(rest[0], "column") || isSQLWord(rest[0], "constraint")) {
			kind := rest[0].text
			if len(rest) < 4 || !isSQLWord(rest[1], "if") || !isSQLWord(rest[2], "exists") {
				return fmt.Sprintf("DROP %s on %s must use IF EXISTS", strings.ToUpper(kind), table)
			}
			if kind == "constraint" {
				drops.add("constraint", constraintKey(table, rest[3].text))
			}
			return ""
		}
		if len(rest) >= 2 && isSQLWord(rest[0], "if") && isSQLWord(rest[1], "exists") {
			return ""
		}
		return fmt.Sprintf("DROP COLUMN on %s must use IF EXISTS", table)
	case isSQLWord(sub[0], "rename"):
		return fmt.Sprintf("RENAME on %s fails when rerun after the rename", table)
	case isSQLWord(sub[0], "set") && len(sub) > 1 && isSQLWord(sub[1], "schema"):
		return fmt.Sprintf("SET SCHEMA on %s fails when rerun after the move", table)
	case isSQLWord(sub[0], "alter"), isSQLWord(sub[0], "validate"), isSQLWord(sub[0], "set"), isSQLWord(sub[0], "reset"),
		isSQLWord(sub[0], "enable"), isSQLWord(sub[0], "disable"), isSQLWord(sub[0], "owner"), isSQLWord(sub[0], "replica"),
		isSQLWord(sub[0], "force"), isSQLWord(sub[0], "no"):
		return ""
	}
	return fmt.Sprintf("ALTER TABLE %s %s is not recognized as rerun-safe", table, strings.ToUpper(sub[0].text))
}

// migrationStatementTexts splits content into statements with the tokenizer the YugabyteDB layout rewriter uses and
// returns each statement's text and tokens.
func migrationStatementTexts(content string) ([]string, [][]sqlToken, error) {
	statements, err := sqlStatements(content)
	if err != nil {
		return nil, nil, err
	}
	texts := make([]string, 0, len(statements))
	for _, statement := range statements {
		texts = append(texts, strings.TrimSpace(content[statement[0].start:statement[len(statement)-1].end]))
	}
	return texts, statements, nil
}

// validateMigrationRerunSafety requires every statement of every post-floor PostgreSQL-family migration to be
// rerun-safe as YugabyteDB executes it, and every listed guard to match a shipped file and a statement that needs it.
func validateMigrationRerunSafety(migrations []Migration) []MigrationValidationIssue {
	var issues []MigrationValidationIssue
	for _, migration := range migrations {
		if !enforcesMigrationPhaseSafety(migration) {
			if _, guarded := yugabyteRerunGuards[migration.Path]; guarded {
				issues = append(issues, MigrationValidationIssue{Path: migration.Path, Message: "rerun guards are listed for a migration below the baseline floor, which is never applied"})
			}
			continue
		}
		texts, tokens, err := migrationStatementTexts(migration.content)
		if err != nil {
			issues = append(issues, MigrationValidationIssue{Path: migration.Path, Message: err.Error()})
			continue
		}
		guarded, err := applyRerunGuards(migration, texts, tokens)
		if err != nil {
			issues = append(issues, MigrationValidationIssue{Path: migration.Path, Message: err.Error()})
			continue
		}
		asWritten := make([]string, len(tokens))
		written := rerunDrops{}
		for i := range tokens {
			asWritten[i] = rerunSafetyProblem(tokens[i], written)
		}
		for _, guard := range yugabyteRerunGuards[migration.Path].guards {
			if asWritten[guard.statement-1] == "" {
				issues = append(issues, MigrationValidationIssue{Path: migration.Path, Message: fmt.Sprintf("statement %d is rerun-safe as written; remove its entry from yugabyteRerunGuards", guard.statement)})
			}
		}
		checked := rerunDrops{}
		for i, text := range guarded {
			statementTokens := tokens[i]
			if text != texts[i] {
				if statementTokens, err = tokenizeSQL(text); err != nil {
					issues = append(issues, MigrationValidationIssue{Path: migration.Path, Message: fmt.Sprintf("guarded statement %d: %v", i+1, err)})
					continue
				}
			}
			if problem := rerunSafetyProblem(statementTokens, checked); problem != "" {
				issues = append(issues, MigrationValidationIssue{
					Path:    migration.Path,
					Message: fmt.Sprintf("statement %d is not rerun-safe: %s. YugabyteDB applies a migration statement by statement and reruns every statement of an unrecorded item", i+1, problem),
				})
			}
		}
	}
	return issues
}

// validateRerunGuardTargets requires every file yugabyteRerunGuards lists to be one of the complete embedded set.
func validateRerunGuardTargets(migrations []Migration) []MigrationValidationIssue {
	present := make(map[string]bool, len(migrations))
	for _, migration := range migrations {
		present[migration.Path] = true
	}
	var issues []MigrationValidationIssue
	for path := range yugabyteRerunGuards {
		if !present[path] {
			issues = append(issues, MigrationValidationIssue{Path: path, Message: "rerun guards are listed for a migration that does not exist"})
		}
	}
	return issues
}
