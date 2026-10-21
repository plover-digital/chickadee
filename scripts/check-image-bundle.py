#!/usr/bin/env python3
"""Validate the sealed flat Linux image bundle before admission or copying."""
import hashlib
import json
import pathlib
import re
import stat
import sys


def validate(root):
    root = root.resolve()
    seal = root / 'SHA256SUMS'
    if not seal.is_file() or seal.is_symlink():
        raise ValueError('bundle seal must be a regular file')
    entries = {}
    for line in seal.read_text().splitlines():
        digest, name = line.split(maxsplit=1)
        name = name.lstrip('*')
        if not re.fullmatch(r'[0-9a-f]{64}', digest) or not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9._-]*', name):
            raise ValueError('invalid bundle seal entry')
        if name in entries or name in ('SHA256SUMS', 'vmlinuz', 'initrd'):
            raise ValueError('duplicate or reserved bundle seal entry')
        entries[name] = digest
    if not {'base.qcow2', 'manifest.json'} <= entries.keys():
        raise ValueError('bundle seal missing required files')
    actual = {p.name for p in root.iterdir()}
    if actual != set(entries) | {'SHA256SUMS', 'vmlinuz', 'initrd'}:
        raise ValueError('unsealed or missing bundle entries')
    for name, digest in entries.items():
        path = root / name
        if not stat.S_ISREG(path.lstat().st_mode):
            raise ValueError('sealed bundle entry must be a regular file: ' + name)
        with path.open('rb') as data:
            if hashlib.file_digest(data, 'sha256').hexdigest() != digest:
                raise ValueError('bundle checksum mismatch: ' + name)
    kernel = json.loads((root / 'manifest.json').read_text())['kernel']
    for alias, target in [('vmlinuz', 'vmlinuz-' + kernel), ('initrd', 'initrd-' + kernel)]:
        path = root / alias
        if not path.is_symlink() or path.readlink() != pathlib.Path(target) or target not in entries:
            raise ValueError('bundle alias must point to its expected sealed regular file: ' + alias)
    return entries


if __name__ == '__main__':
    validate(pathlib.Path(sys.argv[1]))
