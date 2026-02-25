package cmd

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"

	"github.com/spf13/cobra"
)

func newClusterSyncGeoIPCmd() *cobra.Command {
	var (
		manifestPath string
		licenseKey   string
		source       string
		filePath     string
		remotePath   string
		services     []string
		restart      bool
	)

	cmd := &cobra.Command{
		Use:   "sync-geoip",
		Short: "Provision GeoIP MMDB files to cluster services",
		Long: `Download and distribute MaxMind GeoLite2-City MMDB files to services
that need geolocation data (Foghorn and Quartermaster).

Sources:
  maxmind  - Download from MaxMind (requires --license-key or MAXMIND_LICENSE_KEY)
  file     - Use a local .mmdb file (requires --file)`,
		Example: `  # Download from MaxMind and distribute
  frameworks cluster sync-geoip --license-key YOUR_KEY

  # Use a local file
  frameworks cluster sync-geoip --source file --file /path/to/GeoLite2-City.mmdb

  # Target specific services and restart after upload
  frameworks cluster sync-geoip --license-key KEY --services foghorn,quartermaster --restart`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if licenseKey == "" {
				licenseKey = os.Getenv("MAXMIND_LICENSE_KEY")
			}
			return runSyncGeoIP(cmd, manifestPath, licenseKey, source, filePath, remotePath, services, restart)
		},
	}

	cmd.Flags().StringVar(&manifestPath, "manifest", "cluster.yaml", "Path to cluster manifest file")
	cmd.Flags().StringVar(&licenseKey, "license-key", "", "MaxMind license key (or MAXMIND_LICENSE_KEY env)")
	cmd.Flags().StringVar(&source, "source", "maxmind", "Source: maxmind or file")
	cmd.Flags().StringVar(&filePath, "file", "", "Local .mmdb file path (when source=file)")
	cmd.Flags().StringVar(&remotePath, "remote-path", "/var/lib/GeoIP/GeoLite2-City.mmdb", "Target path on hosts")
	cmd.Flags().StringSliceVar(&services, "services", []string{"foghorn", "quartermaster"}, "Services to target")
	cmd.Flags().BoolVar(&restart, "restart", false, "Restart target services after upload")

	return cmd
}

func runSyncGeoIP(_ *cobra.Command, manifestPath, licenseKey, source, filePath, remotePath string, services []string, restart bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	manifest, err := inventory.Load(manifestPath)
	if err != nil {
		return fmt.Errorf("failed to load manifest: %w", err)
	}

	var mmdbPath string
	switch source {
	case "file":
		if filePath == "" {
			return fmt.Errorf("--file is required when source=file")
		}
		if _, err := os.Stat(filePath); err != nil {
			return fmt.Errorf("file not found: %w", err)
		}
		mmdbPath = filePath

	case "maxmind":
		if licenseKey == "" {
			return fmt.Errorf("--license-key or MAXMIND_LICENSE_KEY env required for maxmind source")
		}
		tmpPath, err := downloadMaxMindDB(ctx, licenseKey)
		if err != nil {
			return fmt.Errorf("failed to download MaxMind DB: %w", err)
		}
		defer func() { _ = os.Remove(tmpPath) }()
		mmdbPath = tmpPath
		fmt.Println("Downloaded GeoLite2-City.mmdb")

	default:
		return fmt.Errorf("unknown source %q (use maxmind or file)", source)
	}

	// Build set of target service slugs for matching against host roles
	targetSet := make(map[string]bool, len(services))
	for _, s := range services {
		targetSet[strings.ToLower(s)] = true
	}

	pool := ssh.NewPool(0)
	uploaded := 0

	for hostName, host := range manifest.Hosts {
		match := false
		for _, role := range host.Roles {
			if targetSet[strings.ToLower(role)] {
				match = true
				break
			}
		}
		if !match {
			continue
		}

		connCfg := &ssh.ConnectionConfig{
			Address: host.Address,
			User:    host.User,
			KeyPath: host.SSHKey,
		}

		fmt.Printf("Uploading to %s (%s)...\n", hostName, host.Address)
		if err := pool.Upload(ctx, connCfg, ssh.UploadOptions{
			LocalPath:  mmdbPath,
			RemotePath: remotePath,
			Mode:       0644,
		}); err != nil {
			return fmt.Errorf("failed to upload to %s: %w", hostName, err)
		}
		uploaded++

		if restart {
			restartTargets := make([]string, 0, len(services))
			for _, svc := range services {
				restartTargets = append(restartTargets, fmt.Sprintf("frameworks-%s", svc))
			}
			restartCmd := fmt.Sprintf("docker restart %s", strings.Join(restartTargets, " "))
			fmt.Printf("Restarting services on %s...\n", hostName)
			if _, err := pool.Run(ctx, connCfg, restartCmd); err != nil {
				fmt.Printf("Warning: restart failed on %s: %v\n", hostName, err)
			}
		}
	}

	if uploaded == 0 {
		fmt.Println("No hosts matched the target services. Check your manifest roles.")
		return nil
	}

	fmt.Printf("Uploaded MMDB to %d host(s)\n", uploaded)
	return nil
}

func downloadMaxMindDB(ctx context.Context, licenseKey string) (string, error) {
	url := fmt.Sprintf(
		"https://download.maxmind.com/app/geoip_download?edition_id=GeoLite2-City&license_key=%s&suffix=tar.gz",
		licenseKey,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("MaxMind returned HTTP %d", resp.StatusCode)
	}

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to decompress: %w", err)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("tar read error: %w", err)
		}
		if strings.HasSuffix(hdr.Name, ".mmdb") {
			tmpFile, err := os.CreateTemp("", "geolite2-*.mmdb")
			if err != nil {
				return "", err
			}
			if _, err := io.Copy(tmpFile, tr); err != nil {
				_ = tmpFile.Close()
				_ = os.Remove(tmpFile.Name())
				return "", err
			}
			_ = tmpFile.Close()
			return tmpFile.Name(), nil
		}
	}

	return "", fmt.Errorf("no .mmdb file found in MaxMind archive")
}
