package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// fakeS3API is a hand-written implementation of the s3API seam. It records the
// last input for each call so tests can assert the exact request shape Foghorn
// sends (bucket/key correctness is the invariant — edges never get creds).
type fakeS3API struct {
	putIn      *s3.PutObjectInput
	deleteIn   *s3.DeleteObjectInput
	delObjsIn  []*s3.DeleteObjectsInput
	headIn     *s3.HeadObjectInput
	listIn     *s3.ListObjectsV2Input
	createIn   *s3.CreateMultipartUploadInput
	completeIn *s3.CompleteMultipartUploadInput
	abortIn    *s3.AbortMultipartUploadInput
	listPartIn *s3.ListPartsInput

	// programmable behavior
	headErr      error
	headLen      *int64
	listPages    []*s3.ListObjectsV2Output // returned in order across paginator pages
	listPartsOut *s3.ListPartsOutput
	createID     string
	putErr       error
	deleteErr    error
	delObjsErr   error
}

func (f *fakeS3API) PutObject(_ context.Context, params *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	f.putIn = params
	if f.putErr != nil {
		return nil, f.putErr
	}
	return &s3.PutObjectOutput{}, nil
}

func (f *fakeS3API) DeleteObject(_ context.Context, params *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	f.deleteIn = params
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	return &s3.DeleteObjectOutput{}, nil
}

func (f *fakeS3API) DeleteObjects(_ context.Context, params *s3.DeleteObjectsInput, _ ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error) {
	f.delObjsIn = append(f.delObjsIn, params)
	if f.delObjsErr != nil {
		return nil, f.delObjsErr
	}
	return &s3.DeleteObjectsOutput{}, nil
}

func (f *fakeS3API) HeadObject(_ context.Context, params *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	f.headIn = params
	if f.headErr != nil {
		return nil, f.headErr
	}
	return &s3.HeadObjectOutput{ContentLength: f.headLen}, nil
}

func (f *fakeS3API) ListObjectsV2(_ context.Context, params *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	f.listIn = params
	// Drive the SDK paginator: return one page per call, set NextContinuationToken
	// while pages remain so the paginator asks again.
	if len(f.listPages) == 0 {
		return &s3.ListObjectsV2Output{}, nil
	}
	page := f.listPages[0]
	f.listPages = f.listPages[1:]
	if len(f.listPages) > 0 {
		tok := "more"
		page.IsTruncated = aws.Bool(true)
		page.NextContinuationToken = aws.String(tok)
	} else {
		page.IsTruncated = aws.Bool(false)
		page.NextContinuationToken = nil
	}
	return page, nil
}

func (f *fakeS3API) CreateMultipartUpload(_ context.Context, params *s3.CreateMultipartUploadInput, _ ...func(*s3.Options)) (*s3.CreateMultipartUploadOutput, error) {
	f.createIn = params
	id := f.createID
	if id == "" {
		id = "upload-xyz"
	}
	return &s3.CreateMultipartUploadOutput{UploadId: aws.String(id)}, nil
}

func (f *fakeS3API) CompleteMultipartUpload(_ context.Context, params *s3.CompleteMultipartUploadInput, _ ...func(*s3.Options)) (*s3.CompleteMultipartUploadOutput, error) {
	f.completeIn = params
	return &s3.CompleteMultipartUploadOutput{}, nil
}

func (f *fakeS3API) AbortMultipartUpload(_ context.Context, params *s3.AbortMultipartUploadInput, _ ...func(*s3.Options)) (*s3.AbortMultipartUploadOutput, error) {
	f.abortIn = params
	return &s3.AbortMultipartUploadOutput{}, nil
}

func (f *fakeS3API) ListParts(_ context.Context, params *s3.ListPartsInput, _ ...func(*s3.Options)) (*s3.ListPartsOutput, error) {
	f.listPartIn = params
	if f.listPartsOut != nil {
		return f.listPartsOut, nil
	}
	return &s3.ListPartsOutput{}, nil
}

// fakePresigner implements s3Presigner. It echoes the requested key into the
// returned URL and records the effective Expires so tests can assert that
// GeneratePresigned* both scopes the URL to the object and honors expiry.
type fakePresigner struct {
	lastPutExpiry  time.Duration
	lastGetExpiry  time.Duration
	lastPartExpiry time.Duration
	lastPartNumber int32
	err            error
}

func expiryOf(optFns []func(*s3.PresignOptions)) time.Duration {
	var o s3.PresignOptions
	for _, fn := range optFns {
		fn(&o)
	}
	return o.Expires
}

func (p *fakePresigner) PresignPutObject(_ context.Context, params *s3.PutObjectInput, optFns ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error) {
	if p.err != nil {
		return nil, p.err
	}
	p.lastPutExpiry = expiryOf(optFns)
	return &v4.PresignedHTTPRequest{
		URL:    fmt.Sprintf("https://example.test/%s/%s?X-Amz-Expires=put", aws.ToString(params.Bucket), aws.ToString(params.Key)),
		Method: "PUT",
	}, nil
}

func (p *fakePresigner) PresignGetObject(_ context.Context, params *s3.GetObjectInput, optFns ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error) {
	if p.err != nil {
		return nil, p.err
	}
	p.lastGetExpiry = expiryOf(optFns)
	return &v4.PresignedHTTPRequest{
		URL:    fmt.Sprintf("https://example.test/%s/%s?X-Amz-Expires=get", aws.ToString(params.Bucket), aws.ToString(params.Key)),
		Method: "GET",
	}, nil
}

func (p *fakePresigner) PresignUploadPart(_ context.Context, params *s3.UploadPartInput, optFns ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error) {
	if p.err != nil {
		return nil, p.err
	}
	p.lastPartExpiry = expiryOf(optFns)
	p.lastPartNumber = aws.ToInt32(params.PartNumber)
	return &v4.PresignedHTTPRequest{
		URL:    fmt.Sprintf("https://example.test/%s/%s?partNumber=%d", aws.ToString(params.Bucket), aws.ToString(params.Key), aws.ToInt32(params.PartNumber)),
		Method: "PUT",
	}, nil
}

func newTestClient(t *testing.T, api *fakeS3API, pre *fakePresigner) *S3Client {
	t.Helper()
	return newS3ClientWithAPI(S3Config{Bucket: "frameworks", Prefix: "prod"}, api, pre, logging.NewLogger())
}

// Invariant: presigned PUT/GET return a URL scoped to bucket+prefixed key and
// honor an explicit expiry (the only thing edges receive — no credentials).
func TestGeneratePresignedPUTGET_URLAndExpiry(t *testing.T) {
	api := &fakeS3API{}
	pre := &fakePresigner{}
	c := newTestClient(t, api, pre)

	putURL, err := c.GeneratePresignedPUT("clips/a/b.mp4", 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(putURL, "frameworks/prod/clips/a/b.mp4") {
		t.Fatalf("PUT URL not scoped to prefixed key: %s", putURL)
	}
	if pre.lastPutExpiry != 30*time.Minute {
		t.Fatalf("PUT expiry not honored: %v", pre.lastPutExpiry)
	}

	getURL, err := c.GeneratePresignedGET("clips/a/b.mp4", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(getURL, "frameworks/prod/clips/a/b.mp4") {
		t.Fatalf("GET URL not scoped to prefixed key: %s", getURL)
	}
	// expiry 0 -> default 15m
	if pre.lastGetExpiry != 15*time.Minute {
		t.Fatalf("GET default expiry not applied: %v", pre.lastGetExpiry)
	}
}

// Invariant: presign error is surfaced, not swallowed.
func TestGeneratePresigned_ErrorPropagates(t *testing.T) {
	c := newTestClient(t, &fakeS3API{}, &fakePresigner{err: errors.New("signer down")})
	if _, err := c.GeneratePresignedPUT("k", time.Minute); err == nil {
		t.Fatal("expected error from presigner")
	}
	if _, err := c.GeneratePresignedGET("k", time.Minute); err == nil {
		t.Fatal("expected error from presigner")
	}
}

// Invariant: PutObject sends prefixed key + body to the configured bucket.
func TestPutObject_RequestShape(t *testing.T) {
	api := &fakeS3API{}
	c := newTestClient(t, api, &fakePresigner{})
	if err := c.PutObject(context.Background(), "meta.json", []byte("{}"), "application/json"); err != nil {
		t.Fatal(err)
	}
	if api.putIn == nil {
		t.Fatal("PutObject not called")
	}
	if aws.ToString(api.putIn.Bucket) != "frameworks" {
		t.Fatalf("wrong bucket: %s", aws.ToString(api.putIn.Bucket))
	}
	if aws.ToString(api.putIn.Key) != "prod/meta.json" {
		t.Fatalf("wrong key: %s", aws.ToString(api.putIn.Key))
	}
	if aws.ToString(api.putIn.ContentType) != "application/json" {
		t.Fatalf("content type not set: %s", aws.ToString(api.putIn.ContentType))
	}
}

// Invariant: PutObject error is wrapped and returned.
func TestPutObject_Error(t *testing.T) {
	api := &fakeS3API{putErr: errors.New("boom")}
	c := newTestClient(t, api, &fakePresigner{})
	if err := c.PutObject(context.Background(), "k", []byte("x"), ""); err == nil {
		t.Fatal("expected put error")
	}
}

// Invariant: Delete targets the prefixed key in the configured bucket.
func TestDelete_RequestShape(t *testing.T) {
	api := &fakeS3API{}
	c := newTestClient(t, api, &fakePresigner{})
	if err := c.Delete(context.Background(), "clips/x.mp4"); err != nil {
		t.Fatal(err)
	}
	if aws.ToString(api.deleteIn.Key) != "prod/clips/x.mp4" {
		t.Fatalf("wrong delete key: %s", aws.ToString(api.deleteIn.Key))
	}
	if aws.ToString(api.deleteIn.Bucket) != "frameworks" {
		t.Fatalf("wrong delete bucket: %s", aws.ToString(api.deleteIn.Bucket))
	}
}

// Invariant: DeletePrefix lists under the prefixed path and batch-deletes every
// returned object across all paginator pages, returning the total count.
func TestDeletePrefix_BatchesAcrossPages(t *testing.T) {
	api := &fakeS3API{
		listPages: []*s3.ListObjectsV2Output{
			{Contents: []types.Object{{Key: aws.String("prod/dvr/a/1")}, {Key: aws.String("prod/dvr/a/2")}}},
			{Contents: []types.Object{{Key: aws.String("prod/dvr/a/3")}}},
		},
	}
	c := newTestClient(t, api, &fakePresigner{})
	n, err := c.DeletePrefix(context.Background(), "dvr/a")
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("expected 3 deleted, got %d", n)
	}
	if aws.ToString(api.listIn.Prefix) != "prod/dvr/a" {
		t.Fatalf("list prefix wrong: %s", aws.ToString(api.listIn.Prefix))
	}
	if len(api.delObjsIn) != 2 {
		t.Fatalf("expected 2 delete batches (one per page), got %d", len(api.delObjsIn))
	}
}

// Invariant: DeleteByURL parses the s3:// URL, strips bucket+prefix, and deletes
// the underlying object via the same prefixed-key path as Delete.
func TestDeleteByURL_RoundTrip(t *testing.T) {
	api := &fakeS3API{}
	c := newTestClient(t, api, &fakePresigner{})
	url := c.BuildS3URL("clips/t/s/h.mp4") // s3://frameworks/prod/clips/t/s/h.mp4
	if err := c.DeleteByURL(context.Background(), url); err != nil {
		t.Fatal(err)
	}
	if aws.ToString(api.deleteIn.Key) != "prod/clips/t/s/h.mp4" {
		t.Fatalf("DeleteByURL did not re-derive prefixed key: %s", aws.ToString(api.deleteIn.Key))
	}
}

// Invariant: Exists treats a 404/NotFound HEAD as not-found (false, nil), not an
// error; a real error stays an error; a 2xx is true.
func TestExists_NotFoundIsNotError(t *testing.T) {
	cFound := newTestClient(t, &fakeS3API{}, &fakePresigner{})
	ok, err := cFound.Exists(context.Background(), "k")
	if err != nil || !ok {
		t.Fatalf("expected exists=true, got %v %v", ok, err)
	}

	cMissing := newTestClient(t, &fakeS3API{headErr: errors.New("api error NotFound: 404")}, &fakePresigner{})
	ok, err = cMissing.Exists(context.Background(), "k")
	if err != nil {
		t.Fatalf("404 must not be an error, got %v", err)
	}
	if ok {
		t.Fatal("expected exists=false for 404")
	}

	cErr := newTestClient(t, &fakeS3API{headErr: errors.New("AccessDenied")}, &fakePresigner{})
	if _, err := cErr.Exists(context.Background(), "k"); err == nil {
		t.Fatal("non-404 HEAD error should propagate")
	}
}

// Invariant: GetObjectSize returns the HEAD ContentLength; nil length -> 0.
func TestGetObjectSize(t *testing.T) {
	size := int64(4096)
	c := newTestClient(t, &fakeS3API{headLen: &size}, &fakePresigner{})
	n, err := c.GetObjectSize(context.Background(), "k")
	if err != nil || n != 4096 {
		t.Fatalf("expected 4096, got %d (%v)", n, err)
	}

	cNil := newTestClient(t, &fakeS3API{headLen: nil}, &fakePresigner{})
	n, err = cNil.GetObjectSize(context.Background(), "k")
	if err != nil || n != 0 {
		t.Fatalf("nil ContentLength should be 0, got %d (%v)", n, err)
	}

	cErr := newTestClient(t, &fakeS3API{headErr: errors.New("boom")}, &fakePresigner{})
	if _, err := cErr.GetObjectSize(context.Background(), "k"); err == nil {
		t.Fatal("expected error to propagate")
	}
}

// Invariant: ListPrefix returns relative keys (configured prefix stripped) so
// callers get tenant-scoped relative paths, not the storage-internal prefix.
func TestListPrefix_StripsPrefix(t *testing.T) {
	api := &fakeS3API{
		listPages: []*s3.ListObjectsV2Output{
			{Contents: []types.Object{
				{Key: aws.String("prod/dvr/a/seg1.ts")},
				{Key: aws.String("prod/dvr/a/seg2.ts")},
			}},
		},
	}
	c := newTestClient(t, api, &fakePresigner{})
	keys, err := c.ListPrefix(context.Background(), "dvr/a")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"dvr/a/seg1.ts", "dvr/a/seg2.ts"}
	if len(keys) != len(want) {
		t.Fatalf("expected %v, got %v", want, keys)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("key %d: expected %s, got %s", i, want[i], keys[i])
		}
	}
}

// Invariant: buildS3URL produces the s3://bucket/prefix/key form used for DB
// storage; round-trips back to the same prefixed key via ParseS3URL.
func TestBuildS3URL_Format(t *testing.T) {
	c := newTestClient(t, &fakeS3API{}, &fakePresigner{})
	url := c.BuildS3URL("clips/t/s/h.mp4")
	if url != "s3://frameworks/prod/clips/t/s/h.mp4" {
		t.Fatalf("unexpected URL: %s", url)
	}
	key, err := c.ParseS3URL(url)
	if err != nil {
		t.Fatal(err)
	}
	if key != "clips/t/s/h.mp4" {
		t.Fatalf("round-trip key mismatch: %s", key)
	}
}

// Invariant: the multipart create -> presign-parts -> complete flow threads the
// upload ID and prefixed key through every call, and presigned part URLs carry
// the right 1-indexed part numbers (parallel upload correctness).
func TestMultipartUpload_CreatePresignComplete(t *testing.T) {
	api := &fakeS3API{createID: "mpu-123"}
	pre := &fakePresigner{}
	c := newTestClient(t, api, pre)
	ctx := context.Background()

	uploadID, err := c.CreateMultipartUpload(ctx, "vod/t/h.mp4", "video/mp4")
	if err != nil {
		t.Fatal(err)
	}
	if uploadID != "mpu-123" {
		t.Fatalf("unexpected upload id: %s", uploadID)
	}
	if aws.ToString(api.createIn.Key) != "prod/vod/t/h.mp4" {
		t.Fatalf("create used wrong key: %s", aws.ToString(api.createIn.Key))
	}
	if aws.ToString(api.createIn.ContentType) != "video/mp4" {
		t.Fatalf("create content type not set")
	}

	parts, err := c.GeneratePresignedUploadParts("vod/t/h.mp4", uploadID, 3, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 3 {
		t.Fatalf("expected 3 parts, got %d", len(parts))
	}
	for i, p := range parts {
		if p.PartNumber != i+1 {
			t.Fatalf("part %d has number %d", i, p.PartNumber)
		}
		if !strings.Contains(p.PresignedURL, fmt.Sprintf("partNumber=%d", i+1)) {
			t.Fatalf("part URL missing 1-indexed number: %s", p.PresignedURL)
		}
	}
	// expiry 0 -> default 2h for multipart parts
	if pre.lastPartExpiry != 2*time.Hour {
		t.Fatalf("multipart part default expiry not applied: %v", pre.lastPartExpiry)
	}

	err = c.CompleteMultipartUpload(ctx, "vod/t/h.mp4", uploadID, []CompletedPart{
		{PartNumber: 1, ETag: "e1"},
		{PartNumber: 2, ETag: "e2"},
		{PartNumber: 3, ETag: "e3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if aws.ToString(api.completeIn.UploadId) != "mpu-123" {
		t.Fatalf("complete used wrong upload id: %s", aws.ToString(api.completeIn.UploadId))
	}
	if api.completeIn.MultipartUpload == nil || len(api.completeIn.MultipartUpload.Parts) != 3 {
		t.Fatalf("complete did not forward all parts")
	}
	if aws.ToInt32(api.completeIn.MultipartUpload.Parts[0].PartNumber) != 1 ||
		aws.ToString(api.completeIn.MultipartUpload.Parts[0].ETag) != "e1" {
		t.Fatalf("complete part mapping wrong: %+v", api.completeIn.MultipartUpload.Parts[0])
	}
}

// Invariant: AbortMultipartUpload targets the prefixed key + upload id so a
// failed VOD upload is cleanly torn down (no orphaned multipart on the bucket).
func TestAbortMultipartUpload(t *testing.T) {
	api := &fakeS3API{}
	c := newTestClient(t, api, &fakePresigner{})
	if err := c.AbortMultipartUpload(context.Background(), "vod/t/h.mp4", "mpu-9"); err != nil {
		t.Fatal(err)
	}
	if aws.ToString(api.abortIn.Key) != "prod/vod/t/h.mp4" {
		t.Fatalf("abort wrong key: %s", aws.ToString(api.abortIn.Key))
	}
	if aws.ToString(api.abortIn.UploadId) != "mpu-9" {
		t.Fatalf("abort wrong upload id: %s", aws.ToString(api.abortIn.UploadId))
	}
}

// Invariant: ListUploadedParts reconciles server-side part truth — it maps SDK
// parts to UploadedPart, strips quoted ETags, and skips nil part numbers.
func TestListUploadedParts_MapsAndStrips(t *testing.T) {
	size := int64(5 * 1024 * 1024)
	api := &fakeS3API{
		listPartsOut: &s3.ListPartsOutput{
			Parts: []types.Part{
				{PartNumber: aws.Int32(1), ETag: aws.String("\"abc\""), Size: &size},
				{PartNumber: nil, ETag: aws.String("\"skip\"")},
				{PartNumber: aws.Int32(2), ETag: aws.String("def")},
			},
		},
	}
	c := newTestClient(t, api, &fakePresigner{})
	parts, err := c.ListUploadedParts(context.Background(), "vod/t/h.mp4", "mpu-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts (nil skipped), got %d", len(parts))
	}
	if parts[0].PartNumber != 1 || parts[0].ETag != "abc" || parts[0].SizeBytes != size {
		t.Fatalf("part0 mapped wrong: %+v", parts[0])
	}
	if parts[1].ETag != "def" {
		t.Fatalf("part1 etag wrong: %s", parts[1].ETag)
	}
	if aws.ToString(api.listPartIn.Key) != "prod/vod/t/h.mp4" {
		t.Fatalf("list parts used wrong key: %s", aws.ToString(api.listPartIn.Key))
	}
}
