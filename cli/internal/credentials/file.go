package credentials

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type fileStore struct {
	mu   sync.Mutex
	path string
}

func newFileStore() Store {
	return &fileStore{}
}

func (s *fileStore) Name() string { return "file" }

func (s *fileStore) resolvePath() (string, error) {
	if s.path != "" {
		return s.path, nil
	}
	base := strings.TrimSpace(os.Getenv("XDG_DATA_HOME"))
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "share")
	}
	dir := filepath.Join(base, "frameworks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	s.path = filepath.Join(dir, "credentials")
	return s.path, nil
}

func (s *fileStore) Get(account string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := s.readLocked()
	if err != nil {
		return "", err
	}
	return entries[account], nil
}

func (s *fileStore) Set(account, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := s.readLocked()
	if err != nil {
		return err
	}
	entries[account] = value
	return s.writeLocked(entries)
}

func (s *fileStore) Delete(account string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := s.readLocked()
	if err != nil {
		return err
	}
	if _, ok := entries[account]; !ok {
		return nil
	}
	delete(entries, account)
	return s.writeLocked(entries)
}

func (s *fileStore) readLocked() (map[string]string, error) {
	path, err := s.resolvePath()
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		out[line[:eq]] = line[eq+1:]
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan %s: %w", path, err)
	}
	return out, nil
}

func (s *fileStore) writeLocked(entries map[string]string) error {
	path, err := s.resolvePath()
	if err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("# Frameworks credentials — managed by the CLI. Do not edit by hand.\n")
	for account, value := range entries {
		fmt.Fprintf(&b, "%s=%s\n", account, value)
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}
