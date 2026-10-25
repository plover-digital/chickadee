#!/usr/bin/env python3
"""Actual supported Linux runtime inventory; chain/browser/Listener tests are separate."""
import hashlib
import json
import pathlib
import subprocess

ROOT = pathlib.Path(__file__).resolve().parents[1]


def require_node_versions(actual):
    if set(actual) != {'22.23.4', '24.22.1'}:
        raise ValueError('superseded or missing supported Node cache')


def require_dotnet(actual_sdks, actual_runtimes, lock):
    expected = {t['version'] for t in lock['tools'] if t['name'] == 'dotnet'}
    if set(actual_sdks) != expected:
        raise ValueError('superseded or missing .NET SDK')
    if set(actual_runtimes) != {'8.0.32', '9.0.21', '10.0.13'}:
        raise ValueError('superseded or missing .NET shared runtime')


def main():
    runner = json.loads((ROOT / 'guest/images/runner-linux-x64.json').read_text())
    runner_root = pathlib.Path('/opt/actions-runner')
    for name, expected in runner['files_sha256'].items():
        with (runner_root / name).open('rb') as stream:
            if hashlib.file_digest(stream, 'sha256').hexdigest() != expected:
                raise ValueError('runner official payload identity mismatch: ' + name)
    nodes = sorted(p.parent.name for p in pathlib.Path('/opt/hostedtoolcache/node').glob('*/x64.complete'))
    require_node_versions(nodes)
    for version in nodes:
        output = json.loads(subprocess.check_output([f'/opt/hostedtoolcache/node/{version}/x64/bin/node', '-e', 'console.log(JSON.stringify({version:process.version,options:process.env.NODE_OPTIONS,file:process.env.SSL_CERT_FILE,dir:process.env.SSL_CERT_DIR}))'], text=True))
        rocky = pathlib.Path('/etc/rocky-release').exists()
        expected = {'version': 'v' + version, 'options': '--use-openssl-ca', 'file': '/etc/pki/tls/certs/ca-bundle.crt' if rocky else '/etc/ssl/certs/ca-certificates.crt', 'dir': '/etc/pki/tls/certs' if rocky else '/etc/ssl/certs'}
        if output != expected:
            raise ValueError('actual Node version/inherited trust tuple mismatch')
    report = {'node_caches': nodes, 'runner': runner['version'], 'EOL_vendor_node20_retained': runner['bundled_node20']}
    if not pathlib.Path('/etc/rocky-release').exists():
        expected_python = {'3.10.22', '3.11.17', '3.12.15', '3.13.17', '3.14.9'}
        actual_python = {p.parent.name for p in pathlib.Path('/opt/hostedtoolcache/Python').glob('*/x64.complete')}
        if actual_python != expected_python:
            raise ValueError('superseded or missing Python cache')
        for version in sorted(actual_python):
            output = subprocess.check_output([f'/opt/hostedtoolcache/Python/{version}/x64/bin/python3', '-c', 'import platform; print(platform.python_version())'], text=True).strip()
            if output != version:
                raise ValueError('actual Python version mismatch')
        report['python_caches'] = sorted(actual_python)
        sdks = subprocess.check_output(['dotnet', '--list-sdks'], text=True)
        runtimes = subprocess.check_output(['dotnet', '--list-runtimes'], text=True)
        lock = json.loads((ROOT / 'guest/images/github-extra-tools.json').read_text())
        require_dotnet([line.split()[0] for line in sdks.splitlines()], [line.split()[1] for line in runtimes.splitlines()], lock)
        report.update(dotnet_sdks=sdks, dotnet_shared_runtimes=runtimes)
        policy = json.loads((ROOT / 'guest/images/nss-security.json').read_text())
        os_release = dict(line.split('=', 1) for line in pathlib.Path('/etc/os-release').read_text().splitlines() if '=' in line)
        minimum = policy['releases'][os_release['VERSION_ID'].strip('"')]['minimum_version']
        text = subprocess.check_output(['dpkg-query', '-W', '-f=${Version}', 'libnss3'], text=True)
        subprocess.run(['dpkg', '--compare-versions', text, 'ge', minimum], check=True)
        report['native_nss_version'] = text
    print(json.dumps(report, indent=2))


if __name__ == '__main__':
    main()
