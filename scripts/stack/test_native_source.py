#!/usr/bin/env python3
"""Check native-source child cleanup without starting HTTP or ffmpeg."""
import ast
import pathlib
import subprocess
import unittest
from unittest import mock


class NativeSourceCleanupTest(unittest.TestCase):
    def load_source(self):
        path = pathlib.Path(__file__).with_name("native-source.py")
        tree = ast.parse(path.read_text(), filename=str(path))
        # Load definitions only: the executable script starts an HTTP server.
        tree.body = [node for node in tree.body if isinstance(
            node, (ast.Import, ast.ImportFrom, ast.FunctionDef, ast.ClassDef))]
        namespace = {"processes": set()}
        exec(compile(tree, str(path), "exec"), namespace)
        return namespace

    def request(self, namespace, waits):
        process = mock.Mock()
        process.stdout.read.return_value = b"transport packet"
        process.wait.side_effect = waits
        handler = namespace["Source"].__new__(namespace["Source"])
        handler.path = "/loop.ts"
        handler.send_response = mock.Mock()
        handler.send_header = mock.Mock()
        handler.end_headers = mock.Mock()
        handler.wfile = mock.Mock()
        handler.wfile.write.side_effect = BrokenPipeError()
        with mock.patch.object(namespace["subprocess"], "Popen", return_value=process), mock.patch.object(
                namespace["sys"], "argv", ["native-source.py", "fixture.mp4"]):
            handler.do_GET()
        self.assertFalse(namespace["processes"])
        return process

    def test_disconnected_client_escalates_a_child_that_ignores_terminate(self):
        process = self.request(self.load_source(), [subprocess.TimeoutExpired("ffmpeg", 5), 0])
        process.kill.assert_called_once_with()
        self.assertEqual(process.wait.call_count, 2)

    def test_ordinary_child_shutdown_does_not_kill(self):
        process = self.request(self.load_source(), [0])
        process.terminate.assert_called_once_with()
        process.kill.assert_not_called()


if __name__ == "__main__":
    unittest.main()
