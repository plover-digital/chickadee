#!/usr/bin/env python3
"""Write effective tool inventories for this specific immutable trust refresh."""
import copy
import json
import pathlib
import sys


def updated_lock(original, go_lock, field='tools'):
    updated = copy.deepcopy(original)
    tools = original[field]
    updated[field] = [t for t in tools if t['name'] != 'go'] + copy.deepcopy(go_lock['tools'])
    if field == 'tools' or 'default_go' in original:
        updated['default_go'] = go_lock['default_go']
    return updated


def refresh(source, destination):
    report = json.loads((destination / 'ca-trust.json').read_text())
    if not report['go_toolcache']:
        return
    go_lock = report['go_toolcache']['lock']
    for name, field in [('tool-inputs.json', 'tools'), ('tool-lock.json', 'tools'),
                        ('compatibility.json', 'installed_toolcache')]:
        path = source / name
        if not path.exists():
            continue
        original = json.loads(path.read_text())
        if field not in original:
            raise ValueError('missing tool inventory: ' + name)
        parent = destination / ('parent-' + name)
        if parent.exists() and parent.read_bytes() != path.read_bytes():
            raise ValueError('different parent tool inventory already exists')
        parent.write_bytes(path.read_bytes())
        (destination / name).write_text(json.dumps(updated_lock(original, go_lock, field), indent=2) + '\n')


if __name__ == '__main__':
    refresh(*(pathlib.Path(p) for p in sys.argv[1:]))
