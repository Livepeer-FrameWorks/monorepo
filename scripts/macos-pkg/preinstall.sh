#!/bin/bash
set -euo pipefail

# Stop edge services if running (upgrade scenario).
# Check both user LaunchAgents and system LaunchDaemons.
CONSOLE_USER=$(stat -f '%Su' /dev/console 2>/dev/null || echo "")
CONSOLE_UID=$(id -u "$CONSOLE_USER" 2>/dev/null || echo "")

for svc in helmsman mistserver caddy; do
  label="com.livepeer.frameworks.${svc}"

  # User domain (LaunchAgent)
  if [ -n "$CONSOLE_USER" ] && [ -n "$CONSOLE_UID" ]; then
    user_plist="/Users/${CONSOLE_USER}/Library/LaunchAgents/${label}.plist"
    if [ -f "$user_plist" ]; then
      launchctl bootout "gui/${CONSOLE_UID}/${label}" 2>/dev/null || true
    fi
  fi

  # System domain (LaunchDaemon)
  launchctl bootout "system/${label}" 2>/dev/null || true
done

# Remove previous uninstaller symlink
rm -f /usr/local/bin/frameworks-uninstall

exit 0
