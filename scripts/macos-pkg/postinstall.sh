#!/bin/bash
set -euo pipefail

# Restart edge services if plists exist (upgrade scenario).
# Check both user LaunchAgents and system LaunchDaemons.
CONSOLE_USER=$(stat -f '%Su' /dev/console 2>/dev/null || echo "")
CONSOLE_UID=$(id -u "$CONSOLE_USER" 2>/dev/null || echo "")

for svc in helmsman mistserver caddy; do
  label="com.livepeer.frameworks.${svc}"

  # User domain (LaunchAgent) — preferred if present
  if [ -n "$CONSOLE_USER" ] && [ -n "$CONSOLE_UID" ]; then
    user_plist="/Users/${CONSOLE_USER}/Library/LaunchAgents/${label}.plist"
    if [ -f "$user_plist" ]; then
      launchctl bootstrap "gui/${CONSOLE_UID}" "$user_plist" 2>/dev/null || true
      continue
    fi
  fi

  # System domain (LaunchDaemon)
  sys_plist="/Library/LaunchDaemons/${label}.plist"
  if [ -f "$sys_plist" ]; then
    launchctl bootstrap system "$sys_plist" 2>/dev/null || true
  fi
done

echo ""
echo "FrameWorks installed successfully."
echo ""
echo "  CLI:        /usr/local/bin/frameworks"
echo "  Uninstall:  /usr/local/bin/frameworks-uninstall"
if [ -d "/Applications/FrameWorks.app" ]; then
  echo "  Tray app:   /Applications/FrameWorks.app"
  # Launch tray app for the console user (postinstall runs as root)
  if [ -n "$CONSOLE_USER" ] && [ "$CONSOLE_USER" != "root" ]; then
    sudo -u "$CONSOLE_USER" open "/Applications/FrameWorks.app" 2>/dev/null || true
  fi
fi
echo ""
echo "Get started:  frameworks --help"
echo ""

exit 0
