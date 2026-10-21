#!/usr/bin/env python3
"""Narrow USN-8948-1 refresh, only inside an offline disposable image copy."""
import argparse
import hashlib
import json
import os
import pathlib
import re
import shutil
import subprocess
import urllib.request


def run(*args, **kwargs):
    return subprocess.run(args, check=True, **kwargs)


def check_file(path, entry):
    with path.open('rb') as data:
        valid = path.stat().st_size == entry['size'] and hashlib.file_digest(data, 'sha256').hexdigest() == entry['sha256']
    if not valid:
        raise ValueError('PHP input checksum/size mismatch: ' + path.name)


def inputs(policy, destination, offline=False):
    destination.mkdir(parents=True, exist_ok=True)
    entries = [e for release in policy['releases'].values() for e in release['packages']]
    if sum(e['size'] for e in entries) > 64 * 1024 * 1024:
        raise ValueError('PHP supplemental download exceeds 64 MiB budget')
    for entry in entries:
        name = entry['filename']
        if pathlib.Path(name).name != name or not entry['url'].startswith('https://security.ubuntu.com/ubuntu/pool/'):
            raise ValueError('invalid official PHP input')
        target = destination / name
        if not target.exists():
            if offline:
                raise ValueError('missing prefetched PHP input: ' + name)
            part = target.with_suffix('.part')
            try:
                with urllib.request.urlopen(entry['url'], timeout=60) as response, part.open('wb') as output:
                    remaining = entry['size'] + 1
                    while remaining:
                        data = response.read(min(1024 * 1024, remaining))
                        if not data:
                            break
                        output.write(data)
                        remaining -= len(data)
                check_file(part, entry)
                part.rename(target)
            finally:
                part.unlink(missing_ok=True)
        check_file(target, entry)


def inventory(text):
    rows = []
    for line in text.splitlines():
        name, version, source, architecture, status = line.split('\t')
        if status == 'installed':
            rows.append({'package': name, 'version': version, 'source': source, 'architecture': architecture})
    return rows


def compare(left, relation, right):
    return subprocess.run(['dpkg', '--compare-versions', left, relation, right], check=False).returncode == 0


def cohort(rows, release):
    runtime = [r for r in rows if re.fullmatch(r'php\d+\.\d+', r['source'])]
    if any(r['source'] != release['source'] for r in runtime):
        raise ValueError('unsupported installed PHP source; require its vendor fix')
    entries = {e['package']: e for e in release['packages']}
    for row in runtime:
        entry = entries.get(row['package'])
        if entry is None or row['architecture'] != entry['architecture']:
            raise ValueError('installed PHP package missing from authenticated cohort: ' + row['package'])
    return runtime


def require_fixed(rows, release):
    runtime = cohort(rows, release)
    if any(not compare(r['version'], 'ge', release['minimum_version']) for r in runtime):
        raise ValueError('installed PHP runtime is below the vendor fixed version')
    if len({r['version'] for r in runtime}) > 1:
        raise ValueError('installed PHP source cohort has inconsistent versions')
    return runtime


def query():
    text = run('dpkg-query', '-W', '-f=${Package}\t${Version}\t${source:Package}\t${Architecture}\t${db:Status-Status}\n', capture_output=True, text=True).stdout
    return inventory(text)


def smoke(rows):
    result = {}
    if not shutil.which('php'):
        if any(r['package'].endswith('-cli') for r in rows):
            raise ValueError('installed PHP CLI missing from PATH')
        return {'cli': 'not installed'}
    for key, args in [('version', ['php', '-v']), ('modules', ['php', '-m']), ('runtime', ['php', '-r', 'echo json_encode(["version"=>PHP_VERSION,"binary"=>PHP_BINARY,"extensions"=>get_loaded_extensions()]);'])]:
        result[key] = run(*args, capture_output=True, text=True, timeout=30).stdout
    identity = json.loads(result['runtime'])
    owner = run('dpkg-query', '-S', str(pathlib.Path(identity['binary']).resolve()), capture_output=True, text=True).stdout
    if not any(line.split(': ', 1)[0].split(':', 1)[0] in {r['package'] for r in rows if r['package'].endswith('-cli')} for line in owner.splitlines()):
        raise ValueError('default PHP binary is not owned by the authenticated installed CLI cohort')
    result['binary_owner'] = owner
    extensions = set(identity['extensions'])
    for row in rows:
        suffix = row['package'].rsplit('-', 1)[-1]
        module = {'intl': 'intl', 'mbstring': 'mbstring', 'readline': 'readline', 'opcache': 'Zend OPcache'}.get(suffix)
        if module and module not in extensions:
            raise ValueError('PHP extension not loaded after refresh: ' + module)
    if shutil.which('composer'):
        env = dict(os.environ, COMPOSER_DISABLE_NETWORK='1', COMPOSER_ALLOW_SUPERUSER='1')
        result['composer'] = run('composer', '--no-plugins', '--no-scripts', '--no-interaction', '--no-ansi', '--version', env=env, capture_output=True, text=True, timeout=30).stdout
    else:
        result['composer'] = 'not installed'
    return result


def install(policy, directory):
    if os.geteuid() != 0:
        raise ValueError('root required inside offline disposable image')
    os_release = dict(line.split('=', 1) for line in pathlib.Path('/etc/os-release').read_text().splitlines() if '=' in line)
    distro = os_release['ID'].strip('"')
    version = os_release['VERSION_ID'].strip('"')
    report = {'schema': 1, 'notice': policy['notice'], 'cves': policy['cves'], 'os': distro, 'os_version': version}
    if distro == 'rocky':
        packages = run('rpm', '-qa', '--qf', '%{NAME}\t%{SOURCERPM}\n', capture_output=True, text=True).stdout
        if any(re.match(r'php(?:-|\d)', line) or '\tphp-' in line for line in packages.splitlines()) or shutil.which('php'):
            raise ValueError('Rocky PHP present: resolve a native vendor fix before proceeding')
        report.update(status='not-installed', runtime_packages=[])
    elif distro == 'ubuntu' and version in policy['releases']:
        release = policy['releases'][version]
        before = query()
        runtime = cohort(before, release)
        if not runtime and shutil.which('php'):
            raise ValueError('unmanaged PHP executable present')
        report.update(minimum_version=release['minimum_version'], authentication=release['authentication'], before=runtime)
        if runtime:
            if run('dpkg', '--audit', capture_output=True, text=True).stdout.strip():
                raise ValueError('package database requires repair before PHP-only update')
            entries = {e['package']: e for e in release['packages']}
            update = any(not compare(r['version'], 'ge', release['minimum_version']) for r in runtime)
            if update:
                if any(compare(r['version'], 'gt', release['version']) for r in runtime):
                    raise ValueError('PHP update would downgrade a newer cohort member')
                paths = []
                for row in runtime:
                    entry = entries[row['package']]
                    path = directory / entry['filename']
                    check_file(path, entry)
                    fields = run('dpkg-deb', '-f', str(path), 'Package', 'Version', 'Architecture', 'Source', capture_output=True, text=True).stdout
                    actual = dict(line.split(': ', 1) for line in fields.splitlines())
                    if (actual['Package'], actual['Version'], actual['Architecture'], actual.get('Source', actual['Package']).split(' ')[0]) != (entry['package'], entry['version'], entry['architecture'], release['source']):
                        raise ValueError('PHP deb identity mismatch')
                    paths.append(str(path))
                # Prevent maintainer scripts starting daemons in the offline appliance.
                rc = pathlib.Path('/usr/sbin/policy-rc.d')
                previous = (rc.read_bytes(), rc.stat().st_mode & 0o777) if rc.exists() else None
                if rc.is_symlink():
                    raise ValueError('refuse to replace a symlink service policy')
                try:
                    rc.write_text('#!/bin/sh\nexit 101\n'); rc.chmod(0o755)
                    run('dpkg', '--force-confold', '--install', *paths, env=dict(os.environ, DEBIAN_FRONTEND='noninteractive', SYSTEMD_OFFLINE='1'))
                finally:
                    if previous:
                        rc.write_bytes(previous[0]); rc.chmod(previous[1])
                    else:
                        rc.unlink(missing_ok=True)
            after = query()
            fixed = require_fixed(after, release)
            # No package additions/removals or non-PHP version changes are allowed.
            old = {r['package']: r for r in before if r not in runtime}
            new = {r['package']: r for r in after if r not in fixed}
            if old != new or {r['package'] for r in runtime} != {r['package'] for r in fixed}:
                raise ValueError('PHP-only transaction changed unrelated package inventory')
            if run('dpkg', '--audit', capture_output=True, text=True).stdout.strip():
                raise ValueError('PHP transaction left an inconsistent package database')
            report.update(status='updated' if update else 'already-fixed', runtime_packages=fixed, smoke=smoke(fixed))
        else:
            report.update(status='not-installed', runtime_packages=[])
    else:
        raise ValueError('unsupported image OS for PHP audit')
    target = pathlib.Path('/etc/chickadee/php-security.json')
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_text(json.dumps(report, indent=2) + '\n')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--policy', required=True, type=pathlib.Path)
    group = parser.add_mutually_exclusive_group(required=True)
    for flag in ['download', 'verify', 'install']:
        group.add_argument('--' + flag, type=pathlib.Path)
    args = parser.parse_args()
    policy = json.loads(args.policy.read_text())
    if args.install:
        install(policy, args.install)
    else:
        inputs(policy, args.verify or args.download, offline=args.verify is not None)


if __name__ == '__main__':
    main()
