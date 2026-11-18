#!/usr/bin/env python3
"""Bound disposable remote build storage while all heavy-job slots are idle."""

import argparse
import fcntl
import json
import re
import shutil
import subprocess
import time
from contextlib import ExitStack
from pathlib import Path


OUTPUTS = (
    'node_modules', 'bin', 'edge/dist', 'ansible/.cache',
    'website_application/.svelte-kit', 'website_application/build',
    'website_marketing/.svelte-kit', 'website_marketing/build',
    'website_docs/dist', 'npm_api/dist', 'npm_player/dist',
)


def command(*args):
    return subprocess.check_output(args, text=True).strip()


def protected_workspaces(workspaces):
    workspaces = workspaces.resolve()
    protected = set()

    def protect(path, recursive=False):
        resolved = Path(path).resolve()
        if recursive and workspaces.is_relative_to(resolved):
            protected.update(p.name for p in workspaces.iterdir())
            return
        try:
            relative = resolved.relative_to(workspaces)
        except (ValueError, OSError):
            return
        if relative.parts:
            protected.add(relative.parts[0])
        else:
            protected.update(p.name for p in workspaces.iterdir())

    processes = Path('/proc')
    for process in processes.iterdir() if processes.exists() else []:
        if not process.name.isdigit():
            continue
        try:
            protect((process / 'cwd').resolve(strict=True))
        except (OSError, RuntimeError):
            continue
    ids = command('docker', 'ps', '-aq').split()
    if ids:
        for container in json.loads(command('docker', 'inspect', *ids)):
            for mount in container['Mounts']:
                if mount.get('Source'):
                    protect(mount['Source'], recursive=True)
    return protected


def candidates(workspaces, protected, cutoff):
    for workspace in sorted(workspaces.iterdir()):
        repo = workspace / 'monorepo'
        if workspace.is_symlink() or workspace.name in protected or not (repo / '.git').exists():
            continue
        if repo.is_symlink():
            continue
        touched = [repo / '.git', repo / '.stack/last-job']
        if any(p.exists() and p.stat().st_mtime > cutoff for p in touched):
            continue
        for relative in OUTPUTS:
            path = repo / relative
            if not path.is_dir() or path.is_symlink() or path.stat().st_mtime > cutoff:
                continue
            if not path.resolve().is_relative_to(repo.resolve()):
                continue
            if command('git', '-C', str(repo), 'ls-files', '--', relative):
                continue
            if subprocess.run(['git', '-C', str(repo), 'check-ignore', '-q', relative]).returncode:
                continue
            yield path


def trim_go_cache(cache, budget, apply):
    def allocated_bytes(path):
        size = path.lstat().st_blocks * 512
        if path.is_dir() and not path.is_symlink():
            size += sum(allocated_bytes(child) for child in path.iterdir())
        return size

    files = []
    for directory in cache.glob('[0-9a-f][0-9a-f]'):
        if directory.is_symlink() or not directory.is_dir():
            continue
        for path in directory.iterdir():
            if path.is_symlink() or not re.fullmatch(r'[0-9a-f]{64}-[ad]', path.name):
                continue
            stat = path.stat()
            files.append((stat.st_mtime_ns, path, allocated_bytes(path)))
    total = sum(size for _, _, size in files)
    removed = 0
    for modified, path, size in sorted(files):
        if total <= budget:
            break
        if apply:
            if path.stat().st_mtime_ns != modified:
                continue
            if path.is_dir():
                shutil.rmtree(path)
            else:
                path.unlink()
        removed += size
        total -= size
    return {'allocated_after': total, 'evicted_bytes': removed}


def reserve_job_slots(base, locks, wait_seconds):
    deadline = time.monotonic() + wait_seconds
    pending = {number: locks.enter_context((base / f'cache/.heavy-{number}.lock').open('a')) for number in range(1, 4)}
    while pending:
        for number, handle in list(pending.items()):
            try:
                fcntl.flock(handle, fcntl.LOCK_EX | fcntl.LOCK_NB)
                print(f'remote-dev-storage: reserved heavy-job slot {number}/3', flush=True)
                del pending[number]
            except BlockingIOError:
                continue
        if pending:
            if time.monotonic() >= deadline:
                raise BlockingIOError('heavy jobs are active')
            time.sleep(0.2)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--apply', action='store_true')
    parser.add_argument('--keep-days', type=int, default=3)
    parser.add_argument('--go-cache-gb', type=int, default=16)
    parser.add_argument('--docker-cache-gb', type=int, default=20)
    parser.add_argument('--min-free-gb', type=int, default=50)
    parser.add_argument('--keep-workspace', action='append', default=[])
    parser.add_argument('--wait-seconds', type=int, default=0)
    args = parser.parse_args()
    if min(args.keep_days, args.go_cache_gb, args.docker_cache_gb, args.min_free_gb) < 1:
        parser.error('retention, budgets and free-space target must be positive')
    if args.wait_seconds < 0:
        parser.error('wait-seconds must be nonnegative')
    base = Path('/srv/frameworks-dev')
    user = command('id', '-un')
    workspaces = base / 'workspaces' / user
    cache = base / 'cache' / user
    report = {'apply': args.apply, 'started_epoch': time.time(), 'free_before': shutil.disk_usage(base).free}
    with ExitStack() as locks:
        if args.apply:
            try:
                reserve_job_slots(base, locks, args.wait_seconds)
            except BlockingIOError:
                print('remote-dev-storage: heavy jobs are active; retry when they finish', flush=True)
                return 75
        report['free_after_lock'] = shutil.disk_usage(base).free
        protected = protected_workspaces(workspaces) | set(args.keep_workspace)
        paths = list(candidates(workspaces, protected, time.time() - args.keep_days * 86400))
        report.update(protected_workspaces=sorted(protected), generated_outputs=[str(p) for p in paths])
        if args.apply:
            for path in paths:
                shutil.rmtree(path)
        report['go_cache'] = trim_go_cache(cache / 'go-build', args.go_cache_gb * 10**9, args.apply)
        prune = ['docker', 'buildx', 'prune', '--force', '--max-used-space', f'{args.docker_cache_gb}GB', '--min-free-space', f'{args.min_free_gb}GB', '--reserved-space', '8GB']
        report['docker_cache_command'] = prune
        if args.apply:
            result = subprocess.run(prune, capture_output=True, text=True)
            report['docker_cache'] = {'exit': result.returncode, 'stdout': result.stdout, 'stderr': result.stderr}
        report['free_after'] = shutil.disk_usage(base).free
        if args.apply:
            receipts = cache / 'storage-maintenance'
            receipts.mkdir(exist_ok=True)
            receipt = receipts / f'{time.time_ns()}.json'
            report['receipt'] = str(receipt)
            receipt.write_text(json.dumps(report, indent=2) + '\n')
        print(json.dumps(report, indent=2))
        return report.get('docker_cache', {}).get('exit', 0)


if __name__ == '__main__':
    raise SystemExit(main())
