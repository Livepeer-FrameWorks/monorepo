// Command release-assets publishes the GitHub release for a tag and uploads its assets for the release workflow.
//
// It creates the release (or, when it exists, sets its name, prerelease flag and latest marking) and then uploads the
// assets one at a time. An asset already on the release whose SHA-256 digest and size match the local file is kept; one
// that differs, or whose earlier upload never completed, is deleted and uploaded again. Assets on the release that are
// not in the list are left alone. Every request is retried on network errors, server errors and rate limits, waiting
// as long as GitHub's Retry-After or rate-limit reset headers ask, so a re-run after a failed publication uploads only
// what is missing or changed.
//
//	release-assets --repo owner/name --tag v1.2.3 [--prerelease] [--make-latest true|false|legacy] [--generate-notes] FILE...
//
// The token comes from GH_TOKEN or GITHUB_TOKEN, the API root from GITHUB_API_URL. A FILE that does not exist or is a
// directory is skipped with a warning, so an optional artifact the workflow did not produce does not fail the release.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "release-assets:", err)
		os.Exit(1)
	}
}

type options struct {
	repo          string
	tag           string
	name          string
	prerelease    bool
	makeLatest    string
	generateNotes bool
	files         []string
}

func run(args []string, log io.Writer) error {
	fs := flag.NewFlagSet("release-assets", flag.ContinueOnError)
	var opts options
	fs.StringVar(&opts.repo, "repo", os.Getenv("GITHUB_REPOSITORY"), "repository as owner/name")
	fs.StringVar(&opts.tag, "tag", "", "release tag")
	fs.StringVar(&opts.name, "name", "", "release name (default: the tag)")
	fs.BoolVar(&opts.prerelease, "prerelease", false, "mark the release as a prerelease")
	fs.StringVar(&opts.makeLatest, "make-latest", "legacy", "latest marking: true, false or legacy")
	fs.BoolVar(&opts.generateNotes, "generate-notes", false, "generate release notes when the release is created")
	if err := fs.Parse(args); err != nil {
		return err
	}
	opts.files = fs.Args()
	if opts.name == "" {
		opts.name = opts.tag
	}
	if opts.repo == "" || opts.tag == "" {
		return errors.New("--repo and --tag are required")
	}
	switch opts.makeLatest {
	case "true", "false", "legacy":
	default:
		return fmt.Errorf("--make-latest is %q, want true, false or legacy", opts.makeLatest)
	}
	token := os.Getenv("GH_TOKEN")
	if token == "" {
		token = os.Getenv("GITHUB_TOKEN")
	}
	if token == "" {
		return errors.New("GH_TOKEN or GITHUB_TOKEN must be set")
	}
	api := os.Getenv("GITHUB_API_URL")
	if api == "" {
		api = "https://api.github.com"
	}
	c := newClient(api, token, log)
	return publish(context.Background(), c, opts)
}

type localAsset struct {
	name   string
	path   string
	size   int64
	digest string
}

// localAssets hashes the files to publish. Two files with one base name would publish as one asset, so that is refused.
func localAssets(files []string, log io.Writer) ([]localAsset, error) {
	var assets []localAsset
	seen := map[string]string{}
	for _, path := range files {
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(log, "::warning::release asset %s does not exist; skipping it\n", path)
			continue
		}
		if err != nil {
			return nil, err
		}
		if info.IsDir() {
			fmt.Fprintf(log, "::warning::release asset %s is a directory; skipping it\n", path)
			continue
		}
		name := filepath.Base(path)
		if other, ok := seen[name]; ok {
			return nil, fmt.Errorf("%s and %s would both publish as asset %s", other, path, name)
		}
		seen[name] = path
		digest, err := fileDigest(path)
		if err != nil {
			return nil, err
		}
		assets = append(assets, localAsset{name: name, path: path, size: info.Size(), digest: digest})
	}
	return assets, nil
}

func fileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

type release struct {
	ID        int64  `json:"id"`
	UploadURL string `json:"upload_url"`
}

type remoteAsset struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	State  string `json:"state"`
	Digest string `json:"digest"`
}

// matches reports whether the release already carries exactly this file. An asset without a digest cannot be compared
// and is replaced.
func (r remoteAsset) matches(a localAsset) bool {
	return r.State == "uploaded" && r.Digest == a.digest && r.Size == a.size
}

func publish(ctx context.Context, c *client, opts options) error {
	assets, err := localAssets(opts.files, c.log)
	if err != nil {
		return err
	}
	rel, err := c.ensureRelease(ctx, opts)
	if err != nil {
		return err
	}
	remote, err := c.listAssets(ctx, opts.repo, rel.ID)
	if err != nil {
		return err
	}
	var uploaded, replaced, kept int
	for _, asset := range assets {
		if existing, ok := remote[asset.name]; ok {
			if existing.matches(asset) {
				fmt.Fprintf(c.log, "keep %s: already uploaded with the same digest\n", asset.name)
				kept++
				continue
			}
			fmt.Fprintf(c.log, "replace %s: release copy is %s %s (%d bytes), local is %s (%d bytes)\n",
				asset.name, existing.State, orNone(existing.Digest), existing.Size, asset.digest, asset.size)
			if err := c.deleteAsset(ctx, opts.repo, existing.ID); err != nil {
				return err
			}
			replaced++
		} else {
			uploaded++
		}
		if err := c.uploadAsset(ctx, opts.repo, rel, asset); err != nil {
			return err
		}
	}
	fmt.Fprintf(c.log, "release %s: %d uploaded, %d replaced, %d unchanged\n", opts.tag, uploaded, replaced, kept)
	return nil
}

func orNone(s string) string {
	if s == "" {
		return "<no digest>"
	}
	return s
}

// client is a GitHub REST client that sends one request at a time, leaves at least minWriteGap between writes, and
// retries what GitHub asks callers to retry.
type client struct {
	http        *http.Client
	api         string
	token       string
	log         io.Writer
	sleep       func(time.Duration)
	now         func() time.Time
	maxAttempts int
	minWriteGap time.Duration
	lastWrite   time.Time
}

func newClient(api, token string, log io.Writer) *client {
	return &client{
		http:        &http.Client{},
		api:         strings.TrimRight(api, "/"),
		token:       token,
		log:         log,
		sleep:       time.Sleep,
		now:         time.Now,
		maxAttempts: 8,
		// GitHub's REST guidance for many writes: send them serially, at least one second apart.
		minWriteGap: time.Second,
	}
}

type request struct {
	method      string
	url         string
	contentType string
	// body opens the request body for each attempt; nil sends none.
	body    func() (io.ReadCloser, int64, error)
	timeout time.Duration
}

type response struct {
	status int
	header http.Header
	body   []byte
}

func jsonBody(v any) func() (io.ReadCloser, int64, error) {
	return func() (io.ReadCloser, int64, error) {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, 0, err
		}
		return io.NopCloser(bytes.NewReader(b)), int64(len(b)), nil
	}
}

func fileBody(path string) func() (io.ReadCloser, int64, error) {
	return func() (io.ReadCloser, int64, error) {
		f, err := os.Open(path)
		if err != nil {
			return nil, 0, err
		}
		info, err := f.Stat()
		if err != nil {
			f.Close()
			return nil, 0, err
		}
		return f, info.Size(), nil
	}
}

// do sends req until it gets a response that is not retryable, or maxAttempts run out. The caller interprets every
// non-retryable status, including 4xx.
func (c *client) do(ctx context.Context, req request) (*response, error) {
	for attempt := 1; ; attempt++ {
		if req.method != http.MethodGet {
			if wait := c.minWriteGap - c.now().Sub(c.lastWrite); wait > 0 {
				c.sleep(wait)
			}
			c.lastWrite = c.now()
		}
		resp, err := c.once(ctx, req)
		if err == nil && !retryable(resp) {
			return resp, nil
		}
		wait, reason := c.backoff(attempt, resp, err)
		if attempt >= c.maxAttempts {
			return nil, fmt.Errorf("%s %s: %s; gave up after %d attempts", req.method, redact(req.url), reason, attempt)
		}
		fmt.Fprintf(c.log, "%s %s: %s; retrying in %s (attempt %d of %d)\n",
			req.method, redact(req.url), reason, wait.Round(time.Second), attempt+1, c.maxAttempts)
		c.sleep(wait)
	}
}

func (c *client) once(ctx context.Context, req request) (*response, error) {
	timeout := req.timeout
	if timeout == 0 {
		timeout = time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var body io.ReadCloser
	var length int64
	if req.body != nil {
		var err error
		body, length, err = req.body()
		if err != nil {
			return nil, err
		}
		defer body.Close()
	}
	httpReq, err := http.NewRequestWithContext(ctx, req.method, req.url, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		httpReq.ContentLength = length
		httpReq.Header.Set("Content-Type", req.contentType)
	}
	httpReq.Header.Set("Accept", "application/vnd.github+json")
	httpReq.Header.Set("Authorization", "Bearer "+c.token)
	httpReq.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	httpResp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer httpResp.Body.Close()
	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, err
	}
	return &response{status: httpResp.StatusCode, header: httpResp.Header, body: respBody}, nil
}

// retryable reports whether GitHub asks for the request to be sent again: server errors, 429, and a 403 that is a
// primary or secondary rate limit rather than a permission refusal.
func retryable(resp *response) bool {
	switch {
	case resp.status >= 500, resp.status == http.StatusTooManyRequests:
		return true
	case resp.status == http.StatusForbidden:
		return rateLimited(resp)
	}
	return false
}

func rateLimited(resp *response) bool {
	return resp.header.Get("Retry-After") != "" ||
		resp.header.Get("X-RateLimit-Remaining") == "0" ||
		strings.Contains(strings.ToLower(string(resp.body)), "rate limit")
}

const (
	// GitHub asks for at least a minute's wait after a secondary rate limit that carries no Retry-After.
	secondaryLimitWait = time.Minute
	maxBackoff         = 5 * time.Minute
	transientWait      = 2 * time.Second
)

// backoff returns how long to wait before the next attempt: Retry-After when GitHub sends it, the primary limit's
// reset time when it is exhausted, otherwise an exponential wait that starts at a minute for rate limits and at a
// couple of seconds for network and server errors.
func (c *client) backoff(attempt int, resp *response, err error) (time.Duration, string) {
	if err != nil {
		return exponential(transientWait, attempt), err.Error()
	}
	reason := fmt.Sprintf("HTTP %d %s", resp.status, firstLine(resp.body))
	if seconds, convErr := strconv.Atoi(strings.TrimSpace(resp.header.Get("Retry-After"))); convErr == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second, reason
	}
	if resp.header.Get("X-RateLimit-Remaining") == "0" {
		if reset, convErr := strconv.ParseInt(resp.header.Get("X-RateLimit-Reset"), 10, 64); convErr == nil {
			if wait := time.Unix(reset, 0).Sub(c.now()) + time.Second; wait > 0 {
				return wait, reason
			}
			return time.Second, reason
		}
	}
	if resp.status == http.StatusForbidden || resp.status == http.StatusTooManyRequests {
		return exponential(secondaryLimitWait, attempt), reason
	}
	return exponential(transientWait, attempt), reason
}

func exponential(base time.Duration, attempt int) time.Duration {
	wait := base
	for i := 1; i < attempt && wait < maxBackoff; i++ {
		wait *= 2
	}
	return min(wait, maxBackoff)
}

func firstLine(body []byte) string {
	var message struct {
		Message string `json:"message"`
	}
	text := strings.TrimSpace(string(body))
	if json.Unmarshal(body, &message) == nil && message.Message != "" {
		text = message.Message
	}
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	}
	if len(text) > 200 {
		text = text[:200] + "..."
	}
	return text
}

// redact drops the query string, which carries nothing secret but makes upload URLs long.
func redact(raw string) string {
	if i := strings.IndexByte(raw, '?'); i >= 0 {
		return raw[:i]
	}
	return raw
}

func (c *client) repoURL(repo, format string, args ...any) string {
	return c.api + "/repos/" + repo + fmt.Sprintf(format, args...)
}

// ensureRelease returns the tag's release, creating it with generated notes when there is none. An existing release
// keeps its notes and takes the requested name, prerelease flag and latest marking.
func (c *client) ensureRelease(ctx context.Context, opts options) (*release, error) {
	for range 2 {
		rel, found, err := c.getRelease(ctx, opts)
		if err != nil {
			return nil, err
		}
		if found {
			resp, patchErr := c.do(ctx, request{method: http.MethodPatch, url: c.repoURL(opts.repo, "/releases/%d", rel.ID),
				contentType: "application/json", body: jsonBody(map[string]any{
					"name": opts.name, "prerelease": opts.prerelease, "make_latest": opts.makeLatest,
				})})
			if patchErr != nil {
				return nil, patchErr
			}
			if resp.status != http.StatusOK {
				return nil, fmt.Errorf("update release %s: HTTP %d %s", opts.tag, resp.status, firstLine(resp.body))
			}
			fmt.Fprintf(c.log, "found release %s (id %d)\n", opts.tag, rel.ID)
			return decodeRelease(resp.body)
		}
		resp, err := c.do(ctx, request{method: http.MethodPost, url: c.repoURL(opts.repo, "/releases"),
			contentType: "application/json", body: jsonBody(map[string]any{
				"tag_name": opts.tag, "name": opts.name, "prerelease": opts.prerelease,
				"make_latest": opts.makeLatest, "generate_release_notes": opts.generateNotes,
			})})
		if err != nil {
			return nil, err
		}
		switch resp.status {
		case http.StatusCreated:
			fmt.Fprintf(c.log, "created release %s\n", opts.tag)
			return decodeRelease(resp.body)
		case http.StatusUnprocessableEntity:
			// A create that GitHub completed before a dropped connection, or another run, made the release first.
			fmt.Fprintf(c.log, "create release %s: HTTP 422 %s; reading it again\n", opts.tag, firstLine(resp.body))
			continue
		default:
			return nil, fmt.Errorf("create release %s: HTTP %d %s", opts.tag, resp.status, firstLine(resp.body))
		}
	}
	return nil, fmt.Errorf("release %s neither exists nor can be created", opts.tag)
}

func (c *client) getRelease(ctx context.Context, opts options) (*release, bool, error) {
	resp, err := c.do(ctx, request{method: http.MethodGet, url: c.repoURL(opts.repo, "/releases/tags/%s", url.PathEscape(opts.tag))})
	if err != nil {
		return nil, false, err
	}
	switch resp.status {
	case http.StatusOK:
		rel, err := decodeRelease(resp.body)
		return rel, err == nil, err
	case http.StatusNotFound:
		return nil, false, nil
	default:
		return nil, false, fmt.Errorf("read release %s: HTTP %d %s", opts.tag, resp.status, firstLine(resp.body))
	}
}

func decodeRelease(body []byte) (*release, error) {
	var rel release
	if err := json.Unmarshal(body, &rel); err != nil {
		return nil, fmt.Errorf("decode release: %w", err)
	}
	if rel.ID == 0 || rel.UploadURL == "" {
		return nil, errors.New("release response has no id or upload_url")
	}
	return &rel, nil
}

func (c *client) listAssets(ctx context.Context, repo string, releaseID int64) (map[string]remoteAsset, error) {
	const perPage = 100
	assets := map[string]remoteAsset{}
	for page := 1; ; page++ {
		resp, err := c.do(ctx, request{method: http.MethodGet,
			url: c.repoURL(repo, "/releases/%d/assets?per_page=%d&page=%d", releaseID, perPage, page)})
		if err != nil {
			return nil, err
		}
		if resp.status != http.StatusOK {
			return nil, fmt.Errorf("list release assets: HTTP %d %s", resp.status, firstLine(resp.body))
		}
		var batch []remoteAsset
		if err := json.Unmarshal(resp.body, &batch); err != nil {
			return nil, fmt.Errorf("decode release assets: %w", err)
		}
		for _, asset := range batch {
			assets[asset.Name] = asset
		}
		if len(batch) < perPage {
			return assets, nil
		}
	}
}

func (c *client) deleteAsset(ctx context.Context, repo string, id int64) error {
	resp, err := c.do(ctx, request{method: http.MethodDelete, url: c.repoURL(repo, "/releases/assets/%d", id)})
	if err != nil {
		return err
	}
	if resp.status != http.StatusNoContent && resp.status != http.StatusNotFound {
		return fmt.Errorf("delete release asset %d: HTTP %d %s", id, resp.status, firstLine(resp.body))
	}
	return nil
}

// uploadAsset uploads one file and checks the digest GitHub computed. A 422 means an asset of that name is already
// there, typically from an attempt whose response was lost: it is kept when it is this file, and replaced otherwise.
func (c *client) uploadAsset(ctx context.Context, repo string, rel *release, asset localAsset) error {
	base := rel.UploadURL
	if i := strings.IndexByte(base, '{'); i >= 0 {
		base = base[:i]
	}
	target := base + "?name=" + url.QueryEscape(asset.name)
	for range 3 {
		fmt.Fprintf(c.log, "upload %s (%d bytes)\n", asset.name, asset.size)
		resp, err := c.do(ctx, request{method: http.MethodPost, url: target, contentType: "application/octet-stream",
			body: fileBody(asset.path), timeout: 30 * time.Minute})
		if err != nil {
			return err
		}
		switch resp.status {
		case http.StatusCreated:
			var got remoteAsset
			if err := json.Unmarshal(resp.body, &got); err != nil {
				return fmt.Errorf("decode uploaded asset %s: %w", asset.name, err)
			}
			if got.Digest != "" && got.Digest != asset.digest {
				return fmt.Errorf("uploaded asset %s has digest %s, want %s", asset.name, got.Digest, asset.digest)
			}
			return nil
		case http.StatusUnprocessableEntity:
			remote, err := c.listAssets(ctx, repo, rel.ID)
			if err != nil {
				return err
			}
			existing, ok := remote[asset.name]
			if !ok {
				return fmt.Errorf("upload %s: HTTP 422 %s", asset.name, firstLine(resp.body))
			}
			if existing.matches(asset) {
				fmt.Fprintf(c.log, "upload %s: an earlier attempt already uploaded it\n", asset.name)
				return nil
			}
			if err := c.deleteAsset(ctx, repo, existing.ID); err != nil {
				return err
			}
		default:
			return fmt.Errorf("upload %s: HTTP %d %s", asset.name, resp.status, firstLine(resp.body))
		}
	}
	return fmt.Errorf("upload %s: an asset of that name kept reappearing", asset.name)
}
