#!/usr/bin/env python3
"""Install only an exact offline native Ubuntu transaction in a disposable copy."""
import argparse
import hashlib
import json
import os
import pathlib
import re
import subprocess


def inventory():
    text = subprocess.check_output(['dpkg-query', '-W', '-f=${db:Status-Abbrev}\t${binary:Package}\t${Version}\t${Architecture}\n'], text=True)
    result = {}
    for line in text.splitlines():
        status, name, version, arch = line.split('\t')
        if status[1:2] == 'i':
            if status != 'ii ':
                raise ValueError('incomplete installed dpkg state: ' + name)
            name = name.split(':')[0]
            if name in result:
                raise ValueError('multiarch inventory unsupported')
            result[name] = (version, arch)
    return result


def expected_before(plan):
    rows = plan['parent_packages']
    result = {r['name'].split(':')[0]: (r['version'], r['architecture']) for r in rows}
    if len(result) != len(rows):
        raise ValueError('duplicate/multiarch parent packages')
    return result


def validate_plan_output(output, plan):
    actual = {}
    for line in output.splitlines():
        if line.startswith('Remv '):
            raise ValueError('native APT proposed removal')
        if line.startswith('Inst '):
            match = re.fullmatch(r'Inst ([^ ]+)(?: \[[^]]+\])? \(([^ ]+) .+\)', line)
            if not match:
                raise ValueError('unrecognized native APT transaction row')
            name, version = match.groups()
            name = name.split(':')[0]
            if name in actual:
                raise ValueError('duplicate native APT install row')
            actual[name] = version
    expected = {r['name']: r['version'] for r in plan['packages']}
    if actual != expected:
        raise ValueError('native APT transaction differs from finite prepared lock')
    return actual


def verify_after(before, after, plan):
    expected = dict(before)
    for row in plan['packages']:
        expected[row['name']] = (row['version'], row['architecture'])
    if after != expected:
        raise ValueError('post-install package set/version differs from exact transaction')


def digest(path):
    with path.open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def validate_kernel_config(text, family):
    required = ['CONFIG_VIRTIO_MMIO_CMDLINE_DEVICES=y'] if family == '24.04' else []
    modules = ['CONFIG_VIRTIO_MMIO', 'CONFIG_VIRTIO_BLK', 'CONFIG_VIRTIO_NET'] if family == '24.04' else ['CONFIG_VIRTIO_PCI', 'CONFIG_SCSI_VIRTIO', 'CONFIG_VIRTIO_BLK', 'CONFIG_VIRTIO_NET']
    if any(value not in text.splitlines() for value in required) or any(not re.search('^' + value + r'=(y|m)$', text, re.M) for value in modules):
        raise ValueError('generic kernel lacks required microvm/q35 virtio configuration')


def install(plan, cache, report, expected_bootstrap):
    if os.geteuid() != 0:
        raise ValueError('root inside a disposable image copy required')
    release = dict(line.split('=', 1) for line in pathlib.Path('/etc/os-release').read_text().splitlines() if '=' in line)
    if release.get('ID', '').strip('"') != 'ubuntu' or release.get('VERSION_ID', '').strip('"') != plan['family']:
        raise ValueError('wrong Ubuntu working copy')
    before = inventory()
    if before != expected_before(plan):
        raise ValueError('working copy package inventory differs from accepted parent')
    bootstrap = pathlib.Path('/usr/local/bin/chickadee-guest')
    bootstrap_hash = digest(bootstrap)
    if not re.fullmatch('[0-9a-f]{64}', expected_bootstrap) or bootstrap_hash != expected_bootstrap:
        raise ValueError('actual bootstrap differs from accepted parent provenance')
    envfile = pathlib.Path('/etc/chickadee/runner.env')
    environment = envfile.read_bytes()
    masks = [path for path in pathlib.Path('/etc/systemd/system').rglob('*') if path.is_symlink() and path.readlink() == pathlib.Path('/dev/null')]
    modules = pathlib.Path('/etc/initramfs-tools/modules')
    modules_before = modules.read_bytes()
    debs = []
    for row in plan['packages']:
        if not re.fullmatch(r'[A-Za-z0-9.+_~%:-]+\.deb', row['filename']):
            raise ValueError('invalid deb basename')
        path = cache / row['filename']
        if path.is_symlink() or not path.is_file() or path.stat().st_size != row['size'] or digest(path) != row['sha256']:
            raise ValueError('offline deb hash/size mismatch')
        fields = subprocess.check_output(['dpkg-deb', '-f', path, 'Package', 'Version', 'Architecture'], text=True)
        values = dict(line.split(': ', 1) for line in fields.splitlines())
        if values != {'Package': row['name'], 'Version': row['version'], 'Architecture': row['architecture']}:
            raise ValueError('deb control identity mismatch')
        if row['name'] in before:
            subprocess.run(['dpkg', '--compare-versions', row['version'], 'ge', before[row['name']][0]], check=True)
        debs.append(str(path))
    if len(set(r['name'] for r in plan['packages'])) != len(debs) or not debs:
        raise ValueError('empty or duplicate transaction')
    # APT reads the actual installed dpkg state plus only the provided local debs.
    # No ambient repositories/lists, recommendations, downloads or removals.
    lists = cache.parent / 'empty-apt-lists'
    lists.mkdir()
    common = ['apt-get', '-o', 'Dir::Etc::sourcelist=/dev/null', '-o', 'Dir::Etc::sourceparts=-', '-o', 'Dir::State::Lists=' + str(lists), '-o', 'Dpkg::Use-Pty=0', '--no-remove', '--no-install-recommends', '--no-download']
    env = dict(os.environ, LC_ALL='C', DEBIAN_FRONTEND='noninteractive', NEEDRESTART_MODE='l')
    simulation = subprocess.check_output(common + ['--simulate', 'install', *debs], text=True, env=env)
    validate_plan_output(simulation, plan)
    report.with_suffix('.apt-plan.txt').write_text(simulation)
    # Preserve any existing image-owned service-start policy byte-for-byte.
    policy = pathlib.Path('/usr/sbin/policy-rc.d')
    backup = policy.with_name('policy-rc.d.chickadee-os-parent')
    if backup.exists() or backup.is_symlink():
        raise ValueError('unexpected service policy backup')
    had_policy = policy.exists() or policy.is_symlink()
    if had_policy:
        policy.rename(backup)
    try:
        policy.write_text('#!/bin/sh\nexit 101\n')
        policy.chmod(0o755)
        subprocess.run(common + ['-y', '-o', 'Dpkg::Options::=--force-confold', 'install', *debs], check=True, env=env)
        audit = subprocess.run(['dpkg', '--audit'], check=True, env=env, capture_output=True, text=True)
        if audit.stdout.strip() or audit.stderr.strip():
            raise ValueError('dpkg audit reported an incomplete transaction')
        kernel = plan['kernel']
        if not re.fullmatch(r'[0-9]+\.[0-9]+\.[0-9]+-[0-9]+-generic', kernel):
            raise ValueError('invalid locked kernel ABI')
        validate_kernel_config(pathlib.Path('/boot/config-' + kernel).read_text(), plan['family'])
        subprocess.run(['update-initramfs', '-u', '-k', kernel], check=True, env=env)
        for path in [pathlib.Path('/boot/vmlinuz-' + kernel), pathlib.Path('/boot/initrd.img-' + kernel), pathlib.Path('/lib/modules/' + kernel)]:
            if not path.exists():
                raise ValueError('new generic kernel/initrd/modules missing')
        after = inventory()
        verify_after(before, after, plan)
        if any(not path.is_symlink() or path.readlink() != pathlib.Path('/dev/null') for path in masks) or modules.read_bytes() != modules_before:
            raise ValueError('existing image service masks/initramfs module configuration changed')
        if digest(bootstrap) != bootstrap_hash or envfile.read_bytes() != environment:
            raise ValueError('bootstrap/runner trust environment changed')
        report.write_text(json.dumps({'schema': 1, 'family': plan['family'], 'kernel': kernel, 'snapshot': plan['snapshot'], 'packages': plan['packages'], 'native_apt_exact_plan': True, 'unaffected_packages_preserved': len(before) - sum(r['name'] in before for r in plan['packages']), 'bootstrap_sha256': bootstrap_hash, 'runner_environment_sha256': hashlib.sha256(environment).hexdigest(), 'retained_masked_units': [str(path) for path in masks], 'initramfs_modules_sha256': hashlib.sha256(modules_before).hexdigest(), 'kernel_sha256': digest(pathlib.Path('/boot/vmlinuz-' + kernel)), 'initrd_sha256': digest(pathlib.Path('/boot/initrd.img-' + kernel)), 'acceptance': 'PENDING; cold profile boot, actual trust/runtime clients and real-job acceptance required'}, indent=2) + '\n')
    finally:
        policy.unlink()
        if had_policy:
            backup.rename(policy)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--plan', type=pathlib.Path, required=True)
    parser.add_argument('--cache', type=pathlib.Path, required=True)
    parser.add_argument('--report', type=pathlib.Path, required=True)
    parser.add_argument('--expected-bootstrap', required=True)
    args = parser.parse_args()
    install(json.loads(args.plan.read_text()), args.cache, args.report, args.expected_bootstrap)


if __name__ == '__main__':
    main()
