#!/usr/bin/env python3
"""Publish an owned live Matroska stream with a clockwise90 display projection."""
import signal
import struct
import subprocess
import sys


def header(source):
    first = source.read(1)
    if not first:
        raise EOFError('Matroska ended before its Tracks element')
    def number(first, identifier=False):
        width = 1
        while width <= 8 and not first[0] & (1 << (8 - width)):
            width += 1
        if width > (4 if identifier else 8):
            raise ValueError('invalid EBML number')
        raw = first + exact(source, width - 1)
        value = int.from_bytes(raw, 'big')
        return (value if identifier else value & ((1 << (7 * width)) - 1)), raw
    identifier, raw_id = number(first, True)
    size, raw_size = number(exact(source, 1))
    return identifier, size, raw_id + raw_size, size == (1 << (7 * len(raw_size))) - 1


def exact(source, size):
    result = bytearray()
    while len(result) < size:
        chunk = source.read(size - len(result))
        if not chunk:
            raise EOFError('truncated EBML element')
        result.extend(chunk)
    return bytes(result)


def element(identifier, value):
    width = next(n for n in range(1, 9) if len(value) < (1 << (7 * n)) - 1)
    return identifier.to_bytes((identifier.bit_length() + 7) // 8, 'big') + ((1 << (7 * width)) | len(value)).to_bytes(width, 'big') + value


def rotate_tracks(data):
    import io
    source = io.BytesIO(data)
    result = bytearray()
    while source.tell() < len(data):
        identifier, size, original, unknown = header(source)
        if unknown or size > 16 * 1024 * 1024:
            raise ValueError('invalid bounded Tracks element')
        value = exact(source, size)
        if identifier in (0xAE, 0xE0):
            value = rotate_tracks(value)
        if identifier == 0xE0:
            # Matroska applies reflection first; a plain clockwise90 turn is roll-90.
            projection = element(0x7671, b'\0') + element(0x7675, struct.pack('>d', -90))
            value += element(0x7670, projection)
        if identifier != 0x7670:
            result.extend(element(identifier, value))
    return bytes(result)


def transform(source, destination):
    identifier, size, original, unknown = header(source)
    if identifier != 0x1A45DFA3 or unknown:
        raise ValueError('missing EBML header')
    destination.write(original + exact(source, size))
    identifier, _, original, unknown = header(source)
    if identifier != 0x18538067 or not unknown:
        raise ValueError('publisher must emit a live unknown-size Segment')
    destination.write(original)
    while True:
        identifier, size, original, unknown = header(source)
        if unknown or size > 16 * 1024 * 1024:
            raise ValueError('missing bounded Tracks before media')
        value = exact(source, size)
        # Header growth changes indexed offsets; a live stream needs no seek table.
        if identifier == 0x114D9B74:
            continue
        if identifier == 0x1654AE6B:
            destination.write(element(identifier, rotate_tracks(value)))
            destination.flush()
            break
        if identifier == 0x1F43B675:
            raise ValueError('publisher emitted media before Tracks')
        destination.write(original + value)
    while chunk := source.read(65536):
        destination.write(chunk)
        destination.flush()


def publish(path, url):
    children = []
    def stop(signum, _):
        raise SystemExit(128 + signum)
    for signum in (signal.SIGINT, signal.SIGTERM):
        signal.signal(signum, stop)
    try:
        source = subprocess.Popen(['ffmpeg', '-v', 'error', '-re', '-stream_loop', '-1', '-i', path,
                                   '-map', '0:v:0', '-c', 'copy', '-f', 'matroska', '-live', '1', '-'],
                                  stdout=subprocess.PIPE, bufsize=0)
        children.append(source)
        sink = subprocess.Popen(['curl', '-sS', '--fail', '--max-time', '600', '-H', 'Content-Type: video/x-matroska',
                                 '-T', '-', url], stdin=subprocess.PIPE)
        children.append(sink)
        transform(source.stdout, sink.stdin)
        sink.stdin.close()
        if sink.wait(timeout=10) or source.wait(timeout=10):
            raise RuntimeError('live Matroska publisher stopped unsuccessfully')
    finally:
        for child in reversed(children):
            if child.poll() is None:
                child.terminate()
        for child in reversed(children):
            try:
                child.wait(timeout=5)
            except subprocess.TimeoutExpired:
                child.kill()
                child.wait()


if __name__ == '__main__':
    publish(*sys.argv[1:])
