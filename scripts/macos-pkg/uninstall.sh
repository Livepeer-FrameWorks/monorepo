#!/bin/bash
set -euo pipefail

echo "Uninstalling FrameWorks..."

# Remove CLI
rm -f /usr/local/bin/frameworks
rm -f /usr/local/bin/frameworks-uninstall

# Remove tray app if installed
if [ -d "/Applications/FrameWorks.app" ]; then
  echo "Removing FrameWorks.app..."
  rm -rf "/Applications/FrameWorks.app"
fi

# Forget the package receipt
pkgutil --forget com.livepeer.frameworks 2>/dev/null || true

echo "FrameWorks uninstalled."
