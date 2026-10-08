package main

import (
	"fmt"
	"math/rand"
	"regexp"
	"strings"
	"time"
)

// A parameter is bound from the seeded rows when the statement compares it with a column (alias.col = $n,
// $n = col, col IN (..$n..), col = ANY($n), an INSERT column list position, SET col = $n) or when its sqlc/Go
// argument name is a column of a table the statement reads. Range comparisons on timestamps bind the current
// time (<, <=) or one day ago (>, >=), LIMIT binds 100 and OFFSET 0. Everything else binds a synthetic value
// of the parameter's type, recorded as such.

type paramBinding struct {
	Index  int    `json:"index"`
	Type   string `json:"type"`
	Source string `json:"source"` // column table.col, name hint, limit, synthetic
	Value  string `json:"value,omitempty"`
	Null   bool   `json:"null,omitempty"`
}

var (
	tableRefRe  = regexp.MustCompile(`(?i)\b(?:FROM|JOIN|UPDATE|INTO|USING)\s+(?:ONLY\s+)?([a-z_][a-z0-9_]*)\.([a-z_][a-z0-9_]*)(?:\s+(?:AS\s+)?([a-z_][a-z0-9_]*))?`)
	sqlKeywords = map[string]bool{"where": true, "on": true, "set": true, "left": true, "right": true, "inner": true, "outer": true, "join": true,
		"cross": true, "full": true, "natural": true, "using": true, "group": true, "order": true, "limit": true, "returning": true,
		"values": true, "select": true, "as": true, "and": true, "or": true, "for": true, "union": true, "lateral": true, "default": true,
		"offset": true, "having": true, "window": true, "except": true, "intersect": true, "do": true, "when": true, "then": true}
	colRef   = `(?:([a-z_][a-z0-9_]*)\.)?([a-z_][a-z0-9_]*)`
	castRe   = `(?:\s*::\s*[a-z_][a-z0-9_ ]*(?:\([0-9, ]*\))?(?:\[\])?)?`
	opRe     = `(=|<>|!=|<=|>=|<|>|@>|<@|&&|(?i:\bI?LIKE\b)|(?i:\bIS\s+(?:NOT\s+)?DISTINCT\s+FROM\b))`
	limitRe  = regexp.MustCompile(`(?i)\b(LIMIT|OFFSET)\s+\(?\s*\$(\d+)`)
	insertRe = regexp.MustCompile(`(?is)INSERT\s+INTO\s+([a-z_][a-z0-9_]*)\.([a-z_][a-z0-9_]*)(?:\s+(?:AS\s+)?[a-z_][a-z0-9_]*)?\s*\(([^)]*)\)\s*VALUES\s*\(`)
	paramRe  = regexp.MustCompile(`\$(\d+)`)
)

type bindRef struct {
	qualifier, column, op string
}

// paramRefs finds, for each parameter number, the columns the statement compares it with.
func paramRefs(text string, count int) map[int][]bindRef {
	refs := map[int][]bindRef{}
	lower := strings.ToLower(text)
	for n := 1; n <= count; n++ {
		p := fmt.Sprintf(`\$%d\b`, n)
		left := regexp.MustCompile(colRef + castRe + `\s*` + opRe + `\s*(?:(?i:ANY|ALL)\s*\(\s*)?\(?\s*` + p)
		for _, m := range left.FindAllStringSubmatch(lower, -1) {
			refs[n] = append(refs[n], bindRef{qualifier: m[1], column: m[2], op: m[3]})
		}
		right := regexp.MustCompile(p + castRe + `\s*\)?\s*` + opRe + `\s*(?:(?i:ANY|ALL)\s*\(\s*)?` + colRef + `\b\s*([^(\s]|$)`)
		for _, m := range right.FindAllStringSubmatch(lower, -1) {
			refs[n] = append(refs[n], bindRef{qualifier: m[2], column: m[3], op: flipOp(m[1])})
		}
		in := regexp.MustCompile(colRef + `\s+(?:not\s+)?in\s*\([^()]*` + p)
		for _, m := range in.FindAllStringSubmatch(lower, -1) {
			refs[n] = append(refs[n], bindRef{qualifier: m[1], column: m[2], op: "="})
		}
	}
	// INSERT ... (cols) VALUES (exprs): a value expression that is just $n (with casts) binds its column.
	if m := insertRe.FindStringSubmatchIndex(lower); m != nil {
		cols := strings.Split(lower[m[6]:m[7]], ",")
		exprs := splitTopLevel(lower[m[1]:])
		for i, expr := range exprs {
			if i >= len(cols) {
				break
			}
			if pm := paramRe.FindStringSubmatch(expr); pm != nil {
				n := catalogInteger(pm[1])
				refs[n] = append(refs[n], bindRef{qualifier: lower[m[4]:m[5]], column: strings.TrimSpace(cols[i]), op: "="})
			}
		}
	}
	return refs
}

func flipOp(op string) string {
	switch op {
	case "<":
		return ">"
	case ">":
		return "<"
	case "<=":
		return ">="
	case ">=":
		return "<="
	}
	return op
}

// splitTopLevel splits the VALUES list that starts after the opening parenthesis until its closing one.
func splitTopLevel(s string) []string {
	depth := 0
	var parts []string
	start := 0
	inQuote := false
	for i, r := range s {
		switch {
		case r == '\'':
			inQuote = !inQuote
		case inQuote:
		case r == '(':
			depth++
		case r == ')':
			if depth == 0 {
				return append(parts, s[start:i])
			}
			depth--
		case r == ',' && depth == 0:
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	return parts
}

// tableAliases maps every alias and table name the statement uses to the qualified table.
func tableAliases(text string, tables map[string]*table) (map[string]string, []string) {
	aliases := map[string]string{}
	var referenced []string
	seen := map[string]bool{}
	for _, m := range tableRefRe.FindAllStringSubmatch(strings.ToLower(text), -1) {
		qname := m[1] + "." + m[2]
		if tables[qname] == nil {
			continue
		}
		aliases[m[2]] = qname
		if m[3] != "" && !sqlKeywords[m[3]] {
			aliases[m[3]] = qname
		}
		if !seen[qname] {
			seen[qname] = true
			referenced = append(referenced, qname)
		}
	}
	return aliases, referenced
}

var limitNames = regexp.MustCompile(`^(limit|lim|batch_size|batch|max_rows|page_size|max_items|max_results|row_limit|max_batch|n|count_limit|max_count|size)$`)

var durationNames = regexp.MustCompile(`(threshold|stale|seconds|secs|_ms$|millis|ttl|timeout|lease|retention|window|interval|age|grace|older_than|max_age)`)

// durationUse reports whether parameter n is used as a number of seconds: $n * INTERVAL '1 second',
// make_interval(secs => $n) or ($n || ' seconds')::interval.
func durationUse(text string, n int) bool {
	p := fmt.Sprintf(`\$%d\b`, n)
	re := regexp.MustCompile(`(?i)(` + p + castRe + `\s*\*\s*interval\s*'1 second'|secs\s*=>\s*` + p + `|` + p + castRe + `\s*\|\|\s*' ?seconds?')`)
	return re.MatchString(text)
}

type binder struct {
	s            *seeder
	queryID      string
	now          time.Time
	insertTarget string
	upsert       bool
}

var insertTargetRe = regexp.MustCompile(`(?is)\binsert\s+into\s+([a-z_][a-z0-9_]*)\.([a-z_][a-z0-9_]*)`)

func (b *binder) bind(text string, names []string, types []string) []paramBinding {
	aliases, referenced := tableAliases(text, b.s.tables)
	if m := insertTargetRe.FindStringSubmatch(strings.ToLower(text)); m != nil {
		b.insertTarget = m[1] + "." + m[2]
		b.upsert = strings.Contains(strings.ToLower(text), "on conflict")
	}
	refs := paramRefs(text, len(types))
	limits := map[int]string{}
	for _, m := range limitRe.FindAllStringSubmatch(text, -1) {
		n := catalogInteger(m[2])
		limits[n] = strings.ToUpper(m[1])
	}
	out := make([]paramBinding, len(types))
	for i, typ := range types {
		n := i + 1
		pb := paramBinding{Index: n, Type: typ}
		if name := nameAt(names, i); name == "claim_bucket" || name == "claim_buckets" {
			pb.Source, pb.Value = "name "+name, "0"
			if name == "claim_buckets" {
				pb.Value = "1"
			}
			out[i] = pb
			continue
		}
		if kind, ok := limits[n]; ok {
			pb.Source = strings.ToLower(kind)
			pb.Value = "100"
			if kind == "OFFSET" {
				pb.Value = "0"
			}
			out[i] = pb
			continue
		}
		bound := false
		for _, ref := range refs[n] {
			qname := b.resolve(ref, aliases, referenced)
			if qname == "" {
				continue
			}
			if v, ok := b.valueFor(qname, ref.column, ref.op, typ); ok {
				pb.Source = "column " + qname + "." + ref.column + " " + ref.op
				pb.Value, pb.Null = v.value, v.null
				bound = true
				break
			}
		}
		if !bound && i < len(names) && names[i] != "" {
			name := names[i]
			if limitNames.MatchString(name) && strings.Contains(typ, "int") {
				pb.Source, pb.Value, bound = "name "+name, "100", true
			}
			for _, cand := range []string{name, strings.TrimSuffix(name, "s"), strings.TrimPrefix(name, "new_"), strings.TrimPrefix(name, "old_"), strings.TrimPrefix(name, "expected_")} {
				if bound {
					break
				}
				for _, qname := range referenced {
					if b.s.tables[qname].byName[cand] == nil {
						continue
					}
					if v, ok := b.valueFor(qname, cand, "=", typ); ok {
						pb.Source = "name " + name + " -> " + qname + "." + cand
						pb.Value, pb.Null = v.value, v.null
						bound = true
						break
					}
				}
			}
		}
		if !bound && strings.Contains(typ, "int") && durationUse(text, n) {
			pb.Source, pb.Value, bound = "duration", "300", true
		}
		if !bound {
			v := b.synthetic(typ, nameAt(names, i))
			pb.Source = "synthetic"
			pb.Value, pb.Null = v.value, v.null
		}
		out[i] = pb
	}
	return out
}

func nameAt(names []string, i int) string {
	if i < len(names) {
		return names[i]
	}
	return ""
}

func (b *binder) resolve(ref bindRef, aliases map[string]string, referenced []string) string {
	if ref.qualifier != "" {
		if q, ok := aliases[ref.qualifier]; ok && b.s.tables[q].byName[ref.column] != nil {
			return q
		}
		if ref.qualifier != "excluded" {
			return ""
		}
	}
	for _, q := range referenced {
		if b.s.tables[q].byName[ref.column] != nil {
			return q
		}
	}
	return ""
}

type boundValue struct {
	value string
	null  bool
}

func (b *binder) valueFor(qname, col, op, typ string) (boundValue, bool) {
	t := b.s.tables[qname]
	c := t.byName[col]
	if c == nil {
		return boundValue{}, false
	}
	// A value written into a foreign key column comes from the parent's live rows, so the write passes the
	// constraint the way the application's writes do.
	if qname == b.insertTarget {
		// A plain INSERT writes a new key; an upsert is bound to an existing row so its conflict path runs.
		if t.uniqueCols[col] && !b.upsert {
			return boundValue{}, false
		}
		if fi, ok := t.fkColumns[col]; ok && !t.fks[fi].soft {
			fk := t.fks[fi]
			for k, name := range fk.columns {
				if name == col && fk.parent != qname {
					if v, ok := b.valueFor(fk.parent, fk.parentColumns[k], op, typ); ok {
						return v, true
					}
				}
			}
		}
	}
	isTime := strings.Contains(c.format, "timestamp") || c.format == "date"
	if isTime && (op == "<" || op == "<=") {
		return boundValue{value: b.now.Add(time.Minute).Format(time.RFC3339Nano)}, true
	}
	if isTime && (op == ">" || op == ">=") {
		return boundValue{value: b.now.Add(-24 * time.Hour).Format(time.RFC3339Nano)}, true
	}
	samples := b.s.samples[qname]
	if len(samples) == 0 {
		return boundValue{}, false
	}
	start := int(hashString(b.queryID+qname) % uint64(len(samples)))
	if strings.HasSuffix(typ, "[]") && !c.isArray {
		var vals []*string
		seen := map[string]bool{}
		for k := 0; k < len(samples) && len(vals) < 5; k++ {
			if v := samples[(start+k)%len(samples)][col]; v != nil && !seen[*v] {
				seen[*v] = true
				vals = append(vals, v)
			}
		}
		if len(vals) == 0 {
			return boundValue{}, false
		}
		return boundValue{value: arrayLiteral(vals)}, true
	}
	for k := 0; k < len(samples); k++ {
		if v := samples[(start+k)%len(samples)][col]; v != nil {
			return boundValue{value: *v}, true
		}
	}
	return boundValue{}, false
}

func (b *binder) synthetic(typ, name string) boundValue {
	rng := rand.New(rand.NewSource(int64(hashString(b.queryID + typ + name))))
	switch {
	case strings.HasSuffix(typ, "[]"):
		return boundValue{value: "{}"}
	case typ == "uuid":
		return boundValue{value: uuidFrom(rng)}
	case typ == "smallint" || typ == "integer" || typ == "bigint":
		if limitNames.MatchString(name) {
			return boundValue{value: "100"}
		}
		// Durations (stale thresholds, retention, leases) bind five minutes, in the unit the name carries.
		if durationNames.MatchString(name) {
			if strings.HasSuffix(name, "_ms") || strings.Contains(name, "millis") {
				return boundValue{value: "300000"}
			}
			return boundValue{value: "300"}
		}
		return boundValue{value: "1"}
	case strings.HasPrefix(typ, "numeric") || typ == "double precision" || typ == "real":
		return boundValue{value: "1"}
	case typ == "boolean":
		return boundValue{value: "true"}
	case strings.HasPrefix(typ, "timestamp"):
		return boundValue{value: b.now.Format(time.RFC3339Nano)}
	case typ == "date":
		return boundValue{value: b.now.Format("2006-01-02")}
	case typ == "interval":
		return boundValue{value: "1 hour"}
	case typ == "jsonb" || typ == "json":
		return boundValue{value: "{}"}
	case typ == "bytea":
		return boundValue{value: `\x00`}
	case typ == "inet":
		return boundValue{value: "10.0.0.1"}
	case typ == "text" || strings.HasPrefix(typ, "character") || typ == "citext" || typ == "unknown":
		if strings.HasSuffix(name, "_id") || name == "id" {
			return boundValue{value: uuidFrom(rng)}
		}
		return boundValue{value: "audit"}
	}
	return boundValue{null: true}
}

func literal(p paramBinding) string {
	if p.Null {
		return "NULL"
	}
	quoted := "'" + strings.ReplaceAll(p.Value, "'", "''") + "'"
	if p.Type == "unknown" || p.Type == "" {
		return quoted
	}
	return quoted + "::" + p.Type
}
