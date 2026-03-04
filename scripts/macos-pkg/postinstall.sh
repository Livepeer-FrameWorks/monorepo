#!/bin/bash
set -euo pipefail

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
