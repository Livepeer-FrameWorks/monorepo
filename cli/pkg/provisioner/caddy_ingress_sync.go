package provisioner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

func (c *CaddyProvisioner) installIngressSync(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	serviceToken, ok := config.Metadata["service_token"].(string)
	if !ok || serviceToken == "" {
		return fmt.Errorf("caddy ingress sync requires service_token metadata")
	}

	nodeID, ok := config.Metadata["node_id"].(string)
	if !ok || nodeID == "" {
		return fmt.Errorf("caddy ingress sync requires node_id metadata")
	}

	quartermasterCAFile, _ := config.Metadata["quartermaster_http_ca_file"].(string) //nolint:errcheck // zero value acceptable
	navigatorCAFile, _ := config.Metadata["navigator_http_ca_file"].(string)         //nolint:errcheck // zero value acceptable

	quartermasterURL, ok := config.Metadata["quartermaster_http_url"].(string)
	if !ok || quartermasterURL == "" {
		if quartermasterCAFile != "" {
			quartermasterURL = "https://quartermaster.internal:18002"
		} else {
			quartermasterURL = "http://quartermaster:18002"
		}
	}
	navigatorURL, ok := config.Metadata["navigator_http_url"].(string)
	if !ok || navigatorURL == "" {
		if navigatorCAFile != "" {
			navigatorURL = "https://navigator.internal:18010"
		} else {
			navigatorURL = "http://navigator:18010"
		}
	}

	envContent := fmt.Sprintf(`SERVICE_TOKEN=%s
NODE_ID=%s
QUARTERMASTER_URL=%s
NAVIGATOR_HTTP_URL=%s
QUARTERMASTER_CA_FILE=%s
NAVIGATOR_CA_FILE=%s
CADDY_SNIPPET_PATH=/etc/caddy/conf.d/frameworks.caddyfile
TLS_DIR=/etc/frameworks/tls
`, serviceToken, nodeID, quartermasterURL, navigatorURL, quartermasterCAFile, navigatorCAFile)

	tmpEnv := filepath.Join(os.TempDir(), "frameworks-caddy-sync.env")
	if err := os.WriteFile(tmpEnv, []byte(envContent), 0600); err != nil {
		return err
	}
	defer os.Remove(tmpEnv)

	if _, err := c.RunCommand(ctx, host, "mkdir -p /opt/frameworks/ingress-sync /etc/frameworks /etc/frameworks/tls /etc/caddy/conf.d"); err != nil {
		return fmt.Errorf("prepare caddy ingress sync directories: %w", err)
	}

	if err := c.UploadFile(ctx, host, ssh.UploadOptions{
		LocalPath:  tmpEnv,
		RemotePath: "/etc/frameworks/caddy-sync.env",
		Mode:       0600,
	}); err != nil {
		return fmt.Errorf("upload caddy ingress sync env: %w", err)
	}

	scriptContent := `#!/usr/bin/env python3
import json
import os
import pathlib
import re
import ssl
import stat
import subprocess
import urllib.parse
import urllib.request

TOKEN = os.environ["SERVICE_TOKEN"]
NODE_ID = os.environ["NODE_ID"]
QM_URL = os.environ["QUARTERMASTER_URL"].rstrip("/")
NAV_URL = os.environ["NAVIGATOR_HTTP_URL"].rstrip("/")
QM_CA_FILE = os.environ.get("QUARTERMASTER_CA_FILE", "").strip()
NAV_CA_FILE = os.environ.get("NAVIGATOR_CA_FILE", "").strip()
CADDYFILE = pathlib.Path(os.environ["CADDY_SNIPPET_PATH"])
TLS_DIR = pathlib.Path(os.environ["TLS_DIR"])

def urlopen_with_optional_ca(req: urllib.request.Request, ca_file: str):
    context = None
    if req.full_url.startswith("https://"):
        context = ssl.create_default_context(cafile=ca_file or None)
    return urllib.request.urlopen(req, timeout=30, context=context)

def fetch_json(url: str, ca_file: str = ""):
    req = urllib.request.Request(url, headers={"Authorization": f"Bearer {TOKEN}"})
    with urlopen_with_optional_ca(req, ca_file) as resp:
        return json.load(resp)

def write_file(path: pathlib.Path, content: str, mode: int) -> bool:
    path.parent.mkdir(parents=True, exist_ok=True)
    current = path.read_text() if path.exists() else None
    if current == content:
        return False
    tmp = path.with_suffix(path.suffix + ".tmp")
    tmp.write_text(content)
    os.chmod(tmp, mode)
    os.replace(tmp, path)
    return True

def snapshot(path: pathlib.Path):
    if path.exists():
        return {
            "path": str(path),
            "type": "file",
            "content": path.read_text(),
            "mode": stat.S_IMODE(path.stat().st_mode),
        }
    return {"path": str(path), "type": "missing"}

def remember(backups: dict[str, dict], path: pathlib.Path):
    key = str(path)
    if key not in backups:
        backups[key] = snapshot(path)

def restore(backups: dict[str, dict]):
    for state in reversed(list(backups.values())):
        path = pathlib.Path(state["path"])
        if path.exists():
            path.unlink()
        path.parent.mkdir(parents=True, exist_ok=True)
        if state["type"] == "file":
            tmp = path.with_suffix(path.suffix + ".restore")
            tmp.write_text(state["content"])
            os.chmod(tmp, state["mode"])
            os.replace(tmp, path)

def render_upstream(site: dict) -> str:
    kind = site["kind"]
    upstream = site["upstream"]
    if kind == "reverse_proxy_unix":
        return f"unix//{upstream}"
    if kind == "reverse_proxy_tcp":
        return upstream
    raise RuntimeError(f"unsupported ingress kind: {kind}")

def render_site(site: dict, bundle: dict) -> str:
    domains = ", ".join(site["domains"])
    bundle_dir = TLS_DIR / bundle["bundle_id"]
    upstream = render_upstream(site)
    return "\n".join([
        f"{domains} {{",
        f"    tls {bundle_dir / 'fullchain.pem'} {bundle_dir / 'privkey.pem'}",
        f"    reverse_proxy {upstream}",
        "}",
        "",
    ])

def main():
    sites_resp = fetch_json(f"{QM_URL}/internal/ingress-sites?node_id={urllib.parse.quote(NODE_ID)}", QM_CA_FILE)
    sites = sites_resp.get("sites", [])
    bundle_cache = {}
    changed = False
    backups = {}

    rendered = [
        ":18090 {",
        "    respond /health 200",
        "}",
        "",
    ]

    for site in sites:
        bundle_id = site["tls_bundle_id"]
        if bundle_id not in bundle_cache:
            bundle_cache[bundle_id] = fetch_json(f"{NAV_URL}/internal/tls-bundles/{urllib.parse.quote(bundle_id)}", NAV_CA_FILE)
        bundle = bundle_cache[bundle_id]
        if not bundle.get("bundle_id"):
            raise RuntimeError(f"missing tls bundle {bundle_id}")

        bundle_dir = TLS_DIR / bundle["bundle_id"]
        bundle_dir.mkdir(parents=True, exist_ok=True)
        remember(backups, bundle_dir / "fullchain.pem")
        remember(backups, bundle_dir / "privkey.pem")
        changed |= write_file(bundle_dir / "fullchain.pem", bundle["cert_pem"], 0o644)
        changed |= write_file(bundle_dir / "privkey.pem", bundle["key_pem"], 0o600)
        rendered.append(render_site(site, bundle))

    try:
        remember(backups, CADDYFILE)
        changed |= write_file(CADDYFILE, "\n".join(rendered), 0o644)
        if changed:
            subprocess.run(["caddy", "validate", "--config", "/etc/caddy/Caddyfile"], check=True)
            subprocess.run(["systemctl", "reload", "caddy"], check=True)
    except Exception:
        restore(backups)
        raise

if __name__ == "__main__":
    main()
`

	tmpScript := filepath.Join(os.TempDir(), "caddy-sync.py")
	if err := os.WriteFile(tmpScript, []byte(scriptContent), 0755); err != nil {
		return err
	}
	defer os.Remove(tmpScript)

	if err := c.UploadFile(ctx, host, ssh.UploadOptions{
		LocalPath:  tmpScript,
		RemotePath: "/opt/frameworks/ingress-sync/caddy-sync.py",
		Mode:       0755,
	}); err != nil {
		return fmt.Errorf("upload caddy ingress sync script: %w", err)
	}

	unitContent, err := GenerateSystemdUnit(SystemdUnitData{
		ServiceName: "frameworks-caddy-sync",
		Description: "FrameWorks Caddy ingress site and TLS sync",
		WorkingDir:  "/opt/frameworks/ingress-sync",
		ExecStart:   "/opt/frameworks/ingress-sync/caddy-sync.py",
		User:        "root",
		EnvFile:     "/etc/frameworks/caddy-sync.env",
		Restart:     "no",
		After:       []string{"network-online", "caddy"},
	})
	if err != nil {
		return err
	}

	timerContent := `[Unit]
Description=Run FrameWorks Caddy ingress sync periodically

[Timer]
OnBootSec=2m
OnUnitActiveSec=5m
Unit=frameworks-caddy-sync.service

[Install]
WantedBy=timers.target
`

	tmpUnit := filepath.Join(os.TempDir(), "frameworks-caddy-sync.service")
	err = os.WriteFile(tmpUnit, []byte(unitContent), 0644)
	if err != nil {
		return err
	}
	defer os.Remove(tmpUnit)

	tmpTimer := filepath.Join(os.TempDir(), "frameworks-caddy-sync.timer")
	err = os.WriteFile(tmpTimer, []byte(timerContent), 0644)
	if err != nil {
		return err
	}
	defer os.Remove(tmpTimer)

	err = c.UploadFile(ctx, host, ssh.UploadOptions{
		LocalPath:  tmpUnit,
		RemotePath: "/etc/systemd/system/frameworks-caddy-sync.service",
		Mode:       0644,
	})
	if err != nil {
		return fmt.Errorf("upload caddy ingress sync unit: %w", err)
	}
	err = c.UploadFile(ctx, host, ssh.UploadOptions{
		LocalPath:  tmpTimer,
		RemotePath: "/etc/systemd/system/frameworks-caddy-sync.timer",
		Mode:       0644,
	})
	if err != nil {
		return fmt.Errorf("upload caddy ingress sync timer: %w", err)
	}

	result, err := c.RunCommand(ctx, host, "systemctl daemon-reload && systemctl enable --now frameworks-caddy-sync.timer")
	if err != nil || result.ExitCode != 0 {
		return fmt.Errorf("enable caddy ingress sync timer: %w (stderr: %s)", err, result.Stderr)
	}

	return nil
}
