package frameworks

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// UploadVodOptions configures UploadVod.
type UploadVodOptions struct {
	Filename    string
	ContentType string
	Title       string
	Description string
	// Concurrency is the parts sent at once; 0 means 4.
	Concurrency int
	// PartAttempts is the attempts per part, including the first; 0 means 3.
	PartAttempts int
	// OnProgress is called after each part with the bytes uploaded so far.
	OnProgress func(uploadedBytes, totalBytes int64)
	// HTTPClient sends the part PUTs; defaults to http.DefaultClient.
	HTTPClient *http.Client
}

// UploadedVodAsset is the VOD asset completeVodUpload returns.
type UploadedVodAsset = CompleteVodUploadCompleteVodUploadVodAsset

func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// UploadVod uploads size bytes of src as a VOD asset: createVodUpload, a PUT
// of every part to its presigned URL (at most Concurrency at once, each
// retried on network errors, 408, 429, and 5xx, waiting a storage
// Retry-After up to the client's RetryPolicy.MaxRetryAfter), then
// completeVodUpload with the part ETags. Errors never carry a presigned URL. Any failure after the upload is created aborts it with
// abortVodUpload before the error is returned.
func UploadVod(ctx context.Context, client *Client, src io.ReaderAt, size int64, opts UploadVodOptions) (*UploadedVodAsset, error) {
	created, err := CreateVodUpload(ctx, client, CreateVodUploadInput{
		Filename:    opts.Filename,
		SizeBytes:   float64(size),
		ContentType: optionalString(opts.ContentType),
		Title:       optionalString(opts.Title),
		Description: optionalString(opts.Description),
	})
	if err != nil {
		return nil, err
	}
	session, err := ExpectResult[*CreateVodUploadCreateVodUploadVodUploadSession](created.CreateVodUpload)
	if err != nil {
		return nil, err
	}
	asset, err := uploadParts(ctx, client, session, src, size, opts)
	if err != nil {
		// Abort with a fresh context so a cancelled upload is still released.
		abortCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if _, abortErr := AbortVodUpload(abortCtx, client, session.Id); abortErr != nil {
			return nil, errors.Join(err, fmt.Errorf("frameworks: aborting upload %s: %w", session.Id, abortErr))
		}
		return nil, err
	}
	return asset, nil
}

func uploadParts(ctx context.Context, client *Client, session *CreateVodUploadCreateVodUploadVodUploadSession, src io.ReaderAt, size int64, opts UploadVodOptions) (*UploadedVodAsset, error) {
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	attempts := opts.PartAttempts
	if attempts < 1 {
		attempts = 3
	}
	concurrency := opts.Concurrency
	if concurrency < 1 {
		concurrency = 4
	}
	partSize := int64(session.PartSize)
	parts := append([]CreateVodUploadCreateVodUploadVodUploadSessionPartsVodUploadPart(nil), session.Parts...)
	sort.Slice(parts, func(i, j int) bool { return parts[i].PartNumber < parts[j].PartNumber })

	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	var (
		mu       sync.Mutex
		etags    = map[int]string{}
		uploaded atomic.Int64
		next     atomic.Int64
		wg       sync.WaitGroup
		firstErr error
		errOnce  sync.Once
	)
	fail := func(err error) {
		errOnce.Do(func() {
			firstErr = err
			cancel(err)
		})
	}
	for range min(concurrency, len(parts)) {
		wg.Go(func() {
			for {
				i := int(next.Add(1) - 1)
				if i >= len(parts) || ctx.Err() != nil {
					return
				}
				part := parts[i]
				start := int64(part.PartNumber-1) * partSize
				end := min(start+partSize, size)
				etag, err := putPart(ctx, client, httpClient, session.Id, part.PartNumber, part.PresignedUrl, io.NewSectionReader(src, start, end-start), attempts)
				if err != nil {
					fail(err)
					return
				}
				mu.Lock()
				etags[part.PartNumber] = etag
				mu.Unlock()
				done := uploaded.Add(end - start)
				if opts.OnProgress != nil {
					opts.OnProgress(done, size)
				}
			}
		})
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}

	completed := make([]VodUploadCompletedPart, 0, len(parts))
	for _, p := range parts {
		completed = append(completed, VodUploadCompletedPart{PartNumber: p.PartNumber, Etag: etags[p.PartNumber]})
	}
	resp, err := CompleteVodUpload(ctx, client, CompleteVodUploadInput{UploadId: session.Id, Parts: completed})
	if err != nil {
		return nil, err
	}
	return ExpectResult[*UploadedVodAsset](resp.CompleteVodUpload)
}

// withoutURL drops the request URL that net/http's *url.Error quotes. A
// presigned part URL's query is a credential, so errors carry only the
// underlying failure.
func withoutURL(err error) error {
	var urlErr *neturl.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		return fmt.Errorf("%s: %w", urlErr.Op, urlErr.Err)
	}
	return err
}

// putPart sends one part and returns its ETag.
func putPart(ctx context.Context, client *Client, httpClient *http.Client, uploadID string, partNumber int, url string, body *io.SectionReader, attempts int) (string, error) {
	for attempt := 1; ; attempt++ {
		failure, retryAfter, retryable, etag := putOnce(ctx, httpClient, url, body, uploadID, partNumber)
		if failure == nil {
			return etag, nil
		}
		if ctx.Err() != nil {
			return "", context.Cause(ctx)
		}
		if !retryable || attempt >= attempts {
			return "", failure
		}
		delay := backoffDelay(DefaultRetryPolicy, attempt)
		if retryAfter != nil {
			delay = time.Duration(*retryAfter) * time.Second
			// A Retry-After above the client's GraphQL maximum ends retrying
			// instead of waiting, as it does for GraphQL requests.
			if delay > client.retry.MaxRetryAfter {
				return "", failure
			}
		}
		if err := client.sleep(ctx, delay); err != nil {
			return "", err
		}
	}
}

func putOnce(ctx context.Context, httpClient *http.Client, url string, body *io.SectionReader, uploadID string, partNumber int) (*UploadError, *int, bool, string) {
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return &UploadError{ErrorInfo: ErrorInfo{Message: err.Error(), Cause: err}, UploadID: uploadID, PartNumber: partNumber}, nil, false, ""
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, io.NopCloser(body))
	if err != nil {
		err = withoutURL(err)
		return &UploadError{ErrorInfo: ErrorInfo{Message: err.Error(), Cause: err}, UploadID: uploadID, PartNumber: partNumber}, nil, false, ""
	}
	req.ContentLength = body.Size()
	res, err := httpClient.Do(req)
	if err != nil {
		err = withoutURL(err)
		return &UploadError{
			ErrorInfo:  ErrorInfo{Message: fmt.Sprintf("part %d failed: %v", partNumber, err), Cause: &NetworkError{ErrorInfo{Message: err.Error(), Cause: err}}},
			UploadID:   uploadID,
			PartNumber: partNumber,
		}, nil, true, ""
	}
	_, drainErr := io.Copy(io.Discard, res.Body)
	if closeErr := res.Body.Close(); drainErr == nil {
		drainErr = closeErr
	}
	if drainErr != nil {
		drainErr = withoutURL(drainErr)
	}
	etag := res.Header.Get("ETag")
	if res.StatusCode >= 200 && res.StatusCode < 300 && etag != "" {
		if drainErr != nil {
			return &UploadError{
				ErrorInfo:  ErrorInfo{Message: fmt.Sprintf("part %d response failed: %v", partNumber, drainErr), Cause: drainErr},
				UploadID:   uploadID,
				PartNumber: partNumber,
			}, nil, true, ""
		}
		return nil, nil, false, etag
	}
	msg := fmt.Sprintf("part %d failed with HTTP %d", partNumber, res.StatusCode)
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		msg = fmt.Sprintf("part %d returned no ETag", partNumber)
	}
	var retryAfter *int
	if n, ok := parseRetryAfter(res.Header.Get("Retry-After"), time.Now()); ok {
		retryAfter = &n
	}
	return &UploadError{ErrorInfo: ErrorInfo{Message: msg, Status: intPtr(res.StatusCode)}, UploadID: uploadID, PartNumber: partNumber},
		retryAfter, retryableStatus(res.StatusCode), ""
}
