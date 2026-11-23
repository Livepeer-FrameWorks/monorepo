#!/usr/bin/env python3
"""Report remote disk consumers without changing caches, workspaces or job locks."""

import json
import os
import shutil
import subprocess
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path


def capture(args):
    try:
        result = subprocess.run(args, capture_output=True, text=True, timeout=180)
        return {'command': args, 'exit': result.returncode,
                'stdout': result.stdout, 'stderr': result.stderr}
    except subprocess.TimeoutExpired as error:
        def decode(value):
            return value.decode(errors='replace') if isinstance(value, bytes) else value or ''
        return {'command': args, 'exit': None, 'timed_out': True,
                'stdout': decode(error.stdout), 'stderr': decode(error.stderr)}
    except OSError as error:
        return {'command': args, 'exit': None, 'error': str(error)}


def main():
    base = Path('/srv/frameworks-dev')
    usage = shutil.disk_usage(base)
    commands = (
        ['df', '-B1', '-T', str(base), '/'],
        ['lsblk', '-b', '-o', 'NAME,SIZE,TYPE,FSTYPE,MOUNTPOINTS,MODEL'],
        ['findmnt', '--json', '-o', 'TARGET,SOURCE,FSTYPE'],
        ['du', '-x', '-B1', '--max-depth=4', str(base)],
        ['du', '-x', '-B1', '--max-depth=2', str(Path.home())],
        ['docker', 'system', 'df'],
        ['docker', 'system', 'df', '-v'],
        ['docker', 'info', '--format', '{{.DockerRootDir}}'],
    )
    with ThreadPoolExecutor(max_workers=3) as executor:
        results = list(executor.map(capture, commands))
    report = {'read_only': True, 'filesystem': {'path': str(base),
              'device': os.stat(base).st_dev, 'total': usage.total,
              'used': usage.used, 'free': usage.free}, 'commands': results,
              'note': 'Directory totals overlap. Docker image sizes share layers; do not sum virtual image sizes. Partial command failures are retained.'}
    print(json.dumps(report, indent=2))


if __name__ == '__main__':
    main()
