#!/usr/bin/env python3
import importlib.util
import fcntl
import json
import os
import subprocess
import tempfile
import time
import unittest
from pathlib import Path
from unittest.mock import patch
from contextlib import ExitStack

spec = importlib.util.spec_from_file_location('storage', Path(__file__).with_name('remote-dev-storage.py'))
storage = importlib.util.module_from_spec(spec)
spec.loader.exec_module(storage)


class StorageTests(unittest.TestCase):
    def test_maintenance_requires_every_job_slot_and_releases_partial_reservation(self):
        with tempfile.TemporaryDirectory() as directory:
            base = Path(directory)
            (base / 'cache').mkdir()
            with (base / 'cache/.heavy-2.lock').open('a') as active:
                fcntl.flock(active, fcntl.LOCK_EX)
                with self.assertRaises(BlockingIOError), ExitStack() as locks:
                    storage.reserve_job_slots(base, locks, 0)
                with (base / 'cache/.heavy-1.lock').open('a') as released:
                    fcntl.flock(released, fcntl.LOCK_EX | fcntl.LOCK_NB)
            with ExitStack() as locks:
                storage.reserve_job_slots(base, locks, 0)

    def test_only_ignored_inactive_generated_outputs_are_candidates(self):
        with tempfile.TemporaryDirectory() as directory:
            workspaces = Path(directory)
            for name in ['retired', 'active', 'fresh', 'source']:
                repo = workspaces / name / 'monorepo'
                repo.mkdir(parents=True)
                subprocess.run(['git', 'init', '-q', str(repo)], check=True)
                (repo / '.gitignore').write_text('node_modules/\nbin/\nedge/dist/\n')
                (repo / 'node_modules').mkdir()
                (repo / 'node_modules/package').write_text('dependency')
                (repo / 'bin').mkdir()
                (repo / '.stack').mkdir()
                (repo / '.stack/evidence.log').write_text('retain this evidence')
                if name == 'source':
                    subprocess.run(['git', '-C', str(repo), 'add', '-f', 'node_modules/package'], check=True)
                for path in [repo / '.git', repo / 'node_modules', repo / 'bin']:
                    os.utime(path, (1, 1))
                if name == 'fresh':
                    (repo / '.stack/last-job').touch()
            outside = workspaces / 'outside'
            outside.mkdir()
            (workspaces / 'retired/monorepo/edge').mkdir()
            (workspaces / 'retired/monorepo/edge/dist').symlink_to(outside)
            found = {str(p.relative_to(workspaces)) for p in storage.candidates(workspaces, {'active'}, time.time() - 86400)}
            self.assertEqual(found, {'retired/monorepo/node_modules', 'retired/monorepo/bin', 'source/monorepo/bin'})
            self.assertEqual((workspaces / 'retired/monorepo/.stack/evidence.log').read_text(), 'retain this evidence')

    def test_go_cache_budget_evicts_oldest_without_touching_other_files(self):
        with tempfile.TemporaryDirectory() as directory:
            cache = Path(directory)
            (cache / 'aa').mkdir()
            oldest = cache / 'aa' / ('a' * 64 + '-d')
            newest = cache / 'aa' / ('b' * 64 + '-d')
            for path, modified in [(oldest, 1), (newest, 2)]:
                path.write_bytes(b'x' * 8192)
                os.utime(path, (modified, modified))
            (cache / 'aa/README').write_text('keep')
            (cache / 'aa' / ('c' * 64 + '-d')).symlink_to(cache / 'aa/README')
            budget = newest.stat().st_blocks * 512
            preview = storage.trim_go_cache(cache, budget, False)
            self.assertTrue(oldest.exists())
            applied = storage.trim_go_cache(cache, budget, True)
            self.assertEqual(preview, applied)
            self.assertFalse(oldest.exists())
            self.assertTrue(newest.exists())
            self.assertEqual((cache / 'aa/README').read_text(), 'keep')

    def test_container_bind_mount_protects_its_workspace(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name in ['one', 'two']:
                (root / name).mkdir()
            containers = [{'Mounts': [{'Source': str(root / 'one')}]}]
            with patch.object(storage, 'command', side_effect=['container', json.dumps(containers)]):
                self.assertEqual(storage.protected_workspaces(root), {'one'})
            containers = [{'Mounts': [{'Source': str(root.parent)}]}]
            with patch.object(storage, 'command', side_effect=['container', json.dumps(containers)]):
                self.assertEqual(storage.protected_workspaces(root), {'one', 'two'})

    def test_go_cache_directory_entries_count_their_contents_and_are_evicted(self):
        with tempfile.TemporaryDirectory() as directory:
            cache = Path(directory)
            (cache / 'aa').mkdir()
            bundle = cache / 'aa' / ('a' * 64 + '-d')
            bundle.mkdir()
            (bundle / 'artifact').write_bytes(b'x' * 16384)
            external = cache / 'retain-source'
            external.write_text('retain')
            (bundle / 'link').symlink_to(external)
            newest = cache / 'aa' / ('b' * 64 + '-d')
            newest.write_bytes(b'x' * 8192)
            os.utime(bundle, (1, 1))
            os.utime(newest, (2, 2))
            result = storage.trim_go_cache(cache, newest.stat().st_blocks * 512, True)
            self.assertFalse(bundle.exists())
            self.assertTrue(newest.exists())
            self.assertEqual(external.read_text(), 'retain')
            self.assertGreaterEqual(result['evicted_bytes'], 16384)


if __name__ == '__main__':
    unittest.main()
