#!/usr/bin/env python3
"""Record a supplemental native transaction without relabelling the imported root snapshot."""
import datetime
import hashlib
import json
import pathlib
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parents[1]


def refresh(source, output):
    plan = json.loads((output / 'os-package-plan.json').read_text())
    report = json.loads((output / 'os-security.json').read_text())
    if report['kernel'] != plan['kernel'] or not report['native_apt_exact_plan'] or report['packages'] != plan['packages']:
        raise ValueError('native transaction report does not match prepared lock')
    manifest = json.loads((source / 'manifest.json').read_text())
    parent_seal = hashlib.sha256((source / 'SHA256SUMS').read_bytes()).hexdigest()
    manifest.update(kernel=plan['kernel'], native_os_parent_bundle_sha256=parent_seal,
                    native_os_security_snapshot=plan['snapshot'])
    # snapshot/root/build-inputs and all tool/runtime inputs remain their real parent provenance.
    (output / 'manifest.json').write_text(json.dumps(manifest, indent=2) + '\n')
    trust = json.loads((source / 'ca-trust.json').read_text())
    trust['java_stores'] = json.loads((output / 'os-java-trust.json').read_text())['java_stores']
    (output / 'ca-trust.json').write_text(json.dumps(trust, indent=2) + '\n')
    provenance = {'schema': 1, 'source_commit': subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip(),
                  'built_at': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'parent_sha256sums_sha256': parent_seal,
                  'plan_sha256': hashlib.sha256((output / 'os-package-plan.json').read_bytes()).hexdigest(),
                  'kernel': plan['kernel'], 'signed_indexes': plan['signed_indexes'],
                  'bootstrap_sha256': report['bootstrap_sha256'],
                  'scope': 'native Ubuntu installed-package updates and generic kernel/initrd; original root/full snapshot and separate runtime provenance retained; all consumer/cold-boot/job acceptance pending'}
    (output / 'os-build-provenance.json').write_text(json.dumps(provenance, indent=2) + '\n')
    # Effective native inventory must preserve every non-transaction package and retain protected cohorts.
    before = {r['name'].split(':')[0]: r for r in plan['parent_packages']}
    after = {r['name'].split(':')[0]: r for r in json.loads((output / 'package-provenance.json').read_text())['packages']}
    changed = {r['name'] for r in plan['packages']}
    for row in plan['packages']:
        if after.get(row['name'], {}).get('version') != row['version'] or after[row['name']]['architecture'] != row['architecture']:
            raise ValueError('effective transaction package identity mismatch')
    for name, expected in [('vmlinuz-' + plan['kernel'], report['kernel_sha256']), ('initrd-' + plan['kernel'], report['initrd_sha256'])]:
        if hashlib.sha256((output / name).read_bytes()).hexdigest() != expected:
            raise ValueError('downloaded kernel/initrd identity mismatch')
    if set(after) != set(before) | changed:
        raise ValueError('native effective inventory has missing/unplanned packages')
    if any(after[name] != row for name, row in before.items() if name.split(':')[0] not in changed):
        raise ValueError('unrelated native provenance changed')


if __name__ == '__main__':
    refresh(pathlib.Path(sys.argv[1]), pathlib.Path(sys.argv[2]))
