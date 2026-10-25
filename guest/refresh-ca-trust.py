#!/usr/bin/env python3
"""Build-time trust refresh. Run only inside an offline disposable image copy."""
import argparse
import base64
import csv
import io
import zipfile
import hashlib
import json
import os
import pathlib
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile
import ssl
import urllib.request

PEM = re.compile(rb'-----BEGIN CERTIFICATE-----.*?-----END CERTIFICATE-----', re.S)


def run(*args, **kwargs):
    return subprocess.run(args, check=True, **kwargs)


def spki(pem):
    key = run('openssl', 'x509', '-pubkey', '-noout', input=pem, capture_output=True).stdout
    der = run('openssl', 'pkey', '-pubin', '-outform', 'DER', input=key, capture_output=True).stdout
    return hashlib.sha256(der).hexdigest().upper()


def blocked_certificates(bundle, policy):
    blocked = {c['spki_sha256'] for c in policy['roots']}
    return [pem for pem in PEM.findall(bundle) if spki(pem) in blocked]


def check_file(path, entry):
    with path.open('rb') as data:
        if hashlib.file_digest(data, 'sha256').hexdigest() != entry['sha256']:
            raise ValueError('trust input checksum mismatch: ' + path.name)


def download(policy, destination, offline=False):
    if not offline:
        destination.mkdir(parents=True, exist_ok=True)
    for entry in [*policy['python_wheels'], *policy['ubuntu_packages'].values()]:
        name = entry['filename']
        if pathlib.Path(name).name != name or not entry['url'].startswith((
                'https://files.pythonhosted.org/packages/',
                'https://snapshot.ubuntu.com/ubuntu/20261021T000000Z/pool/')):
            raise ValueError('invalid trust input')
        target = destination / name
        if not target.exists() and offline:
            raise ValueError('missing prefetched trust input: ' + name)
        if not target.exists():
            partial = target.with_suffix('.part')
            try:
                with urllib.request.urlopen(entry['url'], timeout=60) as response, partial.open('wb') as output:
                    shutil.copyfileobj(response, output)
                check_file(partial, entry)
                partial.rename(target)
            finally:
                partial.unlink(missing_ok=True)
        check_file(target, entry)


def refresh_bundled_wheel(wheel, ca_data):
    with zipfile.ZipFile(wheel) as archive:
        files = {name: archive.read(name) for name in archive.namelist()}
    targets = [name for name in files if name.endswith('/_vendor/certifi/cacert.pem')]
    if not targets:
        raise ValueError('pip wheel has no vendored CA bundle: ' + str(wheel))
    for name in targets:
        files[name] = ca_data
    record = next(name for name in files if name.endswith('.dist-info/RECORD'))
    output = io.StringIO()
    writer = csv.writer(output, lineterminator='\n')
    for name, data in files.items():
        digest = base64.urlsafe_b64encode(hashlib.sha256(data).digest()).rstrip(b'=').decode()
        writer.writerow([name, '' if name == record else 'sha256=' + digest, '' if name == record else len(data)])
    files[record] = output.getvalue().encode()
    temporary = wheel.with_suffix('.trust-refresh')
    try:
        with zipfile.ZipFile(temporary, 'w', compression=zipfile.ZIP_DEFLATED) as archive:
            for name, data in files.items():
                archive.writestr(name, data)
        temporary.chmod(wheel.stat().st_mode & 0o777)
        temporary.replace(wheel)
    finally:
        temporary.unlink(missing_ok=True)
    return True


def install_rocky_blocklist(policy, blocklist):
    # Use all four hash-pinned primary certificates, independently of any
    # purpose-filtered TLS extraction. This includes the code-signing root.
    certificates = []
    for root in policy['roots']:
        pem = root['pem'].encode()
        if len(PEM.findall(pem)) != 1 or PEM.fullmatch(pem.strip()) is None:
            raise ValueError('expected exactly one primary root certificate')
        der = ssl.PEM_cert_to_DER_cert(pem.decode())
        if hashlib.sha256(der).hexdigest().upper() != root['sha256'] or spki(pem) != root['spki_sha256']:
            raise ValueError('primary root certificate/SPKI checksum mismatch')
        certificates.append((root['spki_sha256'], pem))
    blocklist.mkdir(parents=True, exist_ok=True)
    for key, pem in certificates:
        target = blocklist / ('chickadee-emudhra-' + key.lower() + '.pem')
        if target.exists() and target.read_bytes() != pem:
            raise ValueError('existing trust blocklist differs: ' + str(target))
        target.write_bytes(pem)


def verify_rocky_distrust(policy):
    with tempfile.TemporaryDirectory() as directory:
        output = pathlib.Path(directory) / 'blocklist.pem'
        # No --purpose: code signing and other EKUs must remain in scope.
        run('trust', 'extract', '--overwrite', '--filter=blocklist', '--format=pem-bundle', str(output))
        actual = {spki(pem) for pem in PEM.findall(output.read_bytes())}
    expected = {root['spki_sha256'] for root in policy['roots']}
    if not expected <= actual:
        raise ValueError('purpose-independent p11-kit distrust missing affected root keys')
    return sorted(expected)


def complete_java_triggers():
    """Process only actual pending Java triggers; an installed package is a no-op."""
    output = run('dpkg-query', '-W',
                 '-f=${binary:Package}\t${db:Status-Status}\t${Triggers-Pending}\n',
                 'ca-certificates-java', capture_output=True, text=True).stdout
    lines = output.splitlines()
    if len(lines) != 1 or len(lines[0].split('\t')) != 3:
        raise ValueError('malformed Java package trigger state')
    package, state, pending = lines[0].split('\t')
    if package != 'ca-certificates-java':
        raise ValueError('unexpected Java trigger package identity')
    if state == 'installed' and not pending:
        return False
    if (state != 'triggers-pending' or not pending.split() or
            any(re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9+_.:/-]*', name) is None for name in pending.split())):
        raise ValueError('unexpected Java package trigger state')
    run('dpkg', '--triggers-only', package)
    return True


def java_store_entries(keytool, store):
    """Read aliases and DER identities, independent of localized keytool output."""
    output = run(str(keytool), '-J-Duser.language=en', '-J-Duser.country=US', '-J-Xmx128m',
                 '-list', '-rfc', '-keystore', str(store), '-storepass', 'changeit',
                 capture_output=True).stdout
    entries = {}
    for section in re.split(rb'(?m)^Alias name: ', output)[1:]:
        alias = section.splitlines()[0].decode()
        certificates = PEM.findall(section)
        if len(certificates) != 1 or b'Entry type: trustedCertEntry' not in section:
            raise ValueError('cacerts must contain only single-certificate trusted entries')
        if alias in entries:
            raise ValueError('duplicate Java trust alias')
        pem = certificates[0]
        entries[alias] = {'sha256': hashlib.sha256(ssl.PEM_cert_to_DER_cert(pem.decode())).hexdigest(),
                          'spki_sha256': spki(pem)}
    if not entries:
        raise ValueError('empty or unparseable Java cacerts')
    return entries


def refresh_java_stores(policy, jvm=pathlib.Path('/usr/lib/jvm')):
    """Remove only authenticated incident root keys; preserve other aliases/DER."""
    # Validate the public root identities before considering any keystore edit.
    blocked = set()
    for root in policy['roots']:
        pem = root['pem'].encode()
        if (PEM.fullmatch(pem.strip()) is None or
                hashlib.sha256(ssl.PEM_cert_to_DER_cert(pem.decode())).hexdigest().upper() != root['sha256'] or
                spki(pem) != root['spki_sha256']):
            raise ValueError('primary root certificate/SPKI checksum mismatch')
        blocked.add(root['spki_sha256'])
    stores = {}
    for java in sorted(jvm.glob('*/bin/java')):
        home = java.resolve().parent.parent
        store = (home / 'lib/security/cacerts').resolve(strict=True)
        keytool = home / 'bin/keytool'
        if not keytool.is_file():
            raise ValueError('missing JDK keytool: ' + str(home))
        stores.setdefault(store, {'keytool': keytool, 'homes': set()})['homes'].add(str(home))
    reports = []
    for store, info in stores.items():
        before = java_store_entries(info['keytool'], store)
        retained = {a: c for a, c in before.items() if c['spki_sha256'] not in blocked}
        removed = {a: c for a, c in before.items() if c['spki_sha256'] in blocked}
        if removed:
            # keytool edits a private same-filesystem copy. Never edit a live
            # image or leave a partly updated shared JDK store on an error.
            with tempfile.TemporaryDirectory(dir=store.parent) as directory:
                temporary = pathlib.Path(directory) / 'cacerts'
                shutil.copy2(store, temporary)
                for alias in removed:
                    run(str(info['keytool']), '-J-Xmx128m', '-delete', '-alias', alias, '-keystore',
                        str(temporary), '-storepass', 'changeit')
                if java_store_entries(info['keytool'], temporary) != retained:
                    raise ValueError('Java refresh changed unaffected trust entries')
                os.chown(temporary, store.stat().st_uid, store.stat().st_gid)
                temporary.replace(store)
        if java_store_entries(info['keytool'], store) != retained:
            raise ValueError('Java effective cacerts verification failed')
        reports.append({'path': str(store), 'jdk_homes': sorted(info['homes']),
                        'removed': removed, 'retained_aliases': len(retained),
                        'sha256': hashlib.sha256(store.read_bytes()).hexdigest()})
    return reports


def refresh_python_stores(policy, inputs, distro):
    wheels = {}
    for entry in policy['python_wheels']:
        path = inputs / entry['filename']
        check_file(path, entry)
        wheels[entry['filename'].split('-')[0]] = path
    pythons = [pathlib.Path('/usr/bin/python3')]
    pythons += sorted(pathlib.Path('/opt/hostedtoolcache/Python').glob('*/x64/bin/python3'))
    for python in pythons:
        env = dict(os.environ, PYTHONPATH=str(wheels['pip']), PIP_CONFIG_FILE='/dev/null', PIP_DISABLE_PIP_VERSION_CHECK='1')
        args = []
        if python == pythons[0]:
            args = ['--break-system-packages', '--ignore-installed']
            # Ubuntu's default posix_local scheme already targets /usr/local.
            # Supplying --prefix selects posix_prefix and an unsearched path.
            if distro == 'rocky':
                args += ['--prefix=/usr/local']
        run(str(python), '-m', 'pip', 'install', '--no-index', '--no-deps', '--no-compile', '--upgrade',
            *args, *(str(wheels[name]) for name in ('pip', 'certifi')), env=env)
        expected = {name: path.name.split('-')[1] for name, path in wheels.items()}
        run(str(python), '-c', 'import pip,certifi; assert pip.__version__ == ' + repr(expected['pip']) + '; assert certifi.__version__ == ' + repr(expected['certifi']))
        if python != pythons[0]:
            run('chown', '-R', 'runner:runner', str(python.parent.parent))
        # ensurepip's filename/version constants must stay coherent. Refresh
        # only its vendored CA data using the exact official certifi bundle;
        # RECORD is regenerated, and input/output hashes are recorded below.
    with zipfile.ZipFile(wheels['certifi']) as archive:
        ca_data = archive.read('certifi/cacert.pem')
    wheel_updates = []
    directories = [pathlib.Path('/usr/share/python-wheels')]
    directories += list(pathlib.Path('/usr/lib').glob('python*/ensurepip/_bundled'))
    directories += list(pathlib.Path('/usr/lib64').glob('python*/ensurepip/_bundled'))
    directories += list(pathlib.Path('/opt/hostedtoolcache/Python').glob('*/x64/lib/python*/ensurepip/_bundled'))
    for directory in directories:
        for wheel in directory.glob('pip-*.whl'):
            before = hashlib.sha256(wheel.read_bytes()).hexdigest()
            if refresh_bundled_wheel(wheel, ca_data):
                wheel_updates.append({'path': str(wheel), 'before_sha256': before,
                                      'after_sha256': hashlib.sha256(wheel.read_bytes()).hexdigest()})

    return pythons, wheel_updates


def install(policy, inputs):
    if os.geteuid() != 0:
        raise ValueError('run only as root inside the offline image copy')
    release = dict(line.split('=', 1) for line in pathlib.Path('/etc/os-release').read_text().splitlines() if '=' in line)
    distro, version = (release[k].strip('"') for k in ('ID', 'VERSION_ID'))
    rocky_distrusted_keys = None
    if distro == 'ubuntu':
        package = policy['ubuntu_packages'][version]
        check_file(inputs / package['filename'], package)
        run('dpkg', '--install', str(inputs / package['filename']))
        actual = run('dpkg-query', '-W', '-f=${Version}', 'ca-certificates', capture_output=True, text=True).stdout
        run('dpkg', '--compare-versions', actual, 'ge', policy['ubuntu_ca_versions'][version])
        # Finish the vendor Java trigger rather than assuming the shell hook
        # synchronously rebuilt cacerts. Inspect every actual JDK store below.
        run('update-ca-certificates', '--fresh')
        if pathlib.Path('/var/lib/dpkg/info/ca-certificates-java.postinst').is_file():
            complete_java_triggers()
        ca_file, ca_dir = '/etc/ssl/certs/ca-certificates.crt', '/etc/ssl/certs'
    elif distro == 'rocky' and version == '10.2':
        ca_file, ca_dir = '/etc/pki/tls/certs/ca-bundle.crt', '/etc/pki/tls/certs'
        # Native vendor fix pending: explicitly distrust all four exact
        # primary root certificates across purposes, including code signing.
        install_rocky_blocklist(policy, pathlib.Path('/etc/pki/ca-trust/source/blocklist'))
        run('update-ca-trust', 'extract')
        rocky_distrusted_keys = verify_rocky_distrust(policy)
    else:
        raise ValueError('unsupported image OS')
    if blocked_certificates(pathlib.Path(ca_file).read_bytes(), policy):
        raise ValueError('system CA bundle still trusts an affected root key')

    java_stores = refresh_java_stores(policy)

    pythons, wheel_updates = refresh_python_stores(policy, inputs, distro)

    go_lock = json.loads((inputs / 'go-inputs.json').read_text())
    if go_lock['default_go'] != '1.27.3' or [(t['name'], t['version']) for t in go_lock['tools']] != [('go', '1.26.10'), ('go', '1.27.3')]:
        raise ValueError('combine the complete reviewed Go refresh first')
    for tool in go_lock['tools']:
        check_file(inputs / tool['filename'], tool)
    go_cache = pathlib.Path('/opt/hostedtoolcache/go')
    go_changes = None
    if go_cache.is_dir():
        previous = sorted(p.name for p in go_cache.iterdir() if p.is_dir())
        for tool in go_lock['tools']:
            if tool['name'] != 'go' or tool['version'] not in ('1.26.10', '1.27.3'):
                raise ValueError('unexpected guest Go trust input')
            archive = inputs / tool['filename']
            check_file(archive, tool)
            target = go_cache / tool['version'] / 'x64'
            if target.exists():
                shutil.rmtree(target)
            target.mkdir(parents=True, exist_ok=True)
            with tarfile.open(archive) as tar:
                tar.extractall(target, filter='data')
            (target / 'setup.sh').unlink(missing_ok=True)
            run(str(target / 'bin/go'), 'version')
            (target.parent / 'x64.complete').touch()
            run('chown', '-R', 'runner:runner', str(target.parent))
        retained = {t['version'] for t in go_lock['tools']}
        if retained != {'1.26.10', '1.27.3'} or go_lock['default_go'] != '1.27.3':
            raise ValueError('incomplete supported guest Go refresh')
        for old in go_cache.iterdir():
            if old.is_dir() and old.name not in retained:
                shutil.rmtree(old)
        default = pathlib.Path('/usr/local/bin/go')
        default.unlink(missing_ok=True)
        default.symlink_to(go_cache / go_lock['default_go'] / 'x64/bin/go')
        go_changes = {'previous_versions': previous, 'lock': go_lock}

    # Node's official option replaces bundled roots, unlike NODE_EXTRA_CA_CERTS.
    # Bootstrap validates these exact values and passes them to Runner.Listener
    # and all inherited action/cache Node processes. The built-in list itself
    # stays old until official Node/runner releases are available.
    config = pathlib.Path('/etc/chickadee')
    config.mkdir(exist_ok=True)
    environment = config / 'runner.env'
    values = environment.read_text().splitlines() if environment.exists() else []
    keys = {'NODE_OPTIONS', 'SSL_CERT_FILE', 'SSL_CERT_DIR'}
    values = [line for line in values if line.split('=', 1)[0] not in keys]
    values += ['NODE_OPTIONS=--use-openssl-ca', 'SSL_CERT_FILE=' + ca_file, 'SSL_CERT_DIR=' + ca_dir]
    environment.write_text('\n'.join(values) + '\n')
    environment.chmod(0o644)
    os.chown(environment, 0, 0)
    report = {'schema': 1, 'incident': policy['incident'], 'system_ca_file': ca_file,
              'system_ca_sha256': hashlib.sha256(pathlib.Path(ca_file).read_bytes()).hexdigest(),
              'java_stores': java_stores,
              'purpose_independent_rocky_distrusted_keys': rocky_distrusted_keys, 'go_toolcache': go_changes, 'policy': policy, 'ensurepip_wheel_ca_updates': wheel_updates, 'python_interpreters': [str(p) for p in pythons],
              'node_mitigation': '--use-openssl-ca; built-in roots remain unchanged'}
    (config / 'ca-trust.json').write_text(json.dumps(report, indent=2) + '\n')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--policy', type=pathlib.Path, required=True)
    group = parser.add_mutually_exclusive_group(required=True)
    group.add_argument('--download', type=pathlib.Path)
    group.add_argument('--install', type=pathlib.Path)
    group.add_argument('--verify', type=pathlib.Path)
    group.add_argument('--refresh-java-report', type=pathlib.Path,
                       help='offline private copy only: complete Ubuntu triggers and refresh actual JDK stores')
    args = parser.parse_args()
    policy = json.loads(args.policy.read_text())
    if args.refresh_java_report:
        if os.geteuid() != 0:
            raise ValueError('Java refresh requires root inside an offline private image copy')
        if pathlib.Path('/var/lib/dpkg/info/ca-certificates-java.postinst').is_file():
            complete_java_triggers()
        report = {'schema': 1, 'incident': policy['incident'], 'java_stores': refresh_java_stores(policy)}
        if not report['java_stores']:
            raise ValueError('no JDK found for Java-only refresh')
        args.refresh_java_report.write_text(json.dumps(report, indent=2) + '\n')
    elif args.verify:
        download(policy, args.verify, offline=True)
    elif args.download:
        download(policy, args.download)
    else:
        install(policy, args.install)


if __name__ == '__main__':
    main()
