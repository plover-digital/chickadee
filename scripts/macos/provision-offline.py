#!/usr/bin/env python3
"""Inject build-only files into a trusted, never-credentialed stopped Apple image.

Never use this tool on images that have run customer jobs. It is not a runtime
disk inspection mechanism. All mount operations hold the native helper's lock.
"""
import argparse
import fcntl
import os
from pathlib import Path
import plistlib
import stat
import subprocess
import tempfile


def run(*args):
    return subprocess.check_output(args)


def private(path, directory=False):
    s = path.lstat()
    expected = stat.S_ISDIR if directory else stat.S_ISREG
    if not expected(s.st_mode) or s.st_uid != os.getuid() or s.st_mode & 0o077:
        raise ValueError('Expected owned private file/directory')


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('state', type=Path)
    p.add_argument('xcode', type=Path)
    p.add_argument('--reporter', type=Path, help='Optional Darwin arm64 build-report binary')
    p.add_argument('--skip-xcode-copy', action='store_true', help='Require matching previously copied Xcode version metadata')
    p.add_argument('--trusted-never-credentialed-build-image', action='store_true', required=True)
    a = p.parse_args()
    if os.uname().sysname != 'Darwin':
        p.error('Requires macOS')
    private(a.state, True)
    private(a.state / 'disk.raw')
    if not (a.xcode / 'Contents/Developer').is_dir():
        p.error('Expected expanded Apple-signed Xcode app')
    lockroot = Path.home() / '.local/state/chickadee-macos'
    private(lockroot, True)
    private(lockroot / 'vm.lock')
    fd = os.open(lockroot / 'vm.lock', os.O_RDWR | os.O_NOFOLLOW)
    try:
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            p.error('VM is active; refusing to attach or modify its disk')
        attached = plistlib.loads(run('sudo', '-n', 'hdiutil', 'attach', '-nomount',
            '-imagekey', 'diskimage-class=CRawDiskImage', '-plist', str(a.state / 'disk.raw')))
        entities = attached['system-entities']
        base = next(e['dev-entry'] for e in entities if e.get('content-hint') == 'GUID_partition_scheme')
        devices = {e.get('dev-entry') for e in entities}
        mount = Path(tempfile.mkdtemp(prefix='chickadee-trusted-build-'))
        mounted = False
        try:
            apfs = plistlib.loads(run('diskutil', 'apfs', 'list', '-plist'))
            data = [v['DeviceIdentifier'] for c in apfs['Containers'] for v in c.get('Volumes', [])
                    if v.get('Roles') == ['Data'] and '/dev/' + v['DeviceIdentifier'] in devices]
            if len(data) != 1:
                raise ValueError('Expected exactly one Data volume on attached image')
            run('sudo', '-n', 'diskutil', 'mount', '-mountPoint', str(mount), data[0])
            mounted = True
            run('sudo', '-n', 'diskutil', 'enableOwnership', data[0])
            source = Path(__file__).parent / 'guest/provision.sh'
            for relative in ['usr/local/libexec', 'Library/LaunchDaemons', 'private/var/db', 'Applications']:
                run('sudo', '-n', 'mkdir', '-p', str(mount / relative))
            for relative in ['usr/local', 'usr/local/libexec', 'Library/LaunchDaemons']:
                run('sudo', '-n', 'chown', 'root:wheel', str(mount / relative))
                run('sudo', '-n', 'chmod', '755', str(mount / relative))
            script = mount / 'usr/local/libexec/chickadee-provision'
            run('sudo', '-n', 'install', '-o', 'root', '-g', 'wheel', '-m', '700', str(source), str(script))
            if a.reporter:
                run('sudo', '-n', 'install', '-o', 'root', '-g', 'wheel', '-m', '700', str(a.reporter),
                    str(mount / 'usr/local/libexec/chickadee-build-report'))
            daemon = {'Label': 'run.chickadee.image-provision', 'ProgramArguments': ['/usr/local/libexec/chickadee-provision'], 'RunAtLoad': True}
            with tempfile.NamedTemporaryFile() as f:
                f.write(plistlib.dumps(daemon)); f.flush()
                run('sudo', '-n', 'install', '-o', 'root', '-g', 'wheel', '-m', '644', f.name,
                    str(mount / 'Library/LaunchDaemons/run.chickadee.image-provision.plist'))
            if a.skip_xcode_copy:
                version = 'Contents/version.plist'
                if (a.xcode / version).read_bytes() != (mount / 'Applications/Xcode.app' / version).read_bytes():
                    raise ValueError('Existing Xcode version metadata differs')
            else:
                run('sudo', '-n', 'ditto', '--noqtn', str(a.xcode), str(mount / 'Applications/Xcode.app'))
            run('sudo', '-n', 'touch', str(mount / 'private/var/db/.AppleSetupDone'))
            for relative in ['usr/local/libexec/chickadee-provision',
                             'Library/LaunchDaemons/run.chickadee.image-provision.plist',
                             'private/var/db/.AppleSetupDone']:
                target = mount / relative
                run('sudo', '-n', 'chown', 'root:wheel', str(target))
                if target.stat().st_uid != 0:
                    raise ValueError('Guest build service must be owned by root')
            print('OFFLINE_BUILD_FILES_INSTALLED')
        finally:
            if mounted:
                run('sudo', '-n', 'diskutil', 'unmount', str(mount))
            run('sudo', '-n', 'hdiutil', 'detach', base)
            mount.rmdir()
    finally:
        os.close(fd)


if __name__ == '__main__':
    main()
