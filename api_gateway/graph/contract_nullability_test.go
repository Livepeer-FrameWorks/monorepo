package graph

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"sort"
	"strings"
	"testing"

	"frameworks/api_gateway/graph/generated"

	gqlast "github.com/vektah/gqlparser/v2/ast"
)

// nonNullSourceGuards lists every non-null schema field whose field resolver
// checks its backing proto value for absence (nil, invalid, or zero) and then
// either errors ("required but missing") or returns nil. Either branch turns
// the field into an error that nulls its parent, so the guard is only
// acceptable when the source is guaranteed present. Each entry states why.
//
// A new guard on a non-null field fails TestNonNullFieldsHaveGuaranteedSources
// until it is listed here with a reason, or the schema field becomes nullable.
// An entry whose guard no longer exists fails the test too, so the list tracks
// the resolvers exactly.
var nonNullSourceGuards = map[string]string{
	// Periscope event and rollup rows: the timestamp is the row's own ClickHouse
	// DateTime key (not Nullable) and Periscope always sets it.
	"APIUsageRecord.timestamp":               periscopeRowTime,
	"APIUsageSummary.date":                   periscopeRowTime,
	"ArtifactEvent.timestamp":                periscopeRowTime,
	"ArtifactState.requestedAt":              periscopeRowTime,
	"BufferEvent.timestamp":                  periscopeRowTime,
	"ClientMetrics5m.timestamp":              periscopeRowTime,
	"ConnectionEvent.timestamp":              periscopeRowTime,
	"FederationEvent.timestamp":              periscopeRowTime,
	"LiveNode.updatedAt":                     periscopeRowTime,
	"NodeMetric.timestamp":                   periscopeRowTime,
	"NodeMetricHourly.timestamp":             periscopeRowTime,
	"NodePerformance5m.timestamp":            periscopeRowTime,
	"Orchestrator.lastSeen":                  periscopeRowTime,
	"Orchestrator.updatedAt":                 periscopeRowTime,
	"OrchestratorInstance.lastSeen":          periscopeRowTime,
	"OrchestratorInstance.updatedAt":         periscopeRowTime,
	"OrchestratorPerformancePoint.timestamp": periscopeRowTime,
	"OrchestratorVantage.lastSeen":           periscopeRowTime,
	"PlayerBootTimeSeriesBucket.timestamp":   periscopeRowTime,
	"ProcessingUsageRecord.timestamp":        periscopeRowTime,
	"ProcessingUsageSummary.date":            periscopeRowTime,
	"QualityTierDaily.day":                   periscopeRowTime,
	"RebufferingEvent.timestamp":             periscopeRowTime,
	"RoutingEvent.timestamp":                 periscopeRowTime,
	"SessionQoeTimeSeriesBucket.timestamp":   periscopeRowTime,
	"StorageEvent.timestamp":                 periscopeRowTime,
	"StorageUsageRecord.timestamp":           periscopeRowTime,
	"StreamAnalyticsDaily.day":               periscopeRowTime,
	"StreamConnectionHourly.hour":            periscopeRowTime,
	"StreamHealth5m.timestamp":               periscopeRowTime,
	"StreamHealthMetric.timestamp":           periscopeRowTime,
	"TenantAnalyticsDaily.day":               periscopeRowTime,
	"TenantDailyStat.date":                   periscopeRowTime,
	"TrackListEvent.timestamp":               periscopeRowTime,
	"ViewerCountBucket.timestamp":            periscopeRowTime,
	"ViewerGeoHourly.hour":                   periscopeRowTime,
	"ViewerGeographic.timestamp":             periscopeRowTime,
	"ViewerHoursHourly.hour":                 periscopeRowTime,
	"ViewerSession.timestamp":                periscopeRowTime,

	// Control-plane rows: NOT NULL or DEFAULT NOW() columns that the owning
	// service always returns.
	"BootstrapToken.createdAt":      controlPlaneRowTime,
	"ClusterInvite.createdAt":       controlPlaneRowTime,
	"ClusterSubscription.createdAt": controlPlaneRowTime,
	"ClusterSubscription.updatedAt": controlPlaneRowTime,
	"DVRRequest.createdAt":          controlPlaneRowTime,
	"DVRRequest.updatedAt":          controlPlaneRowTime,
	"Invoice.createdAt":             controlPlaneRowTime,
	"Invoice.dueDate":               controlPlaneRowTime,
	"Invoice.updatedAt":             controlPlaneRowTime,
	"InvoicePayment.createdAt":      controlPlaneRowTime,
	"InvoicePayment.updatedAt":      controlPlaneRowTime,
	"Payment.createdAt":             controlPlaneRowTime,
	"PullSourceEvent.createdAt":     controlPlaneRowTime,
	"PushTarget.createdAt":          controlPlaneRowTime,
	"Stream.createdAt":              controlPlaneRowTime,
	"Stream.updatedAt":              controlPlaneRowTime,
	"StreamKey.createdAt":           controlPlaneRowTime,
	"Tenant.createdAt":              controlPlaneRowTime,
	"TenantSubscription.createdAt":  controlPlaneRowTime,
	"TenantSubscription.startedAt":  controlPlaneRowTime,
	"TenantSubscription.updatedAt":  controlPlaneRowTime,
	"User.createdAt":                controlPlaneRowTime,

	// Values the gateway itself fills before the field resolves.
	"PlaybackJwtPolicy.requiredClaimsJson": "the guard is on the parent object itself; a policy value always exists when this field resolves",
	"SystemHealthEvent.timestamp":          "node lifecycle events are timestamped by Helmsman when they are emitted",
	"TimeRange.end":                        "response time ranges are echoed from the normalized request range",
	"TimeRange.start":                      "response time ranges are echoed from the normalized request range",

	// N21: a never-live stream has no Periscope status row, so this source is
	// legitimately absent. The guard is only safe because Stream.metrics
	// resolves to null until Periscope has observed the stream
	// (resolvers.ObservedStreamMetrics); without that parent null, createStream
	// failed for every SDK selecting metrics.updatedAt.
	"StreamMetrics.updatedAt": "parent Stream.metrics is null until Periscope has a status row (ledger N21)",
}

const (
	periscopeRowTime    = "Periscope row timestamp: a non-Nullable ClickHouse DateTime that Periscope always sets"
	controlPlaneRowTime = "control-plane row timestamp: NOT NULL or DEFAULT NOW() column the owning service always returns"
)

// sourceGuard is one absence check on the object backing a field resolver.
type sourceGuard struct {
	gqlType, field string
	kind           string // "error" or "nil-return"
	cond           string
}

func TestNonNullFieldsHaveGuaranteedSources(t *testing.T) {
	schema := generated.NewExecutableSchema(generated.Config{}).Schema()
	guards := collectSourceGuards(t, "schema.resolvers.go", nil)

	found := map[string]bool{}
	var unlisted []string
	for _, g := range guards {
		def := schema.Types[g.gqlType]
		if def == nil {
			t.Errorf("resolver for unknown GraphQL type %q", g.gqlType)
			continue
		}
		field := fieldByResolverName(def, g.field)
		if field == nil {
			t.Errorf("resolver %s.%s has no matching schema field", g.gqlType, g.field)
			continue
		}
		if !field.Type.NonNull {
			continue
		}
		key := g.gqlType + "." + field.Name
		found[key] = true
		if _, ok := nonNullSourceGuards[key]; !ok {
			unlisted = append(unlisted, key+" ("+g.kind+" when "+g.cond+")")
		}
	}
	sort.Strings(unlisted)
	for _, u := range unlisted {
		t.Errorf("non-null field %s guards an absent source; make the schema field nullable, null its parent, or list it in nonNullSourceGuards with the reason the source is always present", u)
	}

	var stale []string
	for key := range nonNullSourceGuards {
		if !found[key] {
			stale = append(stale, key)
		}
	}
	sort.Strings(stale)
	for _, key := range stale {
		t.Errorf("nonNullSourceGuards lists %s but its resolver no longer guards a non-null field; remove the entry", key)
	}
}

func TestSourceGuardDetector(t *testing.T) {
	src := `package graph
func (r *streamMetricsResolver) UpdatedAt(ctx context.Context, obj *pb.StreamStatusResponse) (*time.Time, error) {
	if obj.UpdatedAt == nil {
		return nil, fmt.Errorf("stream metrics updatedAt is required but missing")
	}
	return nil, nil
}
func (r *streamResolver) CreatedAt(ctx context.Context, obj *pb.Stream) (*time.Time, error) {
	if obj.GetCreatedAt() == nil || !obj.GetCreatedAt().IsValid() {
		return nil, nil
	}
	return nil, nil
}
func (r *streamResolver) Name(ctx context.Context, obj *pb.Stream) (string, error) {
	if ctx == nil {
		return "", nil
	}
	return obj.Name, nil
}
func (r *queryResolver) Stream(ctx context.Context, id string) (*pb.Stream, error) {
	if id == "" {
		return nil, nil
	}
	return nil, nil
}
`
	got := collectSourceGuards(t, "synthetic.go", src)
	want := []string{"StreamMetrics.UpdatedAt error", "Stream.CreatedAt nil-return"}
	if len(got) != len(want) {
		t.Fatalf("detected %d guards, want %d: %+v", len(got), len(want), got)
	}
	for i, g := range got {
		if key := g.gqlType + "." + g.field + " " + g.kind; key != want[i] {
			t.Errorf("guard %d = %q, want %q", i, key, want[i])
		}
	}
}

// collectSourceGuards finds `if <cond on obj> { return ... }` statements in
// field resolvers whose condition tests the backing object for absence. src
// overrides reading path when non-nil.
func collectSourceGuards(t *testing.T, path string, src any) []sourceGuard {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var guards []sourceGuard
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 || fn.Body == nil {
			continue
		}
		recv := receiverTypeName(fn.Recv.List[0].Type)
		if !strings.HasSuffix(recv, "Resolver") || !hasObjParam(fn) {
			continue
		}
		gqlType := strings.ToUpper(recv[:1]) + strings.TrimSuffix(recv[1:], "Resolver")
		for _, stmt := range fn.Body.List {
			ifStmt, ok := stmt.(*ast.IfStmt)
			if !ok || len(ifStmt.Body.List) == 0 {
				continue
			}
			cond := exprString(fset, ifStmt.Cond)
			absenceCheck := strings.Contains(cond, "== nil") || strings.Contains(cond, "IsValid()") || strings.Contains(cond, "== 0")
			if !strings.Contains(cond, "obj") || !absenceCheck {
				continue
			}
			ret, ok := ifStmt.Body.List[0].(*ast.ReturnStmt)
			if !ok || len(ret.Results) == 0 {
				continue
			}
			kind := "nil-return"
			if last := exprString(fset, ret.Results[len(ret.Results)-1]); last != "nil" {
				kind = "error"
			} else if !allNil(fset, ret.Results) {
				continue
			}
			guards = append(guards, sourceGuard{gqlType: gqlType, field: fn.Name.Name, kind: kind, cond: cond})
		}
	}
	return guards
}

func receiverTypeName(expr ast.Expr) string {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name
	}
	return ""
}

func hasObjParam(fn *ast.FuncDecl) bool {
	for _, param := range fn.Type.Params.List {
		for _, name := range param.Names {
			if name.Name == "obj" {
				return true
			}
		}
	}
	return false
}

func allNil(fset *token.FileSet, results []ast.Expr) bool {
	for _, r := range results {
		if exprString(fset, r) != "nil" {
			return false
		}
	}
	return true
}

func exprString(fset *token.FileSet, expr ast.Expr) string {
	var buf bytes.Buffer
	_ = printer.Fprint(&buf, fset, expr)
	return buf.String()
}

// fieldByResolverName maps a gqlgen resolver method (RequiredClaimsJSON) back to
// its schema field (requiredClaimsJson); the two differ only in case.
func fieldByResolverName(def *gqlast.Definition, method string) *gqlast.FieldDefinition {
	for _, f := range def.Fields {
		if strings.EqualFold(f.Name, method) {
			return f
		}
	}
	return nil
}
