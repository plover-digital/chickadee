#!/usr/bin/env python3
"""Derive effective inventories while retaining parent OS/snapshot/kernel/bootstrap provenance."""
import copy
import datetime
import hashlib
import json
import pathlib
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parents[1]


def updated_inventory(original, nodes, default, extras=None, field='tools'):
    result = copy.deepcopy(original)
    managed = {t['name'] for t in nodes}
    result[field] = [t for t in result[field] if t['name'] not in managed] + copy.deepcopy(nodes)
    result['default_node'] = default
    if extras is not None and 'extra_tools' in result:
        result['extra_tools'] = copy.deepcopy(extras)
    return result


def refresh(source, destination, family):
    read = lambda name: json.loads((ROOT / 'guest/images' / name).read_text())
    runtime = json.loads((destination / 'runtime-security.json').read_text())
    trust = json.loads((destination / 'runtime-trust.json').read_text())
    extra = None if family == 'rocky' else read('github-extra-ubuntu-2604.json' if family == '26.04' else 'github-extra-tools.json')
    for name, field in [('tool-inputs.json', 'tools'), ('tool-lock.json', 'tools'), ('compatibility.json', 'installed_toolcache')]:
        if (source / name).exists():
            original = json.loads((source / name).read_text())
            result = updated_inventory(original, runtime['node_toolcache'] + runtime.get('python_toolcache', []), runtime['default_node'], extra['tools'] if extra else None, field)
            (destination / name).write_text(json.dumps(result, indent=2) + '\n')
    if extra:
        (destination / 'extra-inputs.json').write_text(json.dumps(extra, indent=2) + '\n')
    ca = json.loads((source / 'ca-trust.json').read_text())
    ca.update(trust)
    (destination / 'ca-trust.json').write_text(json.dumps(ca, indent=2) + '\n')
    seal = hashlib.sha256((source / 'SHA256SUMS').read_bytes()).hexdigest()
    manifest = json.loads((source / 'manifest.json').read_text())
    manifest.update(runner=runtime['runner']['version'], runtime_parent_bundle_sha256=seal, runtime_security_refresh='2026-10-25')
    (destination / 'manifest.json').write_text(json.dumps(manifest, indent=2) + '\n')
    policies = {name: read(name) for name in ['runner-linux-x64.json', 'nss-security.json', 'rocky-ca-security.json', 'ca-trust.json']}
    provenance = {'schema': 1, 'source_commit': subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip(), 'built_at': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'parent_sha256sums_sha256': seal, 'policies': policies, 'scope': 'official runner/Node/native trust/Python/developer extras refresh; parent OS snapshot/kernel/bootstrap retained; consumer acceptance pending'}
    (destination / 'runtime-build-provenance.json').write_text(json.dumps(provenance, indent=2) + '\n')


if __name__ == '__main__':
    refresh(pathlib.Path(sys.argv[1]), pathlib.Path(sys.argv[2]), sys.argv[3])
