#!/usr/bin/env python3
"""Serial bounded prefetch of a prepared Ubuntu native transaction; no package execution."""
import argparse
import hashlib
import json
import pathlib
import re
import time
import urllib.request


def verify(row, path):
    if path.is_symlink() or not path.is_file() or path.stat().st_size != row['size']:
        raise ValueError('missing/invalid input: ' + row['filename'])
    with path.open('rb') as stream:
        if hashlib.file_digest(stream, 'sha256').hexdigest() != row['sha256']:
            raise ValueError('input hash mismatch: ' + row['filename'])


def prefetch(plan, output, offline=False):
    entries = plan['packages']
    if len(entries) > 512 or sum(r['size'] for r in entries) != plan['download_bytes'] or plan['download_bytes'] > 1024 ** 3:
        raise ValueError('invalid/excessive finite input budget')
    names = set()
    for row in entries:
        if not re.fullmatch(r'[A-Za-z0-9.+_~%:-]+\.deb', row['filename']) or row['filename'] in names or not re.fullmatch('[0-9a-f]{64}', row['sha256']) or row['size'] <= 0:
            raise ValueError('invalid deb input identity')
        if not row['url'].startswith('https://snapshot.ubuntu.com/ubuntu/' + plan['snapshot'] + '/pool/'):
            raise ValueError('noncanonical snapshot URL')
        names.add(row['filename'])
    output.mkdir(parents=True, exist_ok=True)
    started, transferred = time.monotonic(), 0
    for row in entries:
        path = output / row['filename']
        if not path.exists() and not offline:
            temporary = path.with_name(path.name + '.part')
            with temporary.open('xb') as target, urllib.request.urlopen(row['url'], timeout=30) as source:
                remaining = row['size']
                while True:
                    if time.monotonic() - started > 600:
                        raise TimeoutError('finite ten-minute prefetch deadline exceeded')
                    block = source.read(min(65536, remaining + 1))
                    if not block:
                        break
                    remaining -= len(block)
                    if remaining < 0:
                        raise ValueError('input exceeded signed size')
                    target.write(block)
                    transferred += len(block)
                    delay = transferred / (4 * 1024 ** 2) - (time.monotonic() - started)
                    if delay > 0:
                        time.sleep(delay)
            verify(row, temporary)
            temporary.rename(path)
        verify(row, path)
        print(row['filename'] + ' verified', flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--verify-only', action='store_true')
    parser.add_argument('plan', type=pathlib.Path)
    parser.add_argument('cache', type=pathlib.Path)
    args = parser.parse_args()
    prefetch(json.loads(args.plan.read_text()), args.cache, args.verify_only)


if __name__ == '__main__':
    main()
