#!/usr/bin/env python3
"""Decode saved bounded serial diagnostic chunks. Output remains untrusted text."""
import base64
import json
import sys

with open(sys.argv[1], 'rb') as source:
    total = 0
    for line in source:
        total += len(line)
        if total > 8 * 1024 * 1024 or len(line) > 64 * 1024:
            raise ValueError('diagnostic budget exceeded')
        frame = json.loads(line)
        if frame['v'] != 1 or frame['type'] != 'LOG':
            raise ValueError('invalid diagnostic frame')
        sys.stdout.buffer.write(base64.b64decode(frame['data'], validate=True))
