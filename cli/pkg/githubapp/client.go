package githubapp

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	jwt "github.com/golang-jwt/jwt/v5"
)

// Config holds GitHub App authentication credentials.
type Config struct {
	AppID          int64
	InstallationID int64
	PrivateKeyPath string
	Repo           string // "owner/repo"
	Ref            string // branch or tag, default "main"
}

// LoadPrivateKey reads and parses a PEM-encoded RSA private key.
func LoadPrivateKey(path string) (*rsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading private key: %w", err)
	}
	return ParsePrivateKey(data)
}

// ParsePrivateKey parses PEM-encoded RSA private key bytes.
func ParsePrivateKey(data []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found in private key")
	}

	// Try PKCS1 first, then PKCS8
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse private key (tried PKCS1 and PKCS8): %w", err)
	}
	rsaKey, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("private key is not RSA")
	}
	return rsaKey, nil
}

// MintJWT creates a JWT for GitHub App authentication.
// The JWT is valid for 10 minutes per the GitHub API spec.
func MintJWT(appID int64, privateKey *rsa.PrivateKey) (string, error) {
	now := time.Now()
	claims := jwt.MapClaims{
		"iat": jwt.NewNumericDate(now.Add(-60 * time.Second)), // clock skew buffer
		"exp": jwt.NewNumericDate(now.Add(10 * time.Minute)),
		"iss": appID,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	return token.SignedString(privateKey)
}

type installationTokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// GetInstallationToken exchanges a GitHub App JWT for an installation access token.
func GetInstallationToken(ctx context.Context, jwtToken string, installationID int64) (string, error) {
	url := fmt.Sprintf("https://api.github.com/app/installations/%d/access_tokens", installationID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+jwtToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "frameworks-cli")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("GitHub API returned %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp installationTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}
	return tokenResp.Token, nil
}

type contentsResponse struct {
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
}

// FetchFile retrieves a single file from a GitHub repo using an installation token.
func FetchFile(ctx context.Context, token, owner, repo, path, ref string) ([]byte, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/contents/%s", owner, repo, path)
	if ref != "" {
		url += "?ref=" + ref
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "frameworks-cli")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("file not found: %s/%s/%s", owner, repo, path)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API returned %d: %s", resp.StatusCode, string(body))
	}

	var contents contentsResponse
	if err := json.NewDecoder(resp.Body).Decode(&contents); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if contents.Encoding != "base64" {
		return nil, fmt.Errorf("unexpected encoding: %s", contents.Encoding)
	}

	// GitHub returns base64 with newlines
	cleaned := strings.ReplaceAll(contents.Content, "\n", "")
	data, err := base64.StdEncoding.DecodeString(cleaned)
	if err != nil {
		return nil, fmt.Errorf("failed to decode content: %w", err)
	}
	return data, nil
}

// NewClient creates a configured GitHub App client that can fetch files from a repo.
type Client struct {
	token string
	owner string
	repo  string
	ref   string
}

// NewClient authenticates with GitHub using App credentials and returns a client.
func NewClient(ctx context.Context, cfg Config) (*Client, error) {
	key, err := LoadPrivateKey(cfg.PrivateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("loading GitHub App private key: %w", err)
	}

	jwtToken, err := MintJWT(cfg.AppID, key)
	if err != nil {
		return nil, fmt.Errorf("minting JWT: %w", err)
	}

	installToken, err := GetInstallationToken(ctx, jwtToken, cfg.InstallationID)
	if err != nil {
		return nil, fmt.Errorf("getting installation token: %w", err)
	}

	parts := strings.SplitN(cfg.Repo, "/", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("repo must be in 'owner/repo' format, got: %s", cfg.Repo)
	}

	ref := cfg.Ref
	if ref == "" {
		ref = "main"
	}

	return &Client{
		token: installToken,
		owner: parts[0],
		repo:  parts[1],
		ref:   ref,
	}, nil
}

// Fetch retrieves a file from the configured repository.
func (c *Client) Fetch(ctx context.Context, path string) ([]byte, error) {
	return FetchFile(ctx, c.token, c.owner, c.repo, path, c.ref)
}
