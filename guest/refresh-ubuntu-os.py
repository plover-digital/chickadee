#!/usr/bin/env python3
"""Install only an exact offline native Ubuntu transaction in a disposable copy."""
import argparse
import hashlib
import json
import os
import pathlib
import re
import shutil
import subprocess
import sys


DPKG_QUERY = ['dpkg-query', '-W', '-f=${db:Status-Abbrev}\t${binary:Package}\t${Version}\t${Architecture}\n']


def parse_inventory(text):
    if not text.strip():
        raise ValueError('empty dpkg query output')
    result = {}
    for line in text.splitlines():
        status, name, version, arch = line.split('\t')
        if not name or status == 'rc ' and (not version or not arch):
            raise ValueError('malformed dpkg identity')
        # Explicitly clean non-installed records. rc retains configuration only;
        # none can require APT/dpkg configuration outside the finite install set.
        if status in ('rc ', 'un ', 'rn ', 'pn '):
            continue
        if status != 'ii ':
            raise ValueError('incomplete, error, held or unsupported dpkg state: ' + name + ' ' + repr(status))
        if not name or not version or not arch:
            raise ValueError('malformed installed dpkg identity')
        name = name.split(':')[0]
        if name in result:
            raise ValueError('multiarch inventory unsupported')
        result[name] = (version, arch)
    return result


def inventory():
    return parse_inventory(subprocess.check_output(DPKG_QUERY, text=True))


def pretransaction_inventory(report):
    evidence = {'accepted': False, 'commands': []}
    try:
        outputs = []
        for argv in [DPKG_QUERY, ['dpkg', '--audit']]:
            result = subprocess.run(argv, capture_output=True, text=True,
                                    env=dict(os.environ, LC_ALL='C'))
            evidence['commands'].append({'argv': argv, 'exit': result.returncode,
                                         'stdout': result.stdout, 'stderr': result.stderr})
            if result.returncode:
                raise subprocess.CalledProcessError(result.returncode, argv,
                                                    output=result.stdout, stderr=result.stderr)
            outputs.append(result.stdout)
            if result.stderr.strip():
                raise ValueError('pretransaction dpkg query/audit diagnostic')
        if outputs[1].strip():
            raise ValueError('pretransaction dpkg audit reported an incomplete state')
        actual = parse_inventory(outputs[0])
        evidence['accepted'] = True
        return actual
    except Exception as error:
        evidence['error'] = str(error)
        raise
    finally:
        if not evidence['accepted']:
            print(json.dumps(evidence), file=sys.stderr)
        report.write_text(json.dumps(evidence, indent=2) + '\n')


def expected_before(plan):
    rows = plan['parent_packages']
    result = {r['name'].split(':')[0]: (r['version'], r['architecture']) for r in rows}
    if len(result) != len(rows):
        raise ValueError('duplicate/multiarch parent packages')
    return result


def validate_plan_output(output, plan):
    actions = {'Inst': {}, 'Conf': {}}
    for line in output.splitlines():
        if line.startswith(('Remv ', 'Purg ')):
            raise ValueError('native APT proposed removal/purge')
        for action, actual in actions.items():
            if not line.startswith(action + ' '):
                continue
            # APT Describe emits version + space + RelStr, whose descriptive
            # field may be empty. ShortBreaks appends bracketed diagnostics.
            match = re.fullmatch(action + r' ([^ ]+)(?: \[[^]]+\])? \(([^ ]+) [^()\r\n]*\)(?: \[[a-z0-9+.: -]*\])*', line)
            if not match:
                raise ValueError('unrecognized native APT transaction row: ' + repr(line))
            name, version = match.groups()
            name = name.split(':')[0]
            if name in actual:
                raise ValueError('duplicate native APT action row')
            actual[name] = version
    expected = {r['name']: r['version'] for r in plan['packages']}
    if any(actual != expected for actual in actions.values()):
        raise ValueError('native APT install/configure transaction differs from finite prepared lock')
    return actions['Inst']


def simulate_and_validate(argv, plan, report, env):
    result = subprocess.run(argv, capture_output=True, text=True, env=env)
    evidence = {'argv': argv, 'exit': result.returncode, 'stdout': result.stdout,
                'stderr': result.stderr, 'accepted': False}
    try:
        # Persist execution output before either return-code or syntax checks.
        # On failure also journal it: recovering a partial guest needs a new GO.
        report.with_suffix('.apt-plan.txt').write_text(result.stdout)
        report.with_suffix('.apt-simulation.json').write_text(json.dumps(evidence, indent=2) + '\n')
        if result.returncode:
            raise subprocess.CalledProcessError(result.returncode, argv,
                                                output=result.stdout, stderr=result.stderr)
        validate_plan_output(result.stdout, plan)
        evidence['accepted'] = True
        return result.stdout
    except Exception as error:
        evidence['error'] = str(error)
        print(json.dumps(evidence), file=sys.stderr)
        raise
    finally:
        report.with_suffix('.apt-simulation.json').write_text(json.dumps(evidence, indent=2) + '\n')


def journal_install_failure(evidence, record_limit=16384, total_limit=262144):
    encoded = json.dumps(evidence)

    def serialize(value):
        return json.dumps(value, ensure_ascii=True) + '\n'

    def header(truncated):
        return serialize({'native_apt_install_failure_bytes': len(encoded.encode('utf-8')),
                          'journal_limit': total_limit, 'truncated': truncated})

    # Reserve the larger possible FINAL header before allocating any chunks.
    # Each candidate includes its JSON escaping, metadata and terminating newline.
    remaining = total_limit - max(len(header(t).encode('utf-8')) for t in [True, False])
    chunks = []
    offset = 0
    while offset < len(encoded):
        allowance = min(record_limit, remaining)
        low, high = 0, min(len(encoded) - offset, allowance)
        while low < high:
            count = (low + high + 1) // 2
            candidate = serialize({'native_apt_install_failure_chunk': len(chunks),
                                   'data': encoded[offset:offset + count]})
            if len(candidate.encode('utf-8')) <= allowance:
                low = count
            else:
                high = count - 1
        if low == 0:
            break
        record = serialize({'native_apt_install_failure_chunk': len(chunks),
                            'data': encoded[offset:offset + low]})
        chunks.append(record)
        remaining -= len(record.encode('utf-8'))
        offset += low
    records = [header(offset < len(encoded)), *chunks]
    sizes = [len(record.encode('utf-8')) for record in records]
    if any(size > record_limit for size in sizes) or sum(sizes) > total_limit:
        raise ValueError('APT journal limits too small for metadata')
    for record in records:
        sys.stderr.write(record)


def install_and_record(argv, report, env):
    result = subprocess.run(argv, capture_output=True, text=True, env=env)
    evidence = {'argv': argv, 'exit': result.returncode, 'stdout': result.stdout,
                'stderr': result.stderr}
    report.with_suffix('.apt-install.json').write_text(json.dumps(evidence, indent=2) + '\n')
    if result.returncode:
        # Full streams remain in the guest report; bounded chunks survive in
        # the journal without relying on another appliance to inspect a failure.
        journal_install_failure(evidence)
        raise subprocess.CalledProcessError(result.returncode, argv,
                                            output=result.stdout, stderr=result.stderr)


def canonical_archive_name(row):
    # APT 3.2.0 pkgAcqArchive uses QuoteString(..., "_:") (arch: "_:.").
    def quote(value, bad):
        return ''.join('%%%02x' % ord(c) if c in bad or c == '%' or
                       not 0x20 < ord(c) < 0x7f else c for c in value)
    return '_'.join(quote(row[key], bad) for key, bad in
                    [('name', '_:'), ('version', '_:'), ('architecture', '_:.')]) + '.deb'


def offline_apt_arguments(cache, plan):
    if not cache.is_absolute() or cache.is_symlink() or not cache.is_dir():
        raise ValueError('absolute regular offline cache directory required')
    archives = cache.parent / 'verified-apt-archives'
    archives.mkdir(mode=0o700)  # Fresh transaction only; never reuse ambient cache.
    (archives / 'partial').mkdir(mode=0o700)
    names = set()
    for row in plan['packages']:
        if not re.fullmatch(r'[a-z0-9][a-z0-9+.-]*', row['name']) or not re.fullmatch(r'[A-Za-z0-9.+:~%-]+', row['version']) or not re.fullmatch(r'[a-z0-9-]+', row['architecture']) or not re.fullmatch(r'[A-Za-z0-9.+_~%:-]+\.deb', row['filename']):
            raise ValueError('invalid offline archive identity')
        name = canonical_archive_name(row)
        if '/' in name or name in names:
            raise ValueError('invalid or duplicate canonical archive name')
        names.add(name)
        source = cache / row['filename']
        if source.is_symlink() or not source.is_file() or source.stat().st_size != row['size'] or digest(source) != row['sha256']:
            raise ValueError('offline archive changed before staging')
        target = archives / name
        shutil.copyfile(source, target)
        if target.stat().st_size != row['size'] or digest(target) != row['sha256']:
            raise ValueError('staged archive hash/size mismatch')
    lists = cache.parent / 'empty-apt-lists'
    lists.mkdir()
    sources = cache.parent / 'empty-apt-sources'
    sources.mkdir()
    source_list = cache.parent / 'empty-apt-sources.list'
    source_list.write_text('')
    return ['apt-get', '-o', 'Dir::Etc::sourcelist=' + str(source_list),
            '-o', 'Dir::Etc::sourceparts=' + str(sources),
            '-o', 'Dir::State::Lists=' + str(lists),
            '-o', 'Dir::Cache::archives=' + str(archives),
            '-o', 'Dir::Cache::pkgcache=', '-o', 'Dir::Cache::srcpkgcache=',
            '-o', 'Dpkg::Use-Pty=0', '--no-remove',
            '--no-install-recommends', '--no-download']


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
    before = pretransaction_inventory(report.with_suffix('.dpkg-preflight.json'))
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
    common = offline_apt_arguments(cache, plan)
    env = dict(os.environ, LC_ALL='C', DEBIAN_FRONTEND='noninteractive', NEEDRESTART_MODE='l')
    simulate_and_validate(common + ['--simulate', 'install', *debs], plan, report, env)
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
        install_and_record(common + ['-y', '-o', 'Dpkg::Options::=--force-confold', 'install', *debs], report, env)
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
