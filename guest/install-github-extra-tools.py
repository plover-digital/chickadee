#!/usr/bin/env python3
"""Download verified inputs on the builder, or install them in an offline guest.

Use --download DIR outside the guest, then --install DIR inside its mounted
Ubuntu 24.04 root. The install command is not intended for the deployment host.
"""
import argparse
import hashlib
import json
import pathlib
import subprocess
import tarfile
import urllib.request


def verify(path, tool):
    with path.open('rb') as data:
        digest = hashlib.file_digest(data, tool['hash_algorithm']).hexdigest()
    if digest != tool['digest']:
        raise ValueError('checksum mismatch: ' + tool['filename'])


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--lock', type=pathlib.Path, required=True)
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument('--download', type=pathlib.Path)
    mode.add_argument('--install', type=pathlib.Path)
    args = parser.parse_args()
    lock = json.loads(args.lock.read_text())
    if lock['schema'] != 1:
        raise ValueError('unsupported tool lock')
    for tool in lock['tools']:
        if pathlib.Path(tool['filename']).name != tool['filename'] or tool['hash_algorithm'] not in ('sha256', 'sha512') or not tool['url'].startswith('https://'):
            raise ValueError('invalid tool input')
    if args.download:
        args.download.mkdir(parents=True, exist_ok=True)
        for tool in lock['tools']:
            archive = args.download / tool['filename']
            if archive.exists():
                verify(archive, tool)
                continue
            temporary = archive.with_suffix(archive.suffix + '.partial')
            try:
                with urllib.request.urlopen(tool['url'], timeout=120) as src, temporary.open('wb') as dest:
                    while chunk := src.read(1024 * 1024):
                        dest.write(chunk)
                verify(temporary, tool)
                temporary.replace(archive)
            finally:
                temporary.unlink(missing_ok=True)
        return
    subprocess.run(['bash', '-c', '. /etc/os-release; test "$ID:$VERSION_ID" = ubuntu:24.04'], check=True)
    # Check every input before changing the image, including archives already cached.
    for tool in lock['tools']:
        verify(args.install / tool['filename'], tool)
    dotnet = pathlib.Path('/usr/share/dotnet')
    for tool in sorted(lock['tools'], key=lambda t: (t['name'], tuple(int(v) for v in t['version'].split('.')))):
        archive = args.install / tool['filename']
        if tool['name'] == 'chrome':
            subprocess.run(['dpkg', '-i', str(archive)], check=True)
            # Updates happen through versioned image rebuilds, not live guest apt repos.
            pathlib.Path('/etc/apt/sources.list.d/google-chrome.list').unlink(missing_ok=True)
            pathlib.Path('/etc/cron.daily/google-chrome').unlink(missing_ok=True)
        elif tool['name'] == 'dotnet':
            dotnet.mkdir(parents=True, exist_ok=True)
            with tarfile.open(archive) as tar:
                tar.extractall(dotnet, filter='data')
        elif tool['name'] == 'geckodriver':
            destination = pathlib.Path('/usr/local/share/gecko_driver')
            destination.mkdir(parents=True, exist_ok=True)
            with tarfile.open(archive) as tar:
                tar.extractall(destination, filter='data')
            symlink('/usr/local/bin/geckodriver', str(destination / 'geckodriver'))
        elif tool['name'] == 'firefox':
            with tarfile.open(archive) as tar:
                tar.extractall('/opt', filter='data')
            symlink('/usr/local/bin/firefox', '/opt/firefox/firefox')
        elif tool['name'] == 'rust':
            staging = pathlib.Path('/tmp/chickadee-rust-installer')
            staging.mkdir(exist_ok=True)
            with tarfile.open(archive) as tar:
                tar.extractall(staging, filter='data')
            installer = staging / ('rust-' + tool['version'] + '-x86_64-unknown-linux-gnu')
            subprocess.run(['bash', str(installer / 'install.sh'), '--prefix=/opt/rust', '--without=rust-docs', '--disable-ldconfig'], check=True)
            for binary in ('cargo', 'rustc', 'rustdoc', 'rustfmt', 'cargo-fmt', 'clippy-driver', 'cargo-clippy'):
                if (pathlib.Path('/opt/rust/bin') / binary).exists():
                    symlink('/usr/local/bin/' + binary, '/opt/rust/bin/' + binary)
            import shutil
            shutil.rmtree(staging)
        elif tool['name'] == 'rustup':
            archive.chmod(0o755)
            environment = dict(__import__('os').environ, CARGO_HOME='/opt/cargo', RUSTUP_HOME='/opt/rustup')
            subprocess.run([str(archive), '-y', '--no-modify-path', '--default-toolchain', 'none'], env=environment, check=True)
            subprocess.run(['/opt/cargo/bin/rustup', 'toolchain', 'link', 'chickadee', '/opt/rust'], env=environment, check=True)
            subprocess.run(['/opt/cargo/bin/rustup', 'default', 'chickadee'], env=environment, check=True)
            subprocess.run(['/opt/cargo/bin/rustup', 'set', 'auto-self-update', 'disable'], env=environment, check=True)
            toolchains = pathlib.Path('/opt/rustup/toolchains')
            for alias in ('stable-x86_64-unknown-linux-gnu', '1.98.1-x86_64-unknown-linux-gnu'):
                target = toolchains / alias
                if target.exists() or target.is_symlink():
                    raise ValueError('unexpected preexisting Rust toolchain alias')
                target.symlink_to('/opt/rust')
            offline = dict(environment, RUSTUP_DIST_SERVER='http://127.0.0.1:9', RUSTUP_UPDATE_ROOT='http://127.0.0.1:9')
            for alias in ('stable', '1.98.1'):
                result = subprocess.run(['/opt/cargo/bin/rustup', 'run', alias, 'rustc', '--version'], env=offline, check=True, capture_output=True, text=True)
                if not result.stdout.startswith('rustc 1.98.1 '):
                    raise ValueError('unexpected offline Rust alias version')
            subprocess.run(['chown', '-R', 'runner:runner', '/opt/cargo', '/opt/rustup'], check=True)
            for binary in ('rustup', 'cargo', 'rustc', 'rustdoc', 'rustfmt', 'cargo-fmt', 'clippy-driver', 'cargo-clippy'):
                proxy = pathlib.Path('/opt/cargo/bin') / binary
                if proxy.exists():
                    symlink('/usr/local/bin/' + binary, str(proxy))
        else:
            raise ValueError('unsupported tool')
    if dotnet.exists():
        symlink('/usr/local/bin/dotnet', '/usr/share/dotnet/dotnet')
        subprocess.run(['/usr/local/bin/dotnet', '--list-sdks'], check=True)
    for command in [['/usr/local/bin/rustc', '--version'], ['/usr/local/bin/cargo', '--version'], ['/usr/local/bin/firefox', '--headless', '--version'], ['/usr/bin/google-chrome', '--version'], ['/usr/local/bin/geckodriver', '--version']]:
        subprocess.run(command, check=True)
    env = pathlib.Path('/etc/chickadee/runner.env')
    with env.open('a') as f:
        f.write('DOTNET_ROOT=/usr/share/dotnet\nDOTNET_MULTILEVEL_LOOKUP=0\nDOTNET_NOLOGO=1\nDOTNET_CLI_TELEMETRY_OPTOUT=1\nCARGO_HOME=/opt/cargo\nRUSTUP_HOME=/opt/rustup\nGECKOWEBDRIVER=/usr/local/share/gecko_driver\n')
    report = pathlib.Path('/etc/chickadee/image-compatibility.json')
    data = json.loads(report.read_text()) if report.exists() else {}
    data['extra_tools'] = lock['tools']
    if 'remaining' in data:
        if 'Rust/Swift/Julia/Kotlin/Haskell' in data['remaining']:
            data['remaining'].extend(['Swift', 'Julia', 'Kotlin', 'Haskell'])
        data['remaining'] = [gap for gap in data['remaining'] if gap not in ('dotnet SDK matrix', 'Rust toolchain', 'Rust/Swift/Julia/Kotlin/Haskell', 'Google Chrome/Edge/Firefox and drivers')]
    data['remaining_extra_tools'] = ['Edge and Chrome/Edge browser drivers', 'GitHub baseline Chrome version (pinned current Google release installed)']
    report.write_text(json.dumps(data, indent=2) + '\n')


def symlink(destination, source):
    path = pathlib.Path(destination)
    path.unlink(missing_ok=True)
    path.symlink_to(source)


if __name__ == '__main__':
    main()
