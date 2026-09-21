package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ErrNotFound reports an object that does not exist at a location.
var ErrNotFound = errors.New("not found")

// Location is where one backup's files live: a local directory or an S3 prefix.
type Location interface {
	// Create starts writing the named object. Nothing is visible under the name until Commit succeeds.
	Create(ctx context.Context, name string) (ObjectWriter, error)
	// Open reads the named object; a missing object returns an error wrapping ErrNotFound.
	Open(ctx context.Context, name string) (io.ReadCloser, error)
	// Child returns the location of a named subdirectory or sub-prefix.
	Child(name string) Location
	String() string
}

// ObjectWriter receives one object's bytes. Commit publishes the object; Abort discards it.
type ObjectWriter interface {
	io.Writer
	Commit() error
	Abort()
}

// ParseLocation reads an operator-supplied destination: `s3://bucket/prefix` or a local directory path.
func ParseLocation(raw string) (Location, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("backup location is empty")
	}
	if strings.HasPrefix(raw, "s3://") {
		return parseS3Location(raw)
	}
	if strings.Contains(raw, "://") {
		return nil, fmt.Errorf("backup location %q: only local directories and s3:// URLs are supported", raw)
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return nil, fmt.Errorf("backup location %q: %w", raw, err)
	}
	return LocalDir(abs), nil
}

// StoreFile writes one object through write and returns its size and SHA-256. The object is committed only when
// write succeeds.
func StoreFile(ctx context.Context, loc Location, name string, write func(io.Writer) error) (File, error) {
	w, err := loc.Create(ctx, name)
	if err != nil {
		return File{}, err
	}
	h := sha256.New()
	counter := &countingWriter{}
	if err := write(io.MultiWriter(w, h, counter)); err != nil {
		w.Abort()
		return File{}, err
	}
	if err := w.Commit(); err != nil {
		return File{}, fmt.Errorf("store %s: %w", name, err)
	}
	return File{Path: name, Size: counter.n, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

type countingWriter struct{ n int64 }

func (c *countingWriter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}

// LocalDir is a backup location on the local file system.
type LocalDir string

func (d LocalDir) String() string { return string(d) }

// Child returns a subdirectory.
func (d LocalDir) Child(name string) Location { return LocalDir(filepath.Join(string(d), name)) }

func (d LocalDir) path(name string) (string, error) {
	clean := path.Clean("/" + name)
	if clean == "/" || strings.Contains(name, "\\") {
		return "", fmt.Errorf("invalid backup object name %q", name)
	}
	return filepath.Join(string(d), filepath.FromSlash(strings.TrimPrefix(clean, "/"))), nil
}

// Create writes to a temporary sibling and renames it into place on Commit.
func (d LocalDir) Create(_ context.Context, name string) (ObjectWriter, error) {
	target, err := d.path(name)
	if err != nil {
		return nil, err
	}
	if mkErr := os.MkdirAll(filepath.Dir(target), 0o700); mkErr != nil {
		return nil, fmt.Errorf("create %s: %w", filepath.Dir(target), mkErr)
	}
	f, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".partial-*")
	if err != nil {
		return nil, fmt.Errorf("create %s: %w", target, err)
	}
	return &localWriter{f: f, target: target}, nil
}

// Open opens a file in the directory.
func (d LocalDir) Open(_ context.Context, name string) (io.ReadCloser, error) {
	target, err := d.path(name)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(target)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%s: %w", target, ErrNotFound)
		}
		return nil, err
	}
	return f, nil
}

type localWriter struct {
	f      *os.File
	target string
}

func (w *localWriter) Write(p []byte) (int, error) { return w.f.Write(p) }

func (w *localWriter) Commit() error {
	if err := w.f.Sync(); err != nil {
		w.Abort()
		return err
	}
	if err := w.f.Close(); err != nil {
		_ = os.Remove(w.f.Name()) //nolint:errcheck // best-effort removal of the partial file
		return err
	}
	return os.Rename(w.f.Name(), w.target)
}

func (w *localWriter) Abort() {
	_ = w.f.Close()           //nolint:errcheck // the partial file is removed below
	_ = os.Remove(w.f.Name()) //nolint:errcheck // best-effort removal of the partial file
}
