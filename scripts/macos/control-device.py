#!/usr/bin/env python3
"""Build-only bounded status disk. No credentials, runner READY or JIT support."""
import argparse
import fcntl
import json
import os
from pathlib import Path
import secrets
import stat


def strict_json(data):
    def object_pairs(pairs):
        result = {}
        for key, value in pairs:
            if key in result:
                raise ValueError('Duplicate JSON field')
            result[key] = value
        return result
    return json.loads(data, object_pairs_hook=object_pairs)


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('operation', choices=['create', 'read'])
    p.add_argument('state', type=Path)
    a = p.parse_args()
    s = a.state.lstat()
    if not stat.S_ISDIR(s.st_mode) or s.st_uid != os.getuid() or s.st_mode & 0o077:
        p.error('Expected owned private state directory')
    if a.operation == 'create':
        lock = os.open(Path.home() / '.local/state/chickadee-macos/vm.lock', os.O_RDWR | os.O_NOFOLLOW)
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        fd = os.open(a.state / 'build-control.raw', os.O_CREAT | os.O_EXCL | os.O_RDWR | os.O_NOFOLLOW, 0o600)
        try:
            header = json.dumps({'v': 1, 'kind': 'chickadee-build-control', 'nonce': secrets.token_hex(16)}).encode()
            os.ftruncate(fd, 1024*1024)
            os.pwrite(fd, header.ljust(4096, b'\0'), 0)
            os.fsync(fd)
            metadata = os.open(a.state / 'build-control.json', os.O_CREAT | os.O_EXCL | os.O_WRONLY | os.O_NOFOLLOW, 0o600)
            try:
                os.write(metadata, header)
                os.fsync(metadata)
            finally:
                os.close(metadata)
        finally:
            os.close(fd); os.close(lock)
        print('BUILD_CONTROL_CREATED')
    else:
        fd = os.open(a.state / 'build-control.raw', os.O_RDONLY | os.O_NOFOLLOW)
        try:
            s = os.fstat(fd)
            if not stat.S_ISREG(s.st_mode) or s.st_uid != os.getuid() or s.st_mode & 0o077 or s.st_size != 1024*1024:
                p.error('Invalid build status disk')
            metadata = os.open(a.state / 'build-control.json', os.O_RDONLY | os.O_NOFOLLOW)
            try:
                m = os.fstat(metadata)
                if not stat.S_ISREG(m.st_mode) or m.st_uid != os.getuid() or m.st_mode & 0o077 or m.st_size > 4096:
                    p.error('Invalid private build nonce metadata')
                header = strict_json(os.read(metadata, 4096))
            finally:
                os.close(metadata)
            data = os.pread(fd, 4096, 4096).rstrip(b'\0')
            if not data:
                print('NO_BUILD_REPORT'); return
            report = strict_json(data)
            if set(report) != {'v', 'nonce', 'status'} or report['v'] != 1 or report['nonce'] != header['nonce'] or report['status'] not in ['PROVISIONING', 'PROVISIONED_OFFLINE']:
                p.error('Rejected untrusted build report')
            print(report['status'])
        finally:
            os.close(fd)


if __name__ == '__main__':
    main()
