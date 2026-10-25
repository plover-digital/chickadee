#!/usr/bin/env python3
"""Replace official runner and Node caches in a private, offline Linux image."""
import argparse
import hashlib
import importlib.util
import json
import os
import pathlib
import pwd
import shutil
import subprocess
import tarfile


def verify(directory, entries):
    for entry in entries:
        name = entry['filename']
        if pathlib.Path(name).name != name:
            raise ValueError('invalid supplemental basename')
        with (directory / name).open('rb') as data:
            if hashlib.file_digest(data, 'sha256').hexdigest() != entry['sha256']:
                raise ValueError('runtime input checksum mismatch: ' + name)


def safe_root(path):
    if path.is_symlink():
        raise ValueError('managed runtime root must not be a symlink: ' + str(path))


def require_tuple(text, rocky):
    entries = [line.split('=', 1) for line in text.splitlines() if line and not line.startswith('#')]
    if any(len(e) != 2 for e in entries) or len({e[0] for e in entries}) != len(entries):
        raise ValueError('invalid or duplicate runner environment entry')
    values = dict(entries)
    expected = {'NODE_OPTIONS': '--use-openssl-ca',
                'SSL_CERT_FILE': '/etc/pki/tls/certs/ca-bundle.crt' if rocky else '/etc/ssl/certs/ca-certificates.crt',
                'SSL_CERT_DIR': '/etc/pki/tls/certs' if rocky else '/etc/ssl/certs'}
    if any(values.get(key) != value for key, value in expected.items()):
        raise ValueError('runtime update requires the accepted vendor CA tuple first')


def install(directory, runner, lock, expected_bootstrap=None):
    if os.geteuid() != 0:
        raise ValueError('root in offline disposable image required')
    rocky = pathlib.Path('/etc/rocky-release').exists()
    require_tuple(pathlib.Path('/etc/chickadee/runner.env').read_text(), rocky)
    bootstrap = pathlib.Path('/usr/local/bin/chickadee-guest')
    with bootstrap.open('rb') as stream:
        bootstrap_sha256 = hashlib.file_digest(stream, 'sha256').hexdigest()
    if expected_bootstrap is not None and bootstrap_sha256 != expected_bootstrap:
        raise ValueError('actual retained bootstrap differs from accepted parent provenance')
    target = pathlib.Path('/opt/actions-runner')
    safe_root(target)
    # Accepted immutable input must be unconfigured, never a copy of a registered runner.
    if any(target.glob('.credentials*')) or (target / '.runner').exists() or (target / '.runner').is_symlink():
        raise ValueError('refusing configured runner input')
    staging = target.with_name('actions-runner.security-new')
    if staging.exists():
        raise ValueError('unexpected previous runner staging directory')
    staging.mkdir()
    with tarfile.open(directory / runner['filename']) as archive:
        archive.extractall(staging, filter='data')
    if (staging / 'bin/Runner.Listener').is_file() is False or (staging / 'externals/node24/bin/node').is_file() is False:
        raise ValueError('incomplete official runner archive')
    shutil.rmtree(target)
    staging.rename(target)
    rocky = pathlib.Path('/etc/rocky-release').exists()
    if not rocky:
        release = dict(line.split('=', 1) for line in pathlib.Path('/etc/os-release').read_text().splitlines() if '=' in line)
        platform = 'linux-' + release['VERSION_ID'].strip(chr(34)) + '-x64'
        if any(t['platform'] != platform for t in lock['tools'] if t['name'] == 'python'):
            raise ValueError('Python archive ABI does not match image OS')
    managed = {'node'} if rocky else {'node', 'python'}
    for name in managed:
        root = pathlib.Path('/opt/hostedtoolcache') / ('Python' if name == 'python' else name)
        safe_root(root)
        if root.exists():
            shutil.rmtree(root)
    cache = pathlib.Path('/opt/hostedtoolcache/node')
    for tool in lock['tools']:
        if tool['name'] not in managed:
            continue
        root = pathlib.Path('/opt/hostedtoolcache') / ('Python' if tool['name'] == 'python' else tool['name'])
        destination = root / tool['version'] / 'x64'
        destination.mkdir(parents=True)
        with tarfile.open(directory / tool['filename']) as archive:
            archive.extractall(destination, filter='data')
        (destination / 'setup.sh').unlink(missing_ok=True)
        if tool['name'] == 'python':
            binary = 'python' + '.'.join(tool['version'].split('.')[:2])
            for alias in ['python', 'python3', binary.replace('.', '')]:
                path = destination / 'bin' / alias
                if not path.exists():
                    path.symlink_to(binary)
            if not (destination / 'python').exists():
                (destination / 'python').symlink_to('bin/' + binary)
        (destination.parent / 'x64.complete').touch()
    default = lock.get('default_node', '22.23.4')
    for command in ['node', 'npm', 'npx']:
        source = cache / default / 'x64/bin' / command
        if not source.is_file():
            raise ValueError('missing default Node executable: ' + command)
        alias = pathlib.Path('/usr/local/bin') / command
        alias.unlink(missing_ok=True)
        alias.symlink_to(source)
    owner = pwd.getpwnam('runner')
    subprocess.run(['chown', '-R', f'{owner.pw_uid}:{owner.pw_gid}', str(target), str(cache), *([] if rocky else ['/opt/hostedtoolcache/Python'])], check=True)
    report = {'schema': 1, 'runner': runner, 'node_toolcache': [t for t in lock['tools'] if t['name'] == 'node'], 'default_node': default, 'python_toolcache': [] if rocky else [t for t in lock['tools'] if t['name'] == 'python'],
              'bootstrap_sha256': bootstrap_sha256,
              'scope': 'official full runner and Node caches; vendor EOL Node20 retained, no runtime/Listener acceptance implied'}
    pathlib.Path('/etc/chickadee/runtime-security.json').write_text(json.dumps(report, indent=2) + '\n')
    inventory = pathlib.Path('/etc/chickadee/image-compatibility.json')
    if inventory.exists():
        previous = inventory.with_name('pre-runtime-image-compatibility.json')
        if previous.exists():
            raise ValueError('previous guest runtime inventory already exists')
        previous.write_bytes(inventory.read_bytes())
        data = json.loads(inventory.read_text())
        if 'installed_toolcache' in data:
            data['installed_toolcache'] = [t for t in data['installed_toolcache'] if t['name'] not in managed] + [t for t in lock['tools'] if t['name'] in managed]
        data['default_node'] = default
        inventory.write_text(json.dumps(data, indent=2) + '\n')


def load(name):
    spec = importlib.util.spec_from_file_location(name, pathlib.Path(__file__).with_name(name + '.py'))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def supplemental(directory, trust_policy, nss_policy, rocky_policy, extra_lock, install_now=False):
    trust = load('refresh-ca-trust')
    for entry in trust_policy['python_wheels']:
        trust.check_file(directory / entry['filename'], entry)
    nss = load('refresh-nss-security')
    nss.verify(directory, nss_policy)
    verify(directory, [rocky_policy['package']])
    extra = load('install-github-extra-tools')
    if extra_lock:
        for entry in extra_lock['tools']:
            extra.verify(directory / entry['filename'], entry)
    if not install_now:
        return
    rocky = pathlib.Path('/etc/rocky-release').exists()
    if rocky:
        subprocess.run(['rpm', '-Uvh', '--replacepkgs', str(directory / rocky_policy['package']['filename'])], check=True)
        subprocess.run(['update-ca-trust', 'extract'], check=True)
    else:
        nss.install(directory, nss_policy)
    if pathlib.Path('/var/lib/dpkg/info/ca-certificates-java.postinst').is_file():
        trust.complete_java_triggers()
    ca_file = pathlib.Path('/etc/pki/tls/certs/ca-bundle.crt' if rocky else '/etc/ssl/certs/ca-certificates.crt')
    if trust.blocked_certificates(ca_file.read_bytes(), trust_policy):
        raise ValueError('official system bundle still trusts an incident key')
    report = {'java_stores': trust.refresh_java_stores(trust_policy), 'system_ca_file': str(ca_file), 'system_ca_sha256': hashlib.sha256(ca_file.read_bytes()).hexdigest(), 'policy': trust_policy}
    pythons, updates = trust.refresh_python_stores(trust_policy, directory, 'rocky' if rocky else 'ubuntu')
    report.update(python_interpreters=[str(p) for p in pythons], ensurepip_wheel_ca_updates=updates)
    pathlib.Path('/etc/chickadee/runtime-trust.json').write_text(json.dumps(report, indent=2) + '\n')


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--runner', type=pathlib.Path, required=True)
    parser.add_argument('--lock', type=pathlib.Path, required=True)
    parser.add_argument('--install', action='store_true')
    parser.add_argument('--trust-policy', type=pathlib.Path)
    parser.add_argument('--nss-policy', type=pathlib.Path)
    parser.add_argument('--rocky-policy', type=pathlib.Path)
    parser.add_argument('--extra-lock', type=pathlib.Path)
    parser.add_argument('--expected-bootstrap-sha256')
    parser.add_argument('directory', type=pathlib.Path)
    args = parser.parse_args()
    runner, lock = [json.loads(p.read_text()) for p in [args.runner, args.lock]]
    verify(args.directory, [runner, *[t for t in lock['tools'] if t['name'] in {'node', 'python'}]])
    policies = [args.trust_policy, args.nss_policy, args.rocky_policy]
    if any(policies) and not all(policies):
        raise ValueError('complete supplemental trust policies required')
    extra = json.loads(args.extra_lock.read_text()) if args.extra_lock else None
    if all(policies):
        supplemental(args.directory, *[json.loads(p.read_text()) for p in policies], extra)
    if args.install:
        install(args.directory, runner, lock, args.expected_bootstrap_sha256)
        if all(policies):
            supplemental(args.directory, *[json.loads(p.read_text()) for p in policies], extra, install_now=True)
        if args.extra_lock:
            subprocess.run(['python3', str(pathlib.Path(__file__).with_name('install-github-extra-tools.py')), '--lock', str(args.extra_lock), '--install', str(args.directory), '--replace-installed'], check=True)


if __name__ == '__main__':
    main()
