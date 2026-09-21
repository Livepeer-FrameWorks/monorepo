package backup

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
)

// errStreamAborted closes the inspection pipe when the store fails before or during the transfer.
var errStreamAborted = errors.New("backup stream aborted")

// StoreCompressed stores the gzip stream that produce writes under name and, in the same pass, hands the
// decompressed bytes to inspect. The object is committed only when produce and inspect both succeed.
func StoreCompressed(ctx context.Context, loc Location, name string, produce func(io.Writer) error, inspect func(io.Reader) error) (File, error) {
	pr, pw := io.Pipe()
	inspected := make(chan error, 1)
	go func() {
		err := inspectGzip(pr, inspect)
		_, _ = io.Copy(io.Discard, pr) //nolint:errcheck // drains the pipe so the producer never blocks
		inspected <- err
	}()

	var inspectErr error
	file, err := StoreFile(ctx, loc, name, func(w io.Writer) error {
		produceErr := produce(io.MultiWriter(w, pw))
		if produceErr != nil {
			pw.CloseWithError(produceErr)
			<-inspected
			return produceErr
		}
		_ = pw.Close() //nolint:errcheck // io.PipeWriter.Close always returns nil
		inspectErr = <-inspected
		if inspectErr != nil {
			return fmt.Errorf("inspect %s: %w", name, inspectErr)
		}
		return nil
	})
	pw.CloseWithError(errStreamAborted)
	return file, err
}

func inspectGzip(r io.Reader, inspect func(io.Reader) error) error {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("open gzip stream: %w", err)
	}
	defer zr.Close()
	if inspectErr := inspect(zr); inspectErr != nil {
		return inspectErr
	}
	_, err = io.Copy(io.Discard, zr)
	return err
}
