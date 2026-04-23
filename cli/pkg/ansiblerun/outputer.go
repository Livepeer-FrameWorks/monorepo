package ansiblerun

import (
	"bufio"
	"context"
	"io"

	goansible_result "github.com/apenella/go-ansible/v2/pkg/execute/result"
)

// LineOutputer streams ansible-playbook's combined output line-by-line through
// a caller-supplied writer. The CLI's Tier-U formatter layers on top by
// wrapping an io.Writer that prefixes or colorizes each line.
//
// A Prefix (if set) is emitted before each line, allowing the CLI to tag
// playbook output without consuming context from upstream pipes.
type LineOutputer struct {
	// W is the destination. Required; typically os.Stdout or a Tier-U
	// formatting wrapper.
	W io.Writer

	// Prefix is prepended to each line (no trailing space; set your own if
	// you want spacing). Empty means raw passthrough.
	Prefix string
}

// Print satisfies goansible_result.ResultsOutputer. It reads from reader one
// line at a time and flushes each line to the caller's writer as soon as it
// arrives, preserving the real-time feel of ansible-playbook output.
func (o *LineOutputer) Print(ctx context.Context, reader io.Reader, _ io.Writer, _ ...goansible_result.OptionsFunc) error {
	dst := o.W
	if dst == nil {
		return nil
	}

	scanner := bufio.NewScanner(reader)
	// Ansible lines can exceed the default 64 KiB limit on change-diff
	// output; raise the ceiling rather than silently truncate.
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if o.Prefix != "" {
			if _, err := io.WriteString(dst, o.Prefix); err != nil {
				return err
			}
		}
		if _, err := dst.Write(scanner.Bytes()); err != nil {
			return err
		}
		if _, err := io.WriteString(dst, "\n"); err != nil {
			return err
		}
	}
	return scanner.Err()
}
