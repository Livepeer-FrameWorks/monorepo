#!/bin/bash
set -euo pipefail

echo "Uninstalling FrameWorks..."

# Detect console user for user-domain cleanup
CONSOLE_USER=$(stat -f '%Su' /dev/console 2>/dev/null || echo "")
CONSOLE_UID=$(id -u "$CONSOLE_USER" 2>/dev/null || echo "")

# Stop and remove edge launchd services (both domains)
for svc in helmsman mistserver caddy; do
  label="com.livepeer.frameworks.${svc}"

  # User domain (LaunchAgent)
  if [ -n "$CONSOLE_USER" ] && [ -n "$CONSOLE_UID" ]; then
    user_plist="/Users/${CONSOLE_USER}/Library/LaunchAgents/${label}.plist"
    launchctl bootout "gui/${CONSOLE_UID}/${label}" 2>/dev/null || true
    rm -f "$user_plist"
  fi

  # System domain (LaunchDaemon)
  sys_plist="/Library/LaunchDaemons/${label}.plist"
  launchctl bootout "system/${label}" 2>/dev/null || true
  rm -f "$sys_plist"
done

# Remove CLI
rm -f /usr/local/bin/frameworks
rm -f /usr/local/bin/frameworks-uninstall

# Remove tray app if installed
if [ -d "/Applications/FrameWorks.app" ]; then
  echo "Removing FrameWorks.app..."
  rm -rf "/Applications/FrameWorks.app"
fi

# Remove system-domain edge directories
rm -rf /usr/local/opt/frameworks
rm -rf /usr/local/etc/frameworks
rm -rf /usr/local/var/log/frameworks
rm -rf /usr/local/var/lib/caddy

# Remove user-domain edge directories
if [ -n "$CONSOLE_USER" ]; then
  user_home="/Users/${CONSOLE_USER}"
  rm -rf "${user_home}/.local/opt/frameworks"
  rm -rf "${user_home}/.config/frameworks"
  rm -rf "${user_home}/.local/var/log/frameworks"
  rm -rf "${user_home}/.local/var/lib/caddy"
fi

# Forget the package receipt
pkgutil --forget com.livepeer.frameworks 2>/dev/null || true

echo "FrameWorks uninstalled."
