#!/usr/bin/env python3
"""Exercise s6 restart/crash cleanup through real persistent Mist connections."""
import contextlib
import os
import select
import subprocess
import sys
import time


def execute(container, *args):
    return subprocess.check_output(['docker', 'exec', container, *args], text=True, timeout=15)


def table(container):
    rows = execute(container, 'ps', '-eo', 'pid=,ppid=,pgid=,stat=,comm=')
    return {int(p[0]): (int(p[1]), int(p[2]), p[3], p[4])
            for line in rows.splitlines() if len(p := line.split()) == 5}


def eventually(check, seconds, message):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        result = check()
        if result:
            return result
        time.sleep(0.1)
    raise AssertionError(message)


class Connection:
    def __init__(self, container):
        # The edge binds Mist to loopback. Tunnel bytes through an independent exec,
        # so the connection stays open while the supervised service is restarted.
        self.process = subprocess.Popen([
            'docker', 'exec', '-i', container, 'bash', '-c',
            'exec 3<>/dev/tcp/127.0.0.1/8080; cat <&3 & reader=$!; cat >&3; kill "$reader" 2>/dev/null; wait "$reader"'
        ], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        self.buffer = b''

    def receive(self, deadline):
        remaining = deadline - time.monotonic()
        assert remaining > 0 and select.select([self.process.stdout], [], [], remaining)[0], 'Mist socket timed out'
        return os.read(self.process.stdout.fileno(), 65536)

    def request(self, path, streaming=False):
        self.process.stdin.write(('GET %s HTTP/1.1\r\nHost: localhost\r\nConnection: keep-alive\r\n\r\n' % path).encode())
        self.process.stdin.flush()
        deadline = time.monotonic() + 20
        while b'\r\n\r\n' not in self.buffer:
            data = self.receive(deadline)
            assert data, 'persistent Mist socket closed before its response'
            self.buffer += data
        headers, self.buffer = self.buffer.split(b'\r\n\r\n', 1)
        assert headers.splitlines()[0].split()[1] == b'200', headers.decode(errors='replace')
        if streaming:
            while len(self.buffer) < 188 * 5:
                data = self.receive(deadline)
                assert data, 'TS viewer ended before receiving media'
                self.buffer += data
            assert b'\x47' in self.buffer, 'TS viewer received no transport packets'
            return
        lengths = [line.split(b':', 1)[1].strip() for line in headers.splitlines()[1:]
                   if line.lower().startswith(b'content-length:')]
        assert lengths, 'keep-alive fixture requires an explicit content length'
        length = int(lengths[0])
        while len(self.buffer) < length:
            data = self.receive(deadline)
            assert data, 'crossdomain response ended early'
            self.buffer += data
        body, self.buffer = self.buffer[:length], self.buffer[length:]
        assert b'cross-domain-policy' in body, body

    def eof(self):
        self.buffer = b''
        deadline = time.monotonic() + 12
        while self.receive(deadline):
            pass

    def close(self):
        with contextlib.suppress(BrokenPipeError):
            self.process.stdin.close()
        if self.process.poll() is None:
            self.process.terminate()
        with contextlib.suppress(subprocess.TimeoutExpired):
            self.process.wait(timeout=3)
        if self.process.poll() is None:
            self.process.kill()
            self.process.wait()


def angel(container, previous=None):
    rows = table(container)
    return next(((pid, row[1]) for pid, row in rows.items()
                 if row[3] == 'MistController' and not row[2].startswith('Z')
                 and rows.get(row[0], (0, 0, '', ''))[3] != 'MistController'
                 and pid != previous), None)


def exercise(container, stream, action):
    pid, group = eventually(lambda: angel(container), 30, 'supervised Mist angel is missing')
    before = table(container)
    http, viewer = Connection(container), Connection(container)
    try:
        http.request('/crossdomain.xml')
        handlers = {p for p, row in table(container).items()
                    if p not in before and row[1] == group and row[3] == 'MistOutHTTP'}
        assert handlers, 'persistent HTTP request has no connection handler in the supervised group'
        viewer.request('/live+%s.ts' % stream, streaming=True)
        if action == 'reload':
            execute(container, 'kill', '-USR1', str(pid))
            time.sleep(1)
            http.request('/crossdomain.xml')
            assert handlers <= set(table(container)), 'USR1 replaced the existing connection handler'
        elif action == 'restart':
            execute(container, 's6-svc', '-r', '/run/service/mistserver')
        else:
            execute(container, 'kill', '-KILL', str(pid))
        if action != 'reload':
            eventually(lambda: not any(row[1] == group for row in table(container).values()),
                       15, '%s left processes in the old Mist process group %d' % (action, group))
            http.eof()
            viewer.eof()
            eventually(lambda: angel(container, pid), 30, 's6 did not start a new Mist angel')
            fresh = Connection(container)
            try:
                fresh.request('/crossdomain.xml')
            finally:
                fresh.close()
        print('PASS edge Mist group lifecycle:', action, flush=True)
    finally:
        http.close()
        viewer.close()


if __name__ == '__main__':
    exercise(sys.argv[1], sys.argv[2], sys.argv[3])
