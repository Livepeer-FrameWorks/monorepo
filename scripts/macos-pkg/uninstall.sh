#!/bin/bash
set -euo pipefail

echo "Uninstalling FrameWorks..."

# Stop and remove edge launchd services
for svc in helmsman mistserver caddy; do
  label="com.livepeer.frameworks.${svc}"
  plist="/Library/LaunchDaemons/${label}.plist"
  launchctl bootout "system/${label}" 2>/dev/null || true
  rm -f "${plist}"
done

# Remove CLI
rm -f /usr/local/bin/frameworks
rm -f /usr/local/bin/frameworks-uninstall

# Remove tray app if installed
if [ -d "/Applications/FrameWorks.app" ]; then
  echo "Removing FrameWorks.app..."
  rm -rf "/Applications/FrameWorks.app"
fi

# Remove edge directories
rm -rf /usr/local/opt/frameworks
rm -rf /usr/local/etc/frameworks
rm -rf /usr/local/var/log/frameworks
rm -rf /usr/local/var/lib/caddy

# Forget the package receipt
pkgutil --forget com.livepeer.frameworks 2>/dev/null || true

echo "FrameWorks uninstalled."
