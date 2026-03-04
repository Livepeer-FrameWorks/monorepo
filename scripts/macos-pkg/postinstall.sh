#!/bin/bash
set -euo pipefail

# Restart edge services if plists exist (upgrade scenario — edge was previously provisioned)
PLIST_DIR="/Library/LaunchDaemons"
for svc in helmsman mistserver caddy; do
  plist="${PLIST_DIR}/com.livepeer.frameworks.${svc}.plist"
  if [ -f "${plist}" ]; then
    launchctl bootstrap system "${plist}" 2>/dev/null || true
  fi
done

echo ""
echo "FrameWorks installed successfully."
echo ""
echo "  CLI:        /usr/local/bin/frameworks"
echo "  Uninstall:  /usr/local/bin/frameworks-uninstall"
if [ -d "/Applications/FrameWorks.app" ]; then
  echo "  Tray app:   /Applications/FrameWorks.app"
fi
echo ""
echo "Get started:  frameworks --help"
echo ""

exit 0
