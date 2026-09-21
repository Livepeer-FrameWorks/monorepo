package backup

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

var testNow = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

var testLedger = []LedgerRow{
	{Version: "v0.3.10", Phase: "expand", Seq: 1, Checksum: "c1"},
	{Version: "v0.3.10", Phase: "postdeploy", Seq: 1, Checksum: "c2"},
	{Version: "v0.3.11", Phase: "expand", Seq: 2, Checksum: "c3"},
}

func writeTestBackup(t *testing.T, loc Location, created time.Time) *Manifest {
	t.Helper()
	ctx := context.Background()
	db := Database{Name: "purser", Engine: EngineYugabyte, Owner: "purser", Ledger: testLedger, LedgerDigest: LedgerDigest(testLedger), RowCounts: map[string]int64{"purser.invoices": 3}}
	for _, section := range Sections {
		file, err := StoreFile(ctx, loc, db.Key()+"/"+section+".sql.gz", func(w io.Writer) error {
			_, err := io.WriteString(w, "dump of "+section)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		db.Sections = append(db.Sections, Section{Name: section, File: file})
	}
	chFile, err := StoreFile(ctx, loc, ClickHouseKey("periscope")+"/api_requests.tsv.gz", func(w io.Writer) error {
		_, err := io.WriteString(w, "rows")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	m := &Manifest{
		FormatVersion: FormatVersion, CreatedAt: created, CompletedAt: created.Add(2 * time.Minute), CLIVersion: "v0.3.11",
		Databases:  []Database{db},
		ClickHouse: &ClickHouse{Database: "periscope", Ledger: testLedger[:1], LedgerDigest: LedgerDigest(testLedger[:1]), Tables: []ClickHouseTable{{Name: "api_requests", Columns: []string{"a"}, Rows: 1, File: chFile}}},
	}
	if err := WriteManifest(ctx, loc, m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestLedgerDigestIgnoresOrderAndSeesEveryField(t *testing.T) {
	reversed := []LedgerRow{testLedger[2], testLedger[0], testLedger[1]}
	if LedgerDigest(reversed) != LedgerDigest(testLedger) {
		t.Fatal("digest depends on read order")
	}
	for i, mutate := range []func(*LedgerRow){
		func(r *LedgerRow) { r.Checksum = "other" },
		func(r *LedgerRow) { r.Seq = 9 },
		func(r *LedgerRow) { r.Phase = "contract" },
		func(r *LedgerRow) { r.Version = "v0.3.12" },
	} {
		changed := append([]LedgerRow{}, testLedger...)
		mutate(&changed[0])
		if LedgerDigest(changed) == LedgerDigest(testLedger) {
			t.Fatalf("mutation %d did not change the digest", i)
		}
	}
	if LedgerDigest(nil) != LedgerDigest([]LedgerRow{}) {
		t.Fatal("an absent and an empty ledger must agree")
	}
	if LedgerDigest(testLedger[:2]) == LedgerDigest(testLedger) {
		t.Fatal("an extra migration must change the digest")
	}
}

func TestManifestRoundTripAndVerification(t *testing.T) {
	dir := t.TempDir()
	loc := LocalDir(dir)
	written := writeTestBackup(t, loc, testNow)
	ctx := context.Background()

	read, err := ReadManifest(ctx, loc)
	if err != nil {
		t.Fatal(err)
	}
	if read.Databases[0].LedgerDigest != written.Databases[0].LedgerDigest || !read.CreatedAt.Equal(testNow) || len(read.Files(nil)) != 4 {
		t.Fatalf("round trip lost data: %+v", read)
	}
	if verifyErr := VerifyFiles(ctx, loc, read.Files(nil)); verifyErr != nil {
		t.Fatalf("intact backup: %v", verifyErr)
	}
	if got := read.Files(map[string]bool{"postgres/purser": true}); len(got) != 3 {
		t.Fatalf("files for one key = %d, want its 3 sections", len(got))
	}

	target := filepath.Join(dir, "postgres", "purser", "data.sql.gz")
	if writeErr := os.WriteFile(target, []byte("dump of dat4"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	err = VerifyFiles(ctx, loc, read.Files(nil))
	if err == nil || !strings.Contains(err.Error(), "postgres/purser/data.sql.gz: sha256") {
		t.Fatalf("same-size tampering: %v", err)
	}
	if err := os.WriteFile(target, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyFiles(ctx, loc, read.Files(nil)); err == nil || !strings.Contains(err.Error(), "size 5") {
		t.Fatalf("truncation: %v", err)
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := VerifyFiles(ctx, loc, read.Files(nil)); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing file: %v", err)
	}
}

func TestManifestTamperingIsRejected(t *testing.T) {
	dir := t.TempDir()
	loc := LocalDir(dir)
	writeTestBackup(t, loc, testNow)
	path := filepath.Join(dir, ManifestName)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if decodeErr := json.Unmarshal(raw, &doc); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	db := doc["databases"].([]any)[0].(map[string]any)
	db["ledger_digest"] = LedgerDigest(nil)
	edited, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, edited, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManifest(context.Background(), loc); err == nil || !strings.Contains(err.Error(), "ledger_digest does not match its ledger rows") {
		t.Fatalf("a digest edited to match another cluster state must be rejected: %v", err)
	}

	if _, err := ReadManifest(context.Background(), LocalDir(t.TempDir())); err == nil || !strings.Contains(err.Error(), "the backup is incomplete") {
		t.Fatalf("missing manifest: %v", err)
	}
	if err := WriteManifest(context.Background(), LocalDir(t.TempDir()), &Manifest{FormatVersion: FormatVersion, CreatedAt: testNow, CompletedAt: testNow}); err == nil {
		t.Fatal("an empty backup must not get a manifest")
	}
}

func TestCheckGate(t *testing.T) {
	m := &Manifest{FormatVersion: FormatVersion, CreatedAt: testNow.Add(-30 * time.Minute), Cluster: "production", Databases: []Database{{Name: "purser", Ledger: testLedger, LedgerDigest: LedgerDigest(testLedger)}},
		ClickHouse: &ClickHouse{Database: "periscope", LedgerDigest: LedgerDigest(testLedger[:1])}}
	live := map[string]string{"postgres/purser": LedgerDigest(testLedger), "clickhouse/periscope": LedgerDigest(testLedger[:1])}

	if err := CheckGate(m, GateRequest{Operation: "contract", LiveDigests: live}, testNow); err != nil {
		t.Fatalf("fresh matching backup: %v", err)
	}
	if err := CheckGate(m, GateRequest{Operation: "contract", LiveDigests: live}, testNow.Add(30*time.Minute)); err != nil {
		t.Fatalf("exactly one hour old must pass: %v", err)
	}
	for _, tc := range []struct {
		name    string
		now     time.Time
		live    map[string]string
		cluster string
		want    string
	}{
		{name: "one second too old", now: testNow.Add(30*time.Minute + time.Second), live: live, want: "the limit is 1h0m0s"},
		{name: "start in the future", now: testNow.Add(-40 * time.Minute), live: live, want: "is in the future"},
		{name: "ledger moved on", now: testNow, live: map[string]string{"postgres/purser": LedgerDigest(testLedger[:2])}, want: "postgres/purser: its migration ledger changed after the backup"},
		{name: "database not in the backup", now: testNow, live: map[string]string{"postgres/commodore": LedgerDigest(nil)}, want: "postgres/commodore is not in the backup"},
		{name: "clickhouse ledger moved on", now: testNow, live: map[string]string{"clickhouse/periscope": LedgerDigest(testLedger)}, want: "clickhouse/periscope: its migration ledger changed"},
		{name: "another cluster", now: testNow, live: live, cluster: "staging", want: `the backup is of cluster "production", not "staging"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckGate(m, GateRequest{Operation: "contract", LiveDigests: tc.live, Cluster: tc.cluster}, tc.now)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v; want %q", err, tc.want)
			}
		})
	}
	if err := CheckGate(nil, GateRequest{Operation: "contract"}, testNow); !errors.Is(err, ErrBackupRequired) {
		t.Fatalf("nil manifest: %v", err)
	}
}

func TestRequireGateVerifiesFilesOfProtectedDatabases(t *testing.T) {
	dir := t.TempDir()
	writeTestBackup(t, LocalDir(dir), testNow.Add(-10*time.Minute))
	req := GateRequest{Operation: "contract", LiveDigests: map[string]string{"postgres/purser": LedgerDigest(testLedger)}}
	if _, err := RequireGate(context.Background(), "", req, testNow); !errors.Is(err, ErrBackupRequired) {
		t.Fatalf("empty path: %v", err)
	}
	if _, err := RequireGate(context.Background(), dir, req, testNow); err != nil {
		t.Fatalf("intact backup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "postgres", "purser", "post-data.sql.gz"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RequireGate(context.Background(), dir, req, testNow); err == nil || !strings.Contains(err.Error(), "post-data.sql.gz") {
		t.Fatalf("damaged file of a protected database must refuse: %v", err)
	}
}

func TestParseLocation(t *testing.T) {
	loc, err := ParseLocation("relative/dir")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := loc.(LocalDir); !ok || !filepath.IsAbs(loc.String()) {
		t.Fatalf("local path: %T %s", loc, loc)
	}
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	loc, err = ParseLocation("s3://bucket/some/prefix/")
	if err != nil {
		t.Fatal(err)
	}
	if loc.String() != "s3://bucket/some/prefix" || loc.Child("frameworks-backup-x").String() != "s3://bucket/some/prefix/frameworks-backup-x" {
		t.Fatalf("s3 location: %s", loc)
	}
	for _, bad := range []string{"", "s3://", "gs://bucket/x"} {
		if _, err := ParseLocation(bad); err == nil {
			t.Fatalf("%q must be rejected", bad)
		}
	}
}

func TestLocalDirCommitsAtomically(t *testing.T) {
	dir := t.TempDir()
	loc := LocalDir(dir)
	w, err := loc.Create(context.Background(), "a/b.bin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("data")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "a", "b.bin")); !os.IsNotExist(err) {
		t.Fatal("object visible before Commit")
	}
	w.Abort()
	entries, _ := os.ReadDir(filepath.Join(dir, "a"))
	if len(entries) != 0 {
		t.Fatalf("Abort left %d file(s)", len(entries))
	}
	if _, err := loc.Create(context.Background(), "../escape"); err == nil {
		if _, statErr := os.Stat(filepath.Join(filepath.Dir(dir), "escape")); statErr == nil {
			t.Fatal("object name escaped the backup directory")
		}
	}
	if _, err := StoreFile(context.Background(), loc, "failed.bin", func(io.Writer) error { return errors.New("dump failed") }); err == nil {
		t.Fatal("a failed producer must fail the store")
	}
	if _, err := os.Stat(filepath.Join(dir, "failed.bin")); !os.IsNotExist(err) {
		t.Fatal("a failed producer left an object behind")
	}
}

const sampleDataSection = `--
-- PostgreSQL database dump
--

COPY public._migrations (version, phase, seq, checksum, filename, applied_at) FROM stdin;
v0.3.10	expand	1	c1	001_a.sql	2026-09-18 10:00:00+00
v0.3.10	postdeploy	1	c2	001_b.sql	2026-09-18 10:01:00+00
v0.3.11	expand	2	c3	002_c.sql	2026-09-19 10:00:00+00
\.


COPY purser.invoices (id, note) FROM stdin;
1	line with \\t escaped tab
2	\N
3	COPY looks like a header FROM stdin;
\.


COPY purser."empty table" (id) FROM stdin;
\.


SELECT pg_catalog.setval('purser.invoices_id_seq', 3, true);
`

func TestParseDataSection(t *testing.T) {
	evidence, err := ParseDataSection(strings.NewReader(sampleDataSection))
	if err != nil {
		t.Fatal(err)
	}
	if LedgerDigest(evidence.Ledger) != LedgerDigest(testLedger) {
		t.Fatalf("ledger = %+v", evidence.Ledger)
	}
	want := map[string]int64{"public._migrations": 3, "purser.invoices": 3, `purser."empty table"`: 0}
	if len(evidence.RowCounts) != len(want) {
		t.Fatalf("row counts = %v", evidence.RowCounts)
	}
	for table, n := range want {
		if evidence.RowCounts[table] != n {
			t.Fatalf("row counts = %v, want %v", evidence.RowCounts, want)
		}
	}
	if _, truncatedErr := ParseDataSection(strings.NewReader("COPY public.t (id) FROM stdin;\n1\n")); truncatedErr == nil {
		t.Fatal("a truncated COPY block must fail")
	}
	noLedger, err := ParseDataSection(strings.NewReader("COPY chatwoot.users (id) FROM stdin;\n1\n\\.\n"))
	if err != nil || len(noLedger.Ledger) != 0 || noLedger.RowCounts["chatwoot.users"] != 1 {
		t.Fatalf("database without a ledger: %+v %v", noLedger, err)
	}
}

func gzipped(t *testing.T, s string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestStoreCompressedInspectsTheStoredStream(t *testing.T) {
	loc := LocalDir(t.TempDir())
	payload := gzipped(t, sampleDataSection)
	var evidence *DumpEvidence
	file, err := StoreCompressed(context.Background(), loc, "data.sql.gz", func(w io.Writer) error {
		_, err := w.Write(payload)
		return err
	}, func(r io.Reader) error {
		parsed, err := ParseDataSection(r)
		evidence = parsed
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	if file.Size != int64(len(payload)) || file.SHA256 != hex.EncodeToString(sum[:]) || evidence.RowCounts["purser.invoices"] != 3 {
		t.Fatalf("file = %+v evidence = %+v", file, evidence)
	}

	_, err = StoreCompressed(context.Background(), loc, "bad.sql.gz", func(w io.Writer) error {
		_, writeErr := w.Write(gzipped(t, "COPY public.t (id) FROM stdin;\n1\n"))
		return writeErr
	}, func(r io.Reader) error {
		_, parseErr := ParseDataSection(r)
		return parseErr
	})
	if err == nil {
		t.Fatal("an inspection failure must fail the store")
	}
	if _, statErr := os.Stat(filepath.Join(loc.String(), "bad.sql.gz")); !os.IsNotExist(statErr) {
		t.Fatal("a failed inspection left the object behind")
	}
	_, err = StoreCompressed(context.Background(), loc, "early.sql.gz", func(w io.Writer) error {
		return errors.New("ssh dropped")
	}, func(r io.Reader) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "ssh dropped") {
		t.Fatalf("producer failure: %v", err)
	}
}

func TestCountLines(t *testing.T) {
	n, err := CountLines(strings.NewReader("a\tb\nc\\nd\te\n"))
	if err != nil || n != 2 {
		t.Fatalf("n = %d, err = %v", n, err)
	}
}

// fakeS3 is a path-style S3 endpoint that stores objects in memory and supports single and multipart uploads.
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
	uploads map[string]map[int][]byte
	nextID  int
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := strings.TrimPrefix(r.URL.Path, "/")
	q := r.URL.Query()
	body, _ := io.ReadAll(r.Body)
	switch {
	case r.Method == http.MethodPost && q.Has("uploads"):
		f.nextID++
		id := fmt.Sprintf("upload-%d", f.nextID)
		f.uploads[id] = map[int][]byte{}
		fmt.Fprintf(w, `<InitiateMultipartUploadResult><Bucket>b</Bucket><Key>%s</Key><UploadId>%s</UploadId></InitiateMultipartUploadResult>`, key, id)
	case r.Method == http.MethodPut && q.Has("uploadId"):
		var n int
		fmt.Sscanf(q.Get("partNumber"), "%d", &n) //nolint:errcheck // test fake
		f.uploads[q.Get("uploadId")][n] = body
		w.Header().Set("ETag", fmt.Sprintf(`"part-%d"`, n))
	case r.Method == http.MethodPost && q.Has("uploadId"):
		parts := f.uploads[q.Get("uploadId")]
		numbers := make([]int, 0, len(parts))
		for n := range parts {
			numbers = append(numbers, n)
		}
		sort.Ints(numbers)
		var joined []byte
		for _, n := range numbers {
			joined = append(joined, parts[n]...)
		}
		f.objects[key] = joined
		delete(f.uploads, q.Get("uploadId"))
		fmt.Fprintf(w, `<CompleteMultipartUploadResult><Bucket>b</Bucket><Key>%s</Key><ETag>"done"</ETag></CompleteMultipartUploadResult>`, key)
	case r.Method == http.MethodDelete && q.Has("uploadId"):
		delete(f.uploads, q.Get("uploadId"))
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPut:
		f.objects[key] = body
		w.Header().Set("ETag", `"single"`)
	case r.Method == http.MethodGet:
		data, ok := f.objects[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `<Error><Code>NoSuchKey</Code><Message>missing</Message></Error>`)
			return
		}
		_, _ = w.Write(data) //nolint:errcheck // test fake
	default:
		w.WriteHeader(http.StatusBadRequest)
	}
}

func TestS3PrefixStoresReadsAndVerifiesABackup(t *testing.T) {
	fake := &fakeS3{objects: map[string][]byte{}, uploads: map[string]map[int][]byte{}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	client := s3.New(s3.Options{
		Region: "us-east-1", BaseEndpoint: aws.String(srv.URL), UsePathStyle: true,
		Credentials:                credentials.NewStaticCredentialsProvider("id", "secret", ""),
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
	orig := s3PartSize
	s3PartSize = 8
	defer func() { s3PartSize = orig }()

	loc := NewS3Prefix(client, "bucket", "prod").Child("frameworks-backup-1")
	m := writeTestBackup(t, loc, testNow)
	if _, ok := fake.objects["bucket/prod/frameworks-backup-1/"+ManifestName]; !ok {
		t.Fatalf("manifest not stored; objects: %v", keysOf(fake.objects))
	}
	if len(fake.uploads) != 0 {
		t.Fatalf("%d multipart upload(s) left open", len(fake.uploads))
	}
	read, err := ReadManifest(context.Background(), loc)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyFiles(context.Background(), loc, read.Files(nil)); err != nil {
		t.Fatalf("multipart objects do not verify: %v", err)
	}
	if read.Databases[0].LedgerDigest != m.Databases[0].LedgerDigest {
		t.Fatal("manifest changed in transit")
	}
	if _, err := loc.Open(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing object: %v", err)
	}
}

func keysOf(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
