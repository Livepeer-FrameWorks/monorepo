package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"frameworks/cli/pkg/inventory"

	"github.com/spf13/cobra"
)

func newClusterStorageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "storage",
		Short: "Inspect cluster artifact-storage backends",
		Long: `Inspect the immutable S3 storage descriptor a media cluster is bound to.

Each media cell is bound to exactly one S3 backend (bucket/endpoint/region/prefix).
Quartermaster persists that descriptor on the cluster row, Chandler serves from it,
and Foghorn establishes an immutable cell identity against it on first boot.`,
	}
	cmd.AddCommand(newClusterStorageDescriptorCmd())
	return cmd
}

// newClusterStorageDescriptorCmd prints a cluster's S3 descriptor as JSON. It reads only non-secret S3 fields, so
// it does a STRUCTURAL read — strict-parse the cluster.yaml passed via --manifest and validate just that descriptor —
// and deliberately does NOT go through resolveClusterManifest / LoadWithHosts, which would decrypt the SOPS host
// inventory. This keeps routine descriptor inspection independent of host-secret resolution.
func newClusterStorageDescriptorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "descriptor <cluster>",
		Short: "Print a cluster's S3 descriptor as JSON (structural manifest read; no host inventory)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := strings.TrimSpace(stringFlag(cmd, "manifest").Value)
			if path == "" {
				return fmt.Errorf("cluster storage descriptor requires --manifest <cluster.yaml>")
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("read manifest %s: %w", path, err)
			}
			m, err := inventory.ParseManifest(data) // strict (KnownFields), no host inventory, no SOPS
			if err != nil {
				return err
			}
			return emitClusterDescriptor(cmd.OutOrStdout(), m, args[0])
		},
	}
	return cmd
}

// emitClusterDescriptor writes {"bucket","prefix","endpoint"} for a cluster, or an error when the cluster is absent,
// declares no S3 backend, has an unset prefix, or any field contains a control character (a simple S3 identifier never
// does, so a weird value fails closed rather than being addressed). Split from the
// command so validation + serialization are unit-testable without a live manifest.
func emitClusterDescriptor(w io.Writer, m *inventory.Manifest, clusterID string) error {
	cc, ok := m.Clusters[clusterID]
	if !ok {
		return fmt.Errorf("cluster %q not found in manifest", clusterID)
	}
	if strings.TrimSpace(cc.S3Bucket) == "" {
		return fmt.Errorf("cluster %q declares no s3_bucket (no storage backend)", clusterID)
	}
	if strings.TrimSpace(cc.S3Endpoint) == "" {
		return fmt.Errorf("cluster %q declares no s3_endpoint (descriptor incomplete)", clusterID)
	}
	if cc.S3Prefix == nil {
		return fmt.Errorf("cluster %q has no s3_prefix (descriptor incomplete)", clusterID)
	}
	prefix := *cc.S3Prefix
	for _, f := range []struct{ name, val string }{{"s3_bucket", cc.S3Bucket}, {"s3_prefix", prefix}, {"s3_endpoint", cc.S3Endpoint}} {
		if hasControlChar(f.val) {
			return fmt.Errorf("cluster %q %s contains a control character; refusing", clusterID, f.name)
		}
	}
	b, err := json.Marshal(struct {
		Bucket   string `json:"bucket"`
		Prefix   string `json:"prefix"`
		Endpoint string `json:"endpoint"`
	}{Bucket: cc.S3Bucket, Prefix: prefix, Endpoint: cc.S3Endpoint})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(b))
	return err
}

// hasControlChar reports whether s contains any ASCII control character (below 0x20 or DEL).
func hasControlChar(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}
