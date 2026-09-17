package provisioner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"

	"github.com/lib/pq"
)

const (
	// baselineVerificationPendingFloor is the _schema_baseline value held by a
	// database whose baseline was re-applied but not yet verified. It is not a
	// version, so the initialization probe ignores it and every floor reader
	// refuses it.
	baselineVerificationPendingFloor = "baseline-verification-pending"
	// baselineReferenceInfix marks scratch databases built only to compare a
	// completed schema against its baseline: <database>__baseline_check_<hex>.
	baselineReferenceInfix     = "__baseline_check_"
	maxPostgresIdentifierBytes = 63
	// Yugabyte baseline completion can take several minutes because DDL is
	// replicated and both the target and reference receive the baseline.
	serviceBaselineTimeout = 30 * time.Minute
)

// BaselineApplier runs the schema role (baseline, ownership, runtime grants)
// for the given databases. Databases with ReapplyBaseline set receive their
// baseline even when their service schema already has tables.
type BaselineApplier func(ctx context.Context, databases []SchemaDatabase) error

// InitializeServiceDatabases applies absent or empty baselines directly. An
// explicitly authorized populated database is marked pending, completed, and
// accepted only when its catalog matches a fresh reference database.
func InitializeServiceDatabases(ctx context.Context, sshPool *ssh.Pool, host inventory.Host, pg *inventory.PostgresConfig, pending []SchemaDatabase, states map[string]ServiceDatabaseState, apply BaselineApplier) error {
	if err := validateSchemaDatabaseIdentifiers(pending); err != nil {
		return err
	}
	var probe serviceDatabaseProbe
	for _, database := range pending {
		if states[database.Name].HasUnverifiedSchema() {
			var err error
			if probe, err = newServiceDatabaseProbe(sshPool, host, pg); err != nil {
				return err
			}
			break
		}
	}
	return initializeServiceDatabases(ctx, probe, pending, states, apply, randomReferenceSuffix, serviceBaselineTimeout)
}

type baselineCompletion struct {
	database  SchemaDatabase
	reference SchemaDatabase
}

func initializeServiceDatabases(ctx context.Context, probe serviceDatabaseProbe, pending []SchemaDatabase, states map[string]ServiceDatabaseState, apply BaselineApplier, referenceSuffix func() (string, error), perDatabase time.Duration) error {
	if err := validateSchemaDatabaseIdentifiers(pending); err != nil {
		return err
	}
	var divergences []error
	for _, database := range pending {
		if !states[database.Name].HasUnverifiedSchema() {
			if err := applyBaselineWithin(ctx, perDatabase, apply, database); err != nil {
				return errors.Join(append(divergences, err)...)
			}
			continue
		}
		divergence, err := completeServiceDatabase(ctx, probe, database, apply, referenceSuffix, perDatabase)
		if err != nil {
			return errors.Join(append(divergences, err)...)
		}
		if divergence != nil {
			divergences = append(divergences, divergence)
		}
	}
	return errors.Join(divergences...)
}

func applyBaselineWithin(parent context.Context, timeout time.Duration, apply BaselineApplier, database SchemaDatabase) error {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	err := apply(ctx, []SchemaDatabase{database})
	if err != nil && baselineDeadlineReached(ctx, parent) {
		return fmt.Errorf("%s: baseline apply exceeded its %s per-database bound: %w", database.Name, timeout, err)
	}
	return err
}

// completeServiceDatabase completes and verifies one unverified database
// within timeout. A schema that differs from its reference is returned as
// divergence; every other failure, including the timeout, is returned as err
// and stops the release step.
func completeServiceDatabase(parent context.Context, probe serviceDatabaseProbe, database SchemaDatabase, apply BaselineApplier, referenceSuffix func() (string, error), timeout time.Duration) (divergence, err error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	// The reference is dropped with a context that outlives the completion
	// deadline, so a timed-out completion still removes it; the probe bounds
	// each statement itself.
	cleanup := context.WithoutCancel(parent)
	defer func() {
		if err != nil && baselineDeadlineReached(ctx, parent) {
			err = fmt.Errorf("%s: baseline completion exceeded its %s per-database bound; the database keeps its verification-pending marker and the next release completes it again: %w", database.Name, timeout, err)
		}
	}()

	existing, err := probe.databaseNames(ctx)
	if err != nil {
		return nil, fmt.Errorf("list databases: %w", err)
	}
	for _, stale := range staleBaselineReferences(database.Name, existing) {
		if dropErr := dropDatabaseIfExists(ctx, probe, stale); dropErr != nil {
			return nil, fmt.Errorf("drop baseline reference database %q left by an interrupted run: %w", stale, dropErr)
		}
	}
	suffix, err := referenceSuffix()
	if err != nil {
		return nil, err
	}
	referenceName, err := baselineReferenceName(database.Name, suffix)
	if err != nil {
		return nil, err
	}
	if markErr := markBaselineVerificationPending(ctx, probe, database.Name); markErr != nil {
		return nil, fmt.Errorf("%s: mark baseline verification pending: %w", database.Name, markErr)
	}
	reference := database
	reference.Name = referenceName
	reference.ReapplyBaseline = true
	defer func() {
		if dropErr := dropDatabaseIfExists(cleanup, probe, referenceName); dropErr != nil {
			err = errors.Join(err, fmt.Errorf("drop baseline reference database %q: %w", referenceName, dropErr))
		}
	}()
	if createErr := createBaselineReferenceDatabase(ctx, probe, database.Name, referenceName); createErr != nil {
		return nil, fmt.Errorf("%s: create baseline reference database %q: %w", database.Name, referenceName, createErr)
	}
	database.ReapplyBaseline = true
	if applyErr := apply(ctx, []SchemaDatabase{database, reference}); applyErr != nil {
		return nil, applyErr
	}
	verifyErr := verifyBaselineCompletion(ctx, cleanup, probe, baselineCompletion{database: database, reference: reference})
	if verifyErr != nil && ctx.Err() != nil {
		return nil, verifyErr
	}
	return verifyErr, nil
}

// baselineDeadlineReached reports that the per-database bound, and not the
// caller's context, ended the work.
func baselineDeadlineReached(ctx, parent context.Context) bool {
	return errors.Is(ctx.Err(), context.DeadlineExceeded) && parent.Err() == nil
}

func verifyBaselineCompletion(ctx, cleanup context.Context, probe serviceDatabaseProbe, completion baselineCompletion) error {
	database, reference := completion.database, completion.reference
	actual, err := readServiceSchemaCatalog(ctx, probe, database.Name, database.Schema)
	if err != nil {
		return fmt.Errorf("%s: read schema catalog: %w", database.Name, err)
	}
	expected, err := readServiceSchemaCatalog(ctx, probe, reference.Name, reference.Schema)
	if err != nil {
		return fmt.Errorf("%s: read reference schema catalog from %q: %w", database.Name, reference.Name, err)
	}
	floor, err := probe.scalarText(ctx, reference.Name, baselineMarkerQuery)
	if err != nil {
		return fmt.Errorf("%s: read baseline marker from reference %q: %w", database.Name, reference.Name, err)
	}
	// The reference is dropped before the marker is finalized, so a reference
	// database can only outlive a run whose database is still unverified.
	if dropErr := dropDatabaseIfExists(cleanup, probe, reference.Name); dropErr != nil {
		return fmt.Errorf("%s: drop baseline reference database %q: %w", database.Name, reference.Name, dropErr)
	}
	if differences := diffServiceSchemaCatalogs(expected, actual); len(differences) > 0 {
		return errors.New(formatBaselineDivergence(database, differences))
	}
	floor = strings.TrimSpace(floor)
	if floor == "" {
		return fmt.Errorf("%s: baseline schema/%s.sql wrote no _schema_baseline marker; refusing to mark the database initialized", database.Name, database.SourceName)
	}
	if validateErr := validateBaselineFloor(reference.Name, floor); validateErr != nil {
		return validateErr
	}
	return probe.exec(ctx, database.Name, fmt.Sprintf(
		"UPDATE public._schema_baseline SET floor = %s, applied_at = now() WHERE floor = %s",
		pq.QuoteLiteral(floor), pq.QuoteLiteral(baselineVerificationPendingFloor)))
}

// markBaselineVerificationPending writes the pending marker only into an empty
// marker table; a database that gained a real marker concurrently is refused.
func markBaselineVerificationPending(ctx context.Context, probe serviceDatabaseProbe, database string) error {
	if err := probe.exec(ctx, database, "CREATE TABLE IF NOT EXISTS public._schema_baseline (floor TEXT NOT NULL, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())"); err != nil {
		return err
	}
	pending := pq.QuoteLiteral(baselineVerificationPendingFloor)
	if err := probe.exec(ctx, database, fmt.Sprintf(
		"INSERT INTO public._schema_baseline (floor) SELECT %s WHERE NOT EXISTS (SELECT 1 FROM public._schema_baseline)", pending)); err != nil {
		return err
	}
	onlyPending, err := probe.scalarBool(ctx, database, fmt.Sprintf(
		"SELECT EXISTS (SELECT 1 FROM public._schema_baseline WHERE floor = %[1]s) AND NOT EXISTS (SELECT 1 FROM public._schema_baseline WHERE floor <> %[1]s)", pending))
	if err != nil {
		return err
	}
	if !onlyPending {
		return errors.New("_schema_baseline holds a marker that is not the verification-pending value; the database changed during the release, refusing")
	}
	return nil
}

// createBaselineReferenceDatabase creates the reference with the source
// database's encoding and locale on PostgreSQL; YugabyteDB databases are
// created without options, as the yugabyte role creates them.
func createBaselineReferenceDatabase(ctx context.Context, probe serviceDatabaseProbe, source, reference string) error {
	statement := "CREATE DATABASE " + pq.QuoteIdentifier(reference)
	if !probe.yugabyte() {
		built, err := probe.scalarText(ctx, probe.maintenanceDatabase(), fmt.Sprintf(
			"SELECT format('CREATE DATABASE %%I TEMPLATE template0 ENCODING %%L LC_COLLATE %%L LC_CTYPE %%L', %s, pg_encoding_to_char(encoding), datcollate, datctype) FROM pg_database WHERE datname = %s",
			pq.QuoteLiteral(reference), pq.QuoteLiteral(source)))
		if err != nil {
			return err
		}
		if strings.TrimSpace(built) == "" {
			return fmt.Errorf("database %q not found", source)
		}
		statement = strings.TrimSpace(built)
	}
	return probe.exec(ctx, probe.maintenanceDatabase(), statement)
}

func dropDatabaseIfExists(ctx context.Context, probe serviceDatabaseProbe, name string) error {
	return probe.exec(ctx, probe.maintenanceDatabase(), "DROP DATABASE IF EXISTS "+pq.QuoteIdentifier(name))
}

func baselineReferenceName(database, suffix string) (string, error) {
	name := database + baselineReferenceInfix + suffix
	if len(name) > maxPostgresIdentifierBytes || !simpleDBIdentifier.MatchString(name) {
		return "", fmt.Errorf("database %q: baseline reference name %q is not a valid database identifier of at most %d bytes", database, name, maxPostgresIdentifierBytes)
	}
	return name, nil
}

func staleBaselineReferences(database string, existing map[string]struct{}) []string {
	pattern := regexp.MustCompile("^" + regexp.QuoteMeta(database+baselineReferenceInfix) + "[0-9a-f]{16}$")
	var stale []string
	for name := range existing {
		if pattern.MatchString(name) {
			stale = append(stale, name)
		}
	}
	sort.Strings(stale)
	return stale
}

func randomReferenceSuffix() (string, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("generate baseline reference name: %w", err)
	}
	return hex.EncodeToString(buf[:]), nil
}

// serviceSchemaObjectDifference is one object whose definition differs between
// the reference built from the baseline and the database. An empty definition
// means the object is absent on that side.
type serviceSchemaObjectDifference struct {
	Object   string
	Expected string
	Actual   string
}

func diffServiceSchemaCatalogs(expected, actual map[string]string) []serviceSchemaObjectDifference {
	objects := make(map[string]struct{}, len(expected)+len(actual))
	for object := range expected {
		objects[object] = struct{}{}
	}
	for object := range actual {
		objects[object] = struct{}{}
	}
	names := make([]string, 0, len(objects))
	for object := range objects {
		names = append(names, object)
	}
	sort.Strings(names)
	var differences []serviceSchemaObjectDifference
	for _, object := range names {
		want, got := expected[object], actual[object]
		if want != got {
			differences = append(differences, serviceSchemaObjectDifference{Object: object, Expected: want, Actual: got})
		}
	}
	return differences
}

func formatBaselineDivergence(database SchemaDatabase, differences []serviceSchemaObjectDifference) string {
	var b strings.Builder
	fmt.Fprintf(&b, "service database %q has tables but no baseline marker or migration history, and after re-applying schema/%s.sql its schema %q still differs from a reference database built from the same baseline (%d difference(s)); it stays unverified and releases refuse it until the schema matches:",
		database.Name, database.SourceName, database.Schema, len(differences))
	render := func(definition string) string {
		if definition == "" {
			return "(absent)"
		}
		return definition
	}
	for _, difference := range differences {
		fmt.Fprintf(&b, "\n  %s\n    expected: %s\n    actual:   %s", difference.Object, render(difference.Expected), render(difference.Actual))
	}
	return b.String()
}

func readServiceSchemaCatalog(ctx context.Context, probe serviceDatabaseProbe, database, schema string) (map[string]string, error) {
	rows, err := probe.textRows(ctx, database, serviceSchemaCatalogQuery(schema))
	if err != nil {
		return nil, err
	}
	return parseServiceSchemaCatalog(rows)
}

func parseServiceSchemaCatalog(rows []string) (map[string]string, error) {
	catalog := make(map[string]string, len(rows))
	for _, row := range rows {
		raw, err := hex.DecodeString(strings.TrimSpace(row))
		if err != nil {
			return nil, fmt.Errorf("decode catalog row %q: %w", row, err)
		}
		object, definition, ok := strings.Cut(string(raw), "\t")
		if !ok || object == "" || definition == "" {
			return nil, fmt.Errorf("malformed catalog row %q", string(raw))
		}
		if _, duplicate := catalog[object]; duplicate {
			return nil, fmt.Errorf("catalog lists %s twice", object)
		}
		catalog[object] = definition
	}
	return catalog, nil
}

// serviceSchemaCatalogQuery describes every object in one service schema as
// hex-encoded "object<TAB>definition" rows. Hex keeps each row on one line
// without the '|' separator the ysqlsh row reader splits on. Ownership and
// grants are not compared because the schema role reasserts them after every
// apply. Extension members are skipped, as are NOT NULL constraints, which the
// column definitions already carry and whose generated names differ between
// databases. Index validity is included so an interrupted index build, which
// CREATE INDEX IF NOT EXISTS does not repair, is reported.
func serviceSchemaCatalogQuery(schema string) string {
	return strings.ReplaceAll(serviceSchemaCatalogTemplate, "{{schema}}", pq.QuoteLiteral(schema))
}

const serviceSchemaCatalogTemplate = `
WITH target AS (
  SELECT oid FROM pg_namespace WHERE nspname = {{schema}}
), extension_members AS (
  SELECT classid, objid FROM pg_depend WHERE deptype = 'e'
), objects(object, definition) AS (
  SELECT 'schema ' || n.nspname, 'present'
    FROM pg_namespace n
   WHERE n.oid IN (SELECT oid FROM target)
  UNION ALL
  SELECT 'table ' || n.nspname || '.' || c.relname,
         'kind=' || c.relkind::text || ' persistence=' || c.relpersistence::text ||
         ' row_security=' || c.relrowsecurity::text || ' force_row_security=' || c.relforcerowsecurity::text
    FROM pg_class c
    JOIN pg_namespace n ON n.oid = c.relnamespace
   WHERE c.relnamespace IN (SELECT oid FROM target)
     AND c.relkind IN ('r', 'p', 'f')
     AND NOT EXISTS (SELECT 1 FROM extension_members e WHERE e.classid = 'pg_class'::regclass AND e.objid = c.oid)
  UNION ALL
  SELECT 'column ' || n.nspname || '.' || c.relname || '.' || a.attname,
         format_type(a.atttypid, a.atttypmod) ||
         CASE WHEN a.attnotnull THEN ' NOT NULL' ELSE '' END ||
         CASE WHEN a.attgenerated = 's' THEN ' GENERATED ALWAYS AS (' || pg_get_expr(d.adbin, d.adrelid) || ') STORED'
              ELSE coalesce(' DEFAULT ' || pg_get_expr(d.adbin, d.adrelid), '') END ||
         CASE a.attidentity WHEN 'a' THEN ' GENERATED ALWAYS AS IDENTITY'
                            WHEN 'd' THEN ' GENERATED BY DEFAULT AS IDENTITY' ELSE '' END
    FROM pg_attribute a
    JOIN pg_class c ON c.oid = a.attrelid
    JOIN pg_namespace n ON n.oid = c.relnamespace
    LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
   WHERE c.relnamespace IN (SELECT oid FROM target)
     AND c.relkind IN ('r', 'p', 'f', 'v', 'm', 'c')
     AND a.attnum > 0
     AND NOT a.attisdropped
     AND NOT EXISTS (SELECT 1 FROM extension_members e WHERE e.classid = 'pg_class'::regclass AND e.objid = c.oid)
  UNION ALL
  SELECT 'constraint ' || n.nspname || '.' || t.relname || '.' || con.conname,
         con.contype::text || ' ' || pg_get_constraintdef(con.oid)
    FROM pg_constraint con
    JOIN pg_class t ON t.oid = con.conrelid
    JOIN pg_namespace n ON n.oid = t.relnamespace
   WHERE t.relnamespace IN (SELECT oid FROM target)
     AND con.contype <> 'n'
     AND NOT (con.contype = 'c' AND con.conname ~ '^[0-9]+_[0-9]+_[0-9]+_not_null$')
  UNION ALL
  SELECT 'constraint ' || n.nspname || '.' || ty.typname || '.' || con.conname,
         con.contype::text || ' ' || pg_get_constraintdef(con.oid)
    FROM pg_constraint con
    JOIN pg_type ty ON ty.oid = con.contypid
    JOIN pg_namespace n ON n.oid = ty.typnamespace
   WHERE ty.typnamespace IN (SELECT oid FROM target)
     AND con.contype <> 'n'
  UNION ALL
  SELECT 'index ' || n.nspname || '.' || ic.relname,
         pg_get_indexdef(i.indexrelid) || ' valid=' || i.indisvalid::text || ' ready=' || i.indisready::text
    FROM pg_index i
    JOIN pg_class ic ON ic.oid = i.indexrelid
    JOIN pg_namespace n ON n.oid = ic.relnamespace
   WHERE ic.relnamespace IN (SELECT oid FROM target)
  UNION ALL
  SELECT 'trigger ' || n.nspname || '.' || t.relname || '.' || tg.tgname,
         pg_get_triggerdef(tg.oid) || ' enabled=' || tg.tgenabled::text
    FROM pg_trigger tg
    JOIN pg_class t ON t.oid = tg.tgrelid
    JOIN pg_namespace n ON n.oid = t.relnamespace
   WHERE t.relnamespace IN (SELECT oid FROM target)
     AND NOT tg.tgisinternal
  UNION ALL
  SELECT 'routine ' || n.nspname || '.' || p.proname || '(' || pg_get_function_identity_arguments(p.oid) || ')',
         'kind=' || p.prokind::text || ' definition_md5=' || md5(pg_get_functiondef(p.oid))
    FROM pg_proc p
    JOIN pg_namespace n ON n.oid = p.pronamespace
   WHERE p.pronamespace IN (SELECT oid FROM target)
     AND p.prokind IN ('f', 'p')
     AND NOT EXISTS (SELECT 1 FROM extension_members e WHERE e.classid = 'pg_proc'::regclass AND e.objid = p.oid)
  UNION ALL
  SELECT 'view ' || n.nspname || '.' || c.relname,
         'kind=' || c.relkind::text || ' definition_md5=' || md5(pg_get_viewdef(c.oid, true))
    FROM pg_class c
    JOIN pg_namespace n ON n.oid = c.relnamespace
   WHERE c.relnamespace IN (SELECT oid FROM target)
     AND c.relkind IN ('v', 'm')
     AND NOT EXISTS (SELECT 1 FROM extension_members e WHERE e.classid = 'pg_class'::regclass AND e.objid = c.oid)
  UNION ALL
  SELECT 'sequence ' || n.nspname || '.' || c.relname,
         format_type(s.seqtypid, NULL) || ' start=' || s.seqstart || ' increment=' || s.seqincrement ||
         ' min=' || s.seqmin || ' max=' || s.seqmax || ' cache=' || s.seqcache || ' cycle=' || s.seqcycle::text
    FROM pg_sequence s
    JOIN pg_class c ON c.oid = s.seqrelid
    JOIN pg_namespace n ON n.oid = c.relnamespace
   WHERE c.relnamespace IN (SELECT oid FROM target)
     AND NOT EXISTS (SELECT 1 FROM extension_members e WHERE e.classid = 'pg_class'::regclass AND e.objid = c.oid)
  UNION ALL
  SELECT 'type ' || n.nspname || '.' || ty.typname,
         'enum (' || string_agg(quote_literal(e.enumlabel), ', ' ORDER BY e.enumsortorder) || ')'
    FROM pg_type ty
    JOIN pg_namespace n ON n.oid = ty.typnamespace
    JOIN pg_enum e ON e.enumtypid = ty.oid
   WHERE ty.typnamespace IN (SELECT oid FROM target)
   GROUP BY n.nspname, ty.typname
  UNION ALL
  SELECT 'type ' || n.nspname || '.' || ty.typname,
         'domain ' || format_type(ty.typbasetype, ty.typtypmod) ||
         CASE WHEN ty.typnotnull THEN ' NOT NULL' ELSE '' END || coalesce(' DEFAULT ' || ty.typdefault, '')
    FROM pg_type ty
    JOIN pg_namespace n ON n.oid = ty.typnamespace
   WHERE ty.typnamespace IN (SELECT oid FROM target)
     AND ty.typtype = 'd'
  UNION ALL
  SELECT 'type ' || n.nspname || '.' || c.relname, 'composite'
    FROM pg_class c
    JOIN pg_namespace n ON n.oid = c.relnamespace
   WHERE c.relnamespace IN (SELECT oid FROM target)
     AND c.relkind = 'c'
  UNION ALL
  SELECT 'policy ' || pol.schemaname || '.' || pol.tablename || '.' || pol.policyname,
         pol.permissive || ' ' || pol.cmd ||
         ' roles=' || array_to_string(ARRAY(SELECT r FROM unnest(pol.roles) AS r ORDER BY r), ',') ||
         ' using=' || coalesce(pol.qual, '') || ' check=' || coalesce(pol.with_check, '')
    FROM pg_policies pol
   WHERE pol.schemaname = {{schema}}
)
SELECT encode(convert_to(object || E'\t' || definition, 'UTF8'), 'hex') FROM objects`
