#!/usr/bin/env python3
"""The edge fixture must observe remote EOF while its request input remains open."""
import os
import signal
import socket
import subprocess
import threading
import unittest
from unittest.mock import patch, MagicMock

from edge_group_lifecycle import Connection


class EdgeLifecycleTunnelTest(unittest.TestCase):
    def test_remote_eof_reaches_the_fixture_without_closing_its_stdin(self):
        close_remote = threading.Event()
        server = socket.socket()
        server.bind(('127.0.0.1', 0))
        server.listen()
        self.addCleanup(server.close)
        port = server.getsockname()[1]
        body = b'<cross-domain-policy/>'

        def serve():
            with server.accept()[0] as client:
                request = b''
                while b'\r\n\r\n' not in request:
                    request += client.recv(4096)
                client.sendall(b'HTTP/1.1 200 OK\r\nContent-Length: ' + str(len(body)).encode() + b'\r\n\r\n' + body)
                close_remote.wait(20)

        worker = threading.Thread(target=serve, daemon=True)
        worker.start()
        real_popen = subprocess.Popen
        processes = []

        def local_tunnel(args, **kwargs):
            command = args[-1].replace('/127.0.0.1/8080', '/127.0.0.1/' + str(port))
            process = real_popen(['bash', '-c', command], start_new_session=True, **kwargs)
            processes.append(process)
            return process

        try:
            with patch('edge_group_lifecycle.subprocess.Popen', side_effect=local_tunnel):
                connection = Connection('fixture')
            connection.request('/crossdomain.xml')
            close_remote.set()
            self.assertFalse(connection.process.stdin.closed)
            connection.eof()
            connection.close()
        finally:
            close_remote.set()
            worker.join(timeout=2)
            for process in processes:
                try:
                    os.killpg(process.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                process.wait(timeout=2)
                for pipe in (process.stdin, process.stdout, process.stderr):
                    pipe.close()


class EdgeLifecycleRestartTest(unittest.TestCase):
    def exercise(self, action, listener_not_ready=False):
        from edge_group_lifecycle import exercise
        http, viewer, first, second = (MagicMock() for _ in range(4))
        if listener_not_ready:
            first.request.side_effect = AssertionError('listener not ready')
        connections = [http, viewer, first, second]
        rows = [{}, {20: (10, 10, 'S', 'MistOutHTTP')}, {}]
        with patch('edge_group_lifecycle.Connection', side_effect=connections), \
             patch('edge_group_lifecycle.table', side_effect=rows), \
             patch('edge_group_lifecycle.angel', side_effect=[(10, 10), (30, 30)]), \
             patch('edge_group_lifecycle.execute') as command:
            exercise('edge', 'source', action)
        return command, second

    def test_restart_uses_the_shipped_s6_command_path(self):
        command, _ = self.exercise('restart')
        command.assert_called_once_with('edge', '/command/s6-svc', '-r', '/run/service/mistserver')

    def test_crash_waits_for_the_new_listener(self):
        _, connection = self.exercise('crash', listener_not_ready=True)
        connection.request.assert_called_once_with('/crossdomain.xml')

    def test_restart_waits_for_the_new_listener(self):
        _, connection = self.exercise('restart', listener_not_ready=True)
        connection.request.assert_called_once_with('/crossdomain.xml')


if __name__ == '__main__':
    unittest.main()
