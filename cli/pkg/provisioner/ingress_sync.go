package provisioner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

func (n *NginxProvisioner) installIngressSync(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	serviceToken, ok := config.Metadata["service_token"].(string)
	if !ok || serviceToken == "" {
		return fmt.Errorf("nginx ingress sync requires service_token metadata")
	}

	nodeID, ok := config.Metadata["node_id"].(string)
	if !ok || nodeID == "" {
		return fmt.Errorf("nginx ingress sync requires node_id metadata")
	}

	quartermasterURL, ok := config.Metadata["quartermaster_http_url"].(string)
	if !ok || quartermasterURL == "" {
		quartermasterURL = "http://quartermaster:18002"
	}
	navigatorURL, ok := config.Metadata["navigator_http_url"].(string)
	if !ok || navigatorURL == "" {
		navigatorURL = "http://navigator:18010"
	}
	quartermasterCAFile, _ := config.Metadata["quartermaster_http_ca_file"].(string) //nolint:errcheck // zero value acceptable
	navigatorCAFile, _ := config.Metadata["navigator_http_ca_file"].(string)         //nolint:errcheck // zero value acceptable

	envContent := fmt.Sprintf(`SERVICE_TOKEN=%s
NODE_ID=%s
QUARTERMASTER_URL=%s
NAVIGATOR_HTTP_URL=%s
QUARTERMASTER_CA_FILE=%s
NAVIGATOR_CA_FILE=%s
SITES_AVAILABLE_DIR=/etc/nginx/sites-available
SITES_ENABLED_DIR=/etc/nginx/sites-enabled
HTTP_SNIPPETS_DIR=/etc/nginx/frameworks-http.d
TLS_DIR=/etc/frameworks/tls
`, serviceToken, nodeID, quartermasterURL, navigatorURL, quartermasterCAFile, navigatorCAFile)

	tmpEnv := filepath.Join(os.TempDir(), "frameworks-ingress-sync.env")
	if err := os.WriteFile(tmpEnv, []byte(envContent), 0600); err != nil {
		return err
	}
	defer os.Remove(tmpEnv)

	if _, err := n.RunCommand(ctx, host, "mkdir -p /opt/frameworks/ingress-sync /etc/frameworks /etc/frameworks/tls /etc/nginx/frameworks-http.d"); err != nil {
		return fmt.Errorf("prepare ingress sync directories: %w", err)
	}

	if err := n.UploadFile(ctx, host, ssh.UploadOptions{
		LocalPath:  tmpEnv,
		RemotePath: "/etc/frameworks/ingress-sync.env",
		Mode:       0600,
	}); err != nil {
		return fmt.Errorf("upload ingress sync env: %w", err)
	}

	scriptContent := `#!/usr/bin/env python3
import json
import os
import pathlib
import re
import shutil
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
SITES_AVAILABLE = pathlib.Path(os.environ["SITES_AVAILABLE_DIR"])
SITES_ENABLED = pathlib.Path(os.environ["SITES_ENABLED_DIR"])
HTTP_SNIPPETS = pathlib.Path(os.environ["HTTP_SNIPPETS_DIR"])
TLS_DIR = pathlib.Path(os.environ["TLS_DIR"])
GENERATED_PREFIX = "frameworks-managed-"

def urlopen_with_optional_ca(req: urllib.request.Request, ca_file: str):
    context = None
    if req.full_url.startswith("https://"):
        context = ssl.create_default_context(cafile=ca_file or None)
    return urllib.request.urlopen(req, timeout=30, context=context)

def fetch_json(url: str, ca_file: str = ""):
    req = urllib.request.Request(url, headers={"Authorization": f"Bearer {TOKEN}"})
    with urlopen_with_optional_ca(req, ca_file) as resp:
        return json.load(resp)

def safe_name(value: str) -> str:
    return re.sub(r"[^a-zA-Z0-9_.-]+", "-", value).strip("-")

def write_file(path: pathlib.Path, content: str, mode: int) -> bool:
    path.parent.mkdir(parents=True, exist_ok=True)
    current = None
    if path.exists():
        current = path.read_text()
    if current == content:
        return False
    tmp = path.with_suffix(path.suffix + ".tmp")
    tmp.write_text(content)
    os.chmod(tmp, mode)
    os.replace(tmp, path)
    return True

def snapshot(path: pathlib.Path):
    if path.is_symlink():
        return {"path": str(path), "type": "symlink", "target": os.readlink(path)}
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
        if path.exists() or path.is_symlink():
            if path.is_dir() and not path.is_symlink():
                shutil.rmtree(path)
            else:
                path.unlink()
        path.parent.mkdir(parents=True, exist_ok=True)
        if state["type"] == "file":
            tmp = path.with_suffix(path.suffix + ".restore")
            tmp.write_text(state["content"])
            os.chmod(tmp, state["mode"])
            os.replace(tmp, path)
        elif state["type"] == "symlink":
            path.symlink_to(state["target"])

def ensure_symlink(link: pathlib.Path, target: pathlib.Path) -> bool:
    desired = str(target)
    if link.is_symlink() and os.readlink(link) == desired:
        return False
    if link.exists() or link.is_symlink():
        link.unlink()
    link.symlink_to(target)
    return True

def remove_stale(directory: pathlib.Path, desired_names: set[str]) -> bool:
    changed = False
    for path in directory.glob(GENERATED_PREFIX + "*.conf"):
        if path.name not in desired_names:
            path.unlink()
            changed = True
    return changed

def proxy_pass(site: dict) -> str:
    kind = site["kind"]
    upstream = site["upstream"]
    if kind == "reverse_proxy_unix":
        return f"http://unix:{upstream}"
    if kind == "reverse_proxy_tcp":
        return f"http://{upstream}"
    raise RuntimeError(f"unsupported ingress kind: {kind}")

def render_site(site: dict, bundle: dict) -> str:
    metadata = site.get("metadata") or {}
    names = " ".join(site["domains"])
    bundle_dir = TLS_DIR / bundle["bundle_id"]
    target = proxy_pass(site)
    websocket_path = metadata.get("websocket_path", "")
    upgrade_all = bool(metadata.get("upgrade_all", False))
    geo_proxy_headers = bool(metadata.get("geo_proxy_headers", False))
    lines = [
        "server {",
        "    listen 80;",
        f"    server_name {names};",
        "    return 301 https://$host$request_uri;",
        "}",
        "",
        "server {",
        "    listen 443 ssl;",
        f"    server_name {names};",
        f"    ssl_certificate {bundle_dir / 'fullchain.pem'};",
        f"    ssl_certificate_key {bundle_dir / 'privkey.pem'};",
    ]
    if websocket_path:
        lines.extend([
            "",
            f"    location {websocket_path} {{",
            f"        proxy_pass {target}{websocket_path};",
            "        proxy_http_version 1.1;",
            "        proxy_set_header Upgrade $http_upgrade;",
            "        proxy_set_header Connection \"upgrade\";",
            "        proxy_set_header Host $host;",
            "        proxy_set_header X-Real-IP $remote_addr;",
            "        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;",
            "        proxy_set_header X-Forwarded-Proto $scheme;",
            "        proxy_read_timeout 86400;",
            "    }",
        ])
    lines.extend([
        "",
        "    location / {",
        f"        proxy_pass {target};",
    ])
    if upgrade_all:
        lines.extend([
            "        proxy_http_version 1.1;",
            "        proxy_set_header Upgrade $http_upgrade;",
            "        proxy_set_header Connection \"upgrade\";",
        ])
    lines.extend([
        "        proxy_set_header Host $host;",
        "        proxy_set_header X-Real-IP $remote_addr;",
        "        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;",
        "        proxy_set_header X-Forwarded-Proto $scheme;",
    ])
    if geo_proxy_headers:
        lines.extend([
            "        proxy_set_header X-Latitude $geo_lat;",
            "        proxy_set_header X-Longitude $geo_lon;",
        ])
    lines.extend([
        "    }",
        "}",
        "",
    ])
    return "\n".join(lines)

def main():
    sites_resp = fetch_json(f"{QM_URL}/internal/ingress-sites?node_id={urllib.parse.quote(NODE_ID)}", QM_CA_FILE)
    sites = sites_resp.get("sites", [])
    desired = set()
    changed = False
    bundle_cache = {}
    backups = {}
    geoip_db_path = ""

    try:
        for site in sites:
            metadata = site.get("metadata") or {}
            if metadata.get("geo_proxy_headers") and not geoip_db_path:
                geoip_db_path = metadata.get("geoip_db_path", "/var/lib/GeoIP/GeoLite2-City.mmdb")

            bundle_id = site["tls_bundle_id"]
            if bundle_id not in bundle_cache:
            bundle_cache[bundle_id] = fetch_json(f"{NAV_URL}/internal/tls-bundles/{urllib.parse.quote(bundle_id)}", NAV_CA_FILE)
            bundle = bundle_cache[bundle_id]
            if not bundle.get("bundle_id"):
                raise RuntimeError(f"missing tls bundle {bundle_id}")

            bundle_dir = TLS_DIR / bundle["bundle_id"]
            fullchain_path = bundle_dir / "fullchain.pem"
            privkey_path = bundle_dir / "privkey.pem"
            remember(backups, fullchain_path)
            remember(backups, privkey_path)
            changed |= write_file(fullchain_path, bundle["cert_pem"], 0o644)
            changed |= write_file(privkey_path, bundle["key_pem"], 0o600)

            filename = f"{GENERATED_PREFIX}{safe_name(site['site_id'])}.conf"
            desired.add(filename)
            available_path = SITES_AVAILABLE / filename
            enabled_path = SITES_ENABLED / filename
            remember(backups, available_path)
            remember(backups, enabled_path)
            changed |= write_file(available_path, render_site(site, bundle), 0o644)
            changed |= ensure_symlink(enabled_path, available_path)

        geo_snippet_path = HTTP_SNIPPETS / "frameworks-geoip.conf"
        remember(backups, geo_snippet_path)
        if geoip_db_path:
            snippet = "\n".join([
                f"geoip2 {geoip_db_path} {{",
                "    $geo_lat location latitude;",
                "    $geo_lon location longitude;",
                "}",
                "",
            ])
            changed |= write_file(geo_snippet_path, snippet, 0o644)
        elif geo_snippet_path.exists():
            geo_snippet_path.unlink()
            changed = True

        for path in SITES_AVAILABLE.glob(GENERATED_PREFIX + "*.conf"):
            if path.name not in desired:
                remember(backups, path)
        for path in SITES_ENABLED.glob(GENERATED_PREFIX + "*.conf"):
            if path.name not in desired:
                remember(backups, path)

        changed |= remove_stale(SITES_AVAILABLE, desired)
        changed |= remove_stale(SITES_ENABLED, desired)

        if changed:
            subprocess.run(["nginx", "-t"], check=True)
            subprocess.run(["systemctl", "reload", "nginx"], check=True)
    except Exception:
        restore(backups)
        raise

if __name__ == "__main__":
    main()
`

	tmpScript := filepath.Join(os.TempDir(), "nginx-sync.py")
	if err := os.WriteFile(tmpScript, []byte(scriptContent), 0755); err != nil {
		return err
	}
	defer os.Remove(tmpScript)

	if err := n.UploadFile(ctx, host, ssh.UploadOptions{
		LocalPath:  tmpScript,
		RemotePath: "/opt/frameworks/ingress-sync/nginx-sync.py",
		Mode:       0755,
	}); err != nil {
		return fmt.Errorf("upload ingress sync script: %w", err)
	}

	unitContent, err := GenerateSystemdUnit(SystemdUnitData{
		ServiceName: "frameworks-ingress-sync",
		Description: "FrameWorks ingress site and TLS sync",
		WorkingDir:  "/opt/frameworks/ingress-sync",
		ExecStart:   "/opt/frameworks/ingress-sync/nginx-sync.py",
		User:        "root",
		EnvFile:     "/etc/frameworks/ingress-sync.env",
		Restart:     "no",
		After:       []string{"network-online", "nginx"},
	})
	if err != nil {
		return err
	}

	timerContent := `[Unit]
Description=Run FrameWorks ingress sync periodically

[Timer]
OnBootSec=2m
OnUnitActiveSec=5m
Unit=frameworks-ingress-sync.service

[Install]
WantedBy=timers.target
`

	tmpUnit := filepath.Join(os.TempDir(), "frameworks-ingress-sync.service")
	err = os.WriteFile(tmpUnit, []byte(unitContent), 0644)
	if err != nil {
		return err
	}
	defer os.Remove(tmpUnit)

	tmpTimer := filepath.Join(os.TempDir(), "frameworks-ingress-sync.timer")
	err = os.WriteFile(tmpTimer, []byte(timerContent), 0644)
	if err != nil {
		return err
	}
	defer os.Remove(tmpTimer)

	err = n.UploadFile(ctx, host, ssh.UploadOptions{
		LocalPath:  tmpUnit,
		RemotePath: "/etc/systemd/system/frameworks-ingress-sync.service",
		Mode:       0644,
	})
	if err != nil {
		return fmt.Errorf("upload ingress sync unit: %w", err)
	}
	err = n.UploadFile(ctx, host, ssh.UploadOptions{
		LocalPath:  tmpTimer,
		RemotePath: "/etc/systemd/system/frameworks-ingress-sync.timer",
		Mode:       0644,
	})
	if err != nil {
		return fmt.Errorf("upload ingress sync timer: %w", err)
	}

	result, err := n.RunCommand(ctx, host, "systemctl daemon-reload && systemctl enable --now frameworks-ingress-sync.timer")
	if err != nil || result.ExitCode != 0 {
		return fmt.Errorf("enable ingress sync timer: %w (stderr: %s)", err, result.Stderr)
	}

	return nil
}
