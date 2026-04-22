// Package compare is the config-drift comparison layer.
package compare

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	fwssh "frameworks/cli/pkg/ssh"
)

// Runner fetches file bytes from one location.
//
//   - err != nil: probe failure (transport, permission, unexpected exit).
//   - err == nil, missing == true: file confirmed absent on the host.
//   - err == nil, missing == false: content is the observed bytes.
type Runner interface {
	Fetch(ctx context.Context, path string) (content []byte, missing bool, err error)
}

// LocalRunner fetches from the local filesystem.
type LocalRunner struct{}

// Fetch reads path. ENOENT → missing=true, err=nil. Other errors surface
// as err; missing is false in that case.
func (LocalRunner) Fetch(_ context.Context, path string) ([]byte, bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, true, nil
		}
		return nil, false, err
	}
	return b, false, nil
}

// SSHRunner fetches via an ssh.Pool to a single host. The caller owns the
// pool lifecycle.
type SSHRunner struct {
	pool   *fwssh.Pool
	config *fwssh.ConnectionConfig
}

// NewSSHRunner binds an SSH runner to one host.
func NewSSHRunner(pool *fwssh.Pool, config *fwssh.ConnectionConfig) *SSHRunner {
	return &SSHRunner{pool: pool, config: config}
}

// Fetch runs `test -f` first so absent-file is distinguishable from other
// failures. Exit code 1 from test means "file does not exist"; any other
// non-zero exit is treated as a probe error, not as missing.
func (s *SSHRunner) Fetch(ctx context.Context, path string) ([]byte, bool, error) {
	quoted := fwssh.ShellQuote(path)

	testRes, err := s.pool.Run(ctx, s.config, fmt.Sprintf("test -f %s", quoted))
	if err != nil {
		return nil, false, fmt.Errorf("probe %s: %w", path, err)
	}
	switch testRes.ExitCode {
	case 0:
	case 1:
		return nil, true, nil
	default:
		return nil, false, fmt.Errorf("probe %s: test -f exited %d: %s", path, testRes.ExitCode, testRes.Stderr)
	}

	catRes, err := s.pool.Run(ctx, s.config, fmt.Sprintf("cat %s", quoted))
	if err != nil {
		return nil, false, fmt.Errorf("fetch %s: %w", path, err)
	}
	if catRes.ExitCode != 0 {
		return nil, false, fmt.Errorf("fetch %s: cat exited %d: %s", path, catRes.ExitCode, catRes.Stderr)
	}
	return []byte(catRes.Stdout), false, nil
}
