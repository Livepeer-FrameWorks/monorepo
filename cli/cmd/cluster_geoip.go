package cmd

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"

	"github.com/spf13/cobra"
)

const (
	geoIPCacheSubdir = "frameworks/geoip"
	geoIPCacheFile   = "GeoLite2-City.mmdb"
	geoIPCacheTTL    = 24 * time.Hour
)

func newClusterSyncGeoIPCmd() *cobra.Command {
	var (
		licenseKey string
		source     string
		filePath   string
		remotePath string
		services   []string
		restart    bool
	)

	cmd := &cobra.Command{
		Use:   "sync-geoip",
		Short: "Provision GeoIP MMDB files to cluster services",
		Long: `Download and distribute MaxMind GeoLite2-City MMDB files to services
that need geolocation data (Foghorn and Quartermaster).

Sources:
  maxmind  - Download from MaxMind. Requires MAXMIND_LICENSE_KEY in the
             manifest's env_files, or --license-key for ad-hoc overrides.
  file     - Use a local .mmdb file (requires --file)`,
		Example: `  frameworks cluster sync-geoip
  frameworks cluster sync-geoip --source file --file /path/to/GeoLite2-City.mmdb
  frameworks cluster sync-geoip --license-key KEY --services foghorn,quartermaster --restart`,
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return err
			}
			defer rc.Cleanup()
			return runSyncGeoIP(cmd, rc, licenseKey, source, filePath, remotePath, services, restart)
		},
	}

	cmd.Flags().StringVar(&licenseKey, "license-key", "", "MaxMind license key (ad-hoc override; normally sourced from manifest env_files)")
	cmd.Flags().StringVar(&source, "source", "maxmind", "Source: maxmind or file")
	cmd.Flags().StringVar(&filePath, "file", "", "Local .mmdb file path (when source=file)")
	cmd.Flags().StringVar(&remotePath, "remote-path", "/var/lib/GeoIP/GeoLite2-City.mmdb", "Target path on hosts")
	cmd.Flags().StringSliceVar(&services, "services", []string{"foghorn", "quartermaster"}, "Services to target")
	cmd.Flags().BoolVar(&restart, "restart", false, "Restart target services after upload")

	return cmd
}

func runSyncGeoIP(cmd *cobra.Command, rc *resolvedCluster, licenseKey, source, filePath, remotePath string, services []string, restart bool) error {
	ux.Heading(cmd.OutOrStdout(), fmt.Sprintf("Syncing GeoIP MMDB to %d service(s) via %s", len(services), source))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	manifest := rc.Manifest
	manifestDir := filepath.Dir(rc.ManifestPath)
	source = effectiveGeoIPSource(manifest, source)
	services = effectiveGeoIPServices(manifest, services)
	remotePath = effectiveGeoIPRemotePath(manifest, remotePath)
	filePath = effectiveGeoIPFilePath(manifest, filePath, manifestDir)

	// Only decrypt manifest env_files when we actually need the MaxMind key:
	// source=maxmind AND no explicit --license-key. This preserves --source=file
	// and --license-key override flows when SOPS state is unavailable.
	if licenseKey == "" && source == "maxmind" {
		sharedEnv, err := rc.SharedEnv()
		if err != nil {
			return fmt.Errorf("load manifest env_files: %w", err)
		}
		licenseKey = effectiveGeoIPLicenseKey(sharedEnv, licenseKey)
	}

	mmdbPath, cleanup, err := resolveGeoIPMMDBPath(ctx, source, filePath, licenseKey)
	if err != nil {
		return err
	}
	defer cleanup()

	sshKey := stringFlag(cmd, "ssh-key").Value
	pool := ssh.NewPool(0, sshKey)
	defer pool.Close()

	uploaded, err := uploadGeoIPToHosts(ctx, manifest, pool, mmdbPath, remotePath, services, restart, os.Stdout)
	if err != nil {
		return err
	}
	if uploaded == 0 {
		fmt.Println("No hosts matched the target GeoIP services in the manifest.")
	}
	return nil
}

// effectiveGeoIPLicenseKey resolves the MaxMind license key in priority order:
// explicit flag (sync-geoip --license-key) → sharedEnv["MAXMIND_LICENSE_KEY"]
// from manifest env_files → "". Never reads process env: platform secrets
// for cluster operations live in gitops, not the operator's shell.
func effectiveGeoIPLicenseKey(sharedEnv map[string]string, explicit string) string {
	if explicit != "" {
		return explicit
	}
	if sharedEnv != nil {
		return sharedEnv["MAXMIND_LICENSE_KEY"]
	}
	return ""
}

func effectiveGeoIPSource(manifest *inventory.Manifest, explicit string) string {
	if explicit != "" {
		return explicit
	}
	if manifest != nil && manifest.GeoIP != nil && manifest.GeoIP.Source != "" {
		return manifest.GeoIP.Source
	}
	return "maxmind"
}

func effectiveGeoIPFilePath(manifest *inventory.Manifest, explicit, manifestDir string) string {
	path := explicit
	if path == "" && manifest != nil && manifest.GeoIP != nil {
		path = manifest.GeoIP.File
	}
	if path == "" || manifestDir == "" || filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(manifestDir, path)
}

func effectiveGeoIPRemotePath(manifest *inventory.Manifest, explicit string) string {
	if explicit != "" {
		return explicit
	}
	if manifest != nil && manifest.GeoIP != nil && manifest.GeoIP.RemotePath != "" {
		return manifest.GeoIP.RemotePath
	}
	return "/var/lib/GeoIP/GeoLite2-City.mmdb"
}

func effectiveGeoIPServices(manifest *inventory.Manifest, services []string) []string {
	if len(services) > 0 {
		return services
	}
	if manifest != nil && manifest.GeoIP != nil && len(manifest.GeoIP.Services) > 0 {
		return append([]string{}, manifest.GeoIP.Services...)
	}
	return []string{"foghorn", "quartermaster"}
}

func resolveGeoIPMMDBPath(ctx context.Context, source, filePath, licenseKey string) (string, func(), error) {
	switch source {
	case "file":
		if filePath == "" {
			return "", func() {}, fmt.Errorf("geoip source=file requires a local MMDB file")
		}
		if _, err := os.Stat(filePath); err != nil {
			return "", func() {}, fmt.Errorf("geoip file not found: %w", err)
		}
		return filePath, func() {}, nil
	case "maxmind":
		if licenseKey == "" {
			return "", func() {}, fmt.Errorf("geoip source=maxmind requires MAXMIND_LICENSE_KEY in manifest env_files (or --license-key on sync-geoip) — add it to your gitops secrets")
		}
		cachePath, err := geoIPCachePath()
		if err != nil {
			return "", func() {}, fmt.Errorf("resolve geoip cache path: %w", err)
		}
		if fresh, err := geoIPCacheFresh(cachePath, time.Now()); err != nil {
			return "", func() {}, fmt.Errorf("check geoip cache: %w", err)
		} else if fresh {
			fmt.Println("Using cached GeoLite2-City.mmdb")
			return cachePath, func() {}, nil
		}
		if err := downloadMaxMindDB(ctx, licenseKey, cachePath); err != nil {
			return "", func() {}, fmt.Errorf("failed to download MaxMind DB: %w", err)
		}
		fmt.Println("Downloaded GeoLite2-City.mmdb")
		return cachePath, func() {}, nil
	default:
		return "", func() {}, fmt.Errorf("unknown geoip source %q (use maxmind or file)", source)
	}
}

func geoIPCachePath() (string, error) {
	root, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, geoIPCacheSubdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, geoIPCacheFile), nil
}

func geoIPCacheFresh(path string, now time.Time) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if info.Size() == 0 {
		return false, nil
	}
	return now.Sub(info.ModTime()) < geoIPCacheTTL, nil
}

func downloadMaxMindDB(ctx context.Context, licenseKey, destPath string) error {
	url := fmt.Sprintf(
		"https://download.maxmind.com/app/geoip_download?edition_id=GeoLite2-City&license_key=%s&suffix=tar.gz",
		licenseKey,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("MaxMind returned HTTP %d", resp.StatusCode)
	}

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to decompress: %w", err)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	tmpFile, err := os.CreateTemp(filepath.Dir(destPath), "geolite2-*.mmdb")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()
	defer func() {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
	}()

	for {
		hdr, err := tr.Next()
		if err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("tar read error: %w", err)
		}
		if !strings.HasSuffix(hdr.Name, ".mmdb") {
			continue
		}
		if _, err := io.Copy(tmpFile, tr); err != nil {
			return err
		}
		if err := tmpFile.Close(); err != nil {
			return err
		}
		if err := os.Rename(tmpPath, destPath); err != nil {
			return err
		}
		return nil
	}

	return fmt.Errorf("no .mmdb file found in MaxMind archive")
}

func uploadGeoIPToHosts(ctx context.Context, manifest *inventory.Manifest, pool *ssh.Pool, mmdbPath, remotePath string, services []string, restart bool, out io.Writer) (int, error) {
	targetHosts, err := geoIPTargetHosts(manifest, services)
	if err != nil {
		return 0, err
	}
	if len(targetHosts) == 0 {
		return 0, nil
	}

	uploaded := 0
	for _, hostName := range targetHosts {
		host := manifest.Hosts[hostName]
		connCfg := &ssh.ConnectionConfig{
			Address:  host.ExternalIP,
			User:     host.User,
			HostName: host.Name,
		}

		fmt.Fprintf(out, "Uploading GeoIP MMDB to %s (%s)...\n", hostName, host.ExternalIP)
		if _, err := pool.Run(ctx, connCfg, fmt.Sprintf("mkdir -p %s", ssh.ShellQuote(filepath.Dir(remotePath)))); err != nil {
			return uploaded, fmt.Errorf("prepare geoip directory on %s: %w", hostName, err)
		}
		if err := pool.Upload(ctx, connCfg, ssh.UploadOptions{
			LocalPath:  mmdbPath,
			RemotePath: remotePath,
			Mode:       0644,
		}); err != nil {
			return uploaded, fmt.Errorf("upload GeoIP MMDB to %s:%s: %w", hostName, remotePath, err)
		}
		uploaded++

		if !restart {
			continue
		}

		var restartTargets []string
		for _, serviceName := range services {
			svc, ok := manifest.Services[serviceName]
			if !ok || !svc.Enabled {
				continue
			}
			if svc.Host == hostName || slicesContain(svc.Hosts, hostName) {
				restartTargets = append(restartTargets, serviceName)
			}
		}
		if len(restartTargets) == 0 {
			continue
		}

		for _, serviceName := range restartTargets {
			fmt.Fprintf(out, "Restarting %s on %s...\n", serviceName, hostName)
			restartCmd := fmt.Sprintf("docker restart frameworks-%s || systemctl restart frameworks-%s", serviceName, serviceName)
			if _, err := pool.Run(ctx, connCfg, restartCmd); err != nil {
				fmt.Fprintf(out, "Warning: failed to restart %s on %s: %v\n", serviceName, hostName, err)
			}
		}
	}

	ux.Success(out, fmt.Sprintf("Uploaded GeoIP MMDB to %d host(s)", uploaded))
	return uploaded, nil
}

func slicesContain(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func geoIPTargetHosts(manifest *inventory.Manifest, services []string) ([]string, error) {
	if manifest == nil {
		return nil, fmt.Errorf("manifest is required")
	}
	hostSet := make(map[string]struct{})
	for _, serviceName := range services {
		name := strings.TrimSpace(serviceName)
		if name == "" {
			continue
		}
		svc, ok := manifest.Services[name]
		if !ok {
			return nil, fmt.Errorf("geoip target service %q not found in manifest services", name)
		}
		if !svc.Enabled {
			continue
		}
		if svc.Host != "" {
			hostSet[svc.Host] = struct{}{}
		}
		for _, hostName := range svc.Hosts {
			hostSet[hostName] = struct{}{}
		}
	}

	hosts := make([]string, 0, len(hostSet))
	for hostName := range hostSet {
		if _, ok := manifest.Hosts[hostName]; !ok {
			return nil, fmt.Errorf("geoip target host %q not found in manifest", hostName)
		}
		hosts = append(hosts, hostName)
	}
	sort.Strings(hosts)
	return hosts, nil
}
