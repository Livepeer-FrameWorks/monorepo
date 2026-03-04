#!/bin/bash
set -euo pipefail

# Stop edge services if running (upgrade scenario)
for svc in helmsman mistserver caddy; do
  label="com.livepeer.frameworks.${svc}"
  launchctl bootout "system/${label}" 2>/dev/null || true
done

# Remove previous uninstaller symlink
rm -f /usr/local/bin/frameworks-uninstall

exit 0
