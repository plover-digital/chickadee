#!/usr/bin/env python3
"""Install only the authenticated native NSS cohort already present in an offline image."""
import argparse
import hashlib
import json
import os
import pathlib
import subprocess


def verify(directory, policy):
    for release in policy['releases'].values():
        for entry in release['packages']:
            path = directory / entry['filename']
            with path.open('rb') as stream:
                if path.stat().st_size != entry['size'] or hashlib.file_digest(stream, 'sha256').hexdigest() != entry['sha256']:
                    raise ValueError('NSS input checksum/size mismatch: ' + path.name)


def installed():
    text = subprocess.check_output(['dpkg-query', '-W', '-f=${Package}\t${Version}\t${source:Package}\t${db:Status-Status}\n'], text=True)
    return [dict(zip(['package', 'version', 'source', 'status'], line.split('\t'), strict=True)) for line in text.splitlines()]


def select(rows, release):
    entries = {e['package']: e for e in release['packages']}
    cohort = [r for r in rows if r['status'] == 'installed' and r['source'] == 'nss']
    if not cohort:
        raise ValueError('no installed native NSS source cohort')
    if any(r['package'] not in entries for r in cohort):
        raise ValueError('installed NSS member absent from authenticated cohort')
    return [entries[r['package']] for r in cohort]


def install(directory, policy):
    if os.geteuid() != 0:
        raise ValueError('offline image root required')
    os_release = dict(line.split('=', 1) for line in pathlib.Path('/etc/os-release').read_text().splitlines() if '=' in line)
    if os_release['ID'].strip('"') != 'ubuntu':
        raise ValueError('Ubuntu NSS policy cannot repair another distribution')
    release = policy['releases'][os_release['VERSION_ID'].strip('"')]
    before = installed()
    selected = select(before, release)
    # Do not downgrade a newer vendor image under an older supplemental policy.
    for row in before:
        if row['package'] in {e['package'] for e in selected}:
            subprocess.run(['dpkg', '--compare-versions', row['version'], 'le', release['minimum_version']], check=True)
    subprocess.run(['dpkg', '--install', *[str(directory / e['filename']) for e in selected]], check=True)
    after = installed()
    expected = {e['package']: e['version'] for e in selected}
    actual = {r['package']: r['version'] for r in after if r['source'] == 'nss' and r['status'] == 'installed'}
    if actual != expected:
        raise ValueError('native NSS post-install cohort mismatch')
    unaffected = lambda rows: sorted((r['package'], r['version'], r['source'], r['status']) for r in rows if r['package'] not in expected)
    if unaffected(before) != unaffected(after):
        raise ValueError('unrelated package inventory changed')
    report = {'schema': 1, 'packages': selected, 'scope': 'official native NSS packages; actual all12 controls required; no browser profile clearance'}
    pathlib.Path('/etc/chickadee/nss-security.json').write_text(json.dumps(report, indent=2) + '\n')


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--policy', type=pathlib.Path, required=True)
    parser.add_argument('--install', action='store_true')
    parser.add_argument('directory', type=pathlib.Path)
    args = parser.parse_args()
    policy = json.loads(args.policy.read_text())
    verify(args.directory, policy)
    if args.install:
        install(args.directory, policy)


if __name__ == '__main__':
    main()
