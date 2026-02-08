package ssh

import (
	"crypto/rand"
	"crypto/rsa"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestTrustAndSaveHostKeyConcurrent(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	knownHostsPath := filepath.Join(tempDir, "known_hosts")

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}

	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatalf("failed to create signer: %v", err)
	}

	host := "example.com:22"
	remote := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 22}
	publicKey := signer.PublicKey()

	var wg sync.WaitGroup
	errs := make(chan error, 8)

	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if saveErr := trustAndSaveHostKey(knownHostsPath, host, remote, publicKey); saveErr != nil {
				errs <- saveErr
			}
		}()
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatalf("unexpected error: %v", err)
	}

	contents, err := os.ReadFile(knownHostsPath)
	if err != nil {
		t.Fatalf("failed to read known_hosts: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(contents)), "\n")
	var entries int
	for _, line := range lines {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		entries++
	}

	if entries != 1 {
		t.Fatalf("expected 1 known_hosts entry, got %d", entries)
	}
}
