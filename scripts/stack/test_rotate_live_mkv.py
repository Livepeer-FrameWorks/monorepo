#!/usr/bin/env python3
"""Validate the live publisher's metadata with real demuxing and decoded pixels."""
import io
import json
from pathlib import Path
import subprocess
import tempfile
import unittest

import rotate_live_mkv as publisher


class LiveRotationFixture(unittest.TestCase):
    def test_live_header_preserves_all_packets_and_rotates_pixels(self):
        with tempfile.TemporaryDirectory(prefix='fw-live-mkv-test-') as directory:
            root = Path(directory)
            source, rotated = root / 'source.mkv', root / 'rotated.mkv'
            subprocess.run(['ffmpeg', '-v', 'error', '-f', 'lavfi', '-i', 'color=blue:size=64x48:rate=4:duration=1',
                            '-vf', 'drawbox=x=0:y=0:w=16:h=16:color=red:t=fill', '-c:v', 'libx264', '-threads', '1',
                            '-preset', 'ultrafast', '-pix_fmt', 'yuv420p', '-bf', '0', '-f', 'matroska', '-live', '1', str(source)],
                           check=True, capture_output=True, timeout=20)
            with source.open('rb') as incoming, rotated.open('wb') as outgoing:
                publisher.transform(incoming, outgoing)
            packets = []
            frames = []
            for path in (source, rotated):
                probe = subprocess.run(['ffprobe', '-v', 'error', '-show_packets', '-show_streams', '-of', 'json', str(path)],
                                       check=True, capture_output=True, text=True, timeout=10)
                self.assertFalse(probe.stderr, 'header growth left invalid seek offsets')
                data = json.loads(probe.stdout)
                packets.append([(p['pts'], p['dts'], p['size']) for p in data['packets']])
                decoded = subprocess.run(['ffmpeg', '-v', 'error', '-xerror', '-noautorotate', '-i', str(path),
                                          '-map', '0:v:0', '-f', 'rawvideo', '-pix_fmt', 'rgb24', '-'],
                                         check=True, capture_output=True, timeout=10)
                self.assertFalse(decoded.stderr)
                frames.append(decoded.stdout)
            self.assertEqual(len(packets[0]), 4)
            self.assertEqual(packets[0], packets[1])
            self.assertEqual(frames[0], frames[1], 'metadata transform changed coded pixels')
            decoded = subprocess.run(['ffmpeg', '-v', 'error', '-xerror', '-i', str(rotated), '-frames:v', '1',
                                      '-f', 'rawvideo', '-pix_fmt', 'rgb24', '-'],
                                     check=True, capture_output=True, timeout=10)
            self.assertFalse(decoded.stderr)
            self.assertEqual(len(decoded.stdout), 48 * 64 * 3)
            for index, (x, y) in enumerate(((6, 6), (42, 6), (42, 58), (6, 58))):
                red, _, blue = decoded.stdout[(y * 48 + x) * 3:(y * 48 + x) * 3 + 3]
                if index == 1:
                    self.assertGreater(red, 100)
                    self.assertGreater(red, blue * 1.5)
                else:
                    self.assertGreater(blue, 80)
                    self.assertGreater(blue, red * 1.5)

    def test_requires_a_stream_without_fixed_segment_offsets(self):
        fixed = publisher.element(0x1A45DFA3, b'') + publisher.element(0x18538067, b'')
        with self.assertRaisesRegex(ValueError, 'unknown-size Segment'):
            publisher.transform(io.BytesIO(fixed), io.BytesIO())


if __name__ == '__main__':
    unittest.main()
