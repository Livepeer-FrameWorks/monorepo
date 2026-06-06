package ansiblerun

import (
	"bufio"
	"context"
	"io"
	"strconv"
	"strings"

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

// RecapHost is the parsed PLAY RECAP summary for one Ansible host.
type RecapHost struct {
	Changed int
}

// RecapOutputer captures Ansible output and parses PLAY RECAP changed counts.
// When W is nil it is silent, which is useful for live provision prechecks
// that only need the yes/no change decision.
type RecapOutputer struct {
	W      io.Writer
	Prefix string
	Hosts  map[string]RecapHost
}

func (o *RecapOutputer) Print(ctx context.Context, reader io.Reader, _ io.Writer, _ ...goansible_result.OptionsFunc) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		line := scanner.Text()
		if host, recap, ok := parseRecapLine(line); ok {
			if o.Hosts == nil {
				o.Hosts = map[string]RecapHost{}
			}
			o.Hosts[host] = recap
		}
		if o.W != nil {
			if o.Prefix != "" {
				if _, err := io.WriteString(o.W, o.Prefix); err != nil {
					return err
				}
			}
			if _, err := io.WriteString(o.W, line); err != nil {
				return err
			}
			if _, err := io.WriteString(o.W, "\n"); err != nil {
				return err
			}
		}
	}
	return scanner.Err()
}

func (o *RecapOutputer) HasRecap() bool {
	return o != nil && len(o.Hosts) > 0
}

func (o *RecapOutputer) Changed() bool {
	if o == nil {
		return false
	}
	for _, host := range o.Hosts {
		if host.Changed > 0 {
			return true
		}
	}
	return false
}

func parseRecapLine(line string) (string, RecapHost, bool) {
	before, after, ok := strings.Cut(strings.TrimSpace(line), ":")
	if !ok {
		return "", RecapHost{}, false
	}
	host := strings.TrimSpace(before)
	if host == "" {
		return "", RecapHost{}, false
	}
	recap := RecapHost{}
	foundChanged := false
	for _, field := range strings.Fields(after) {
		key, value, ok := strings.Cut(field, "=")
		if !ok || key != "changed" {
			continue
		}
		changed, err := strconv.Atoi(value)
		if err != nil {
			return "", RecapHost{}, false
		}
		recap.Changed = changed
		foundChanged = true
	}
	if !foundChanged {
		return "", RecapHost{}, false
	}
	return host, recap, true
}
