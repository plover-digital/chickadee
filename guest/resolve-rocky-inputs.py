#!/usr/bin/env python3
"""Resolve a complete Rocky base upgrade and developer additions in a scratch guest.

Run only in a disposable copy of the pinned Rocky cloud image, before provisioning.
Requires its native python3-dnf. Does not install packages: it downloads the resolved
transaction, verifies RPM signatures, and emits a proposed lock. Review/replay that
lock offline in a separate fresh image; never run this against a live guest/host.
"""
import argparse
import hashlib
import json
import os
import pathlib
import re
import shutil
import subprocess
import urllib.parse


def package_names(text):
    names = []
    for line in text.splitlines():
        for name in line.partition('#')[0].split():
            if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9+_.-]*', name):
                raise ValueError('expected native package name, not a URL or option')
            if name not in names:
                names.append(name)
    return names


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('packages', type=pathlib.Path)
    parser.add_argument('destination', type=pathlib.Path)
    args = parser.parse_args()
    os.umask(0o077)
    os.environ["LC_ALL"] = "C"
    args.destination = args.destination.resolve()
    release = pathlib.Path('/etc/os-release').read_text()
    if '\nID="rocky"\n' not in '\n' + release or 'VERSION_ID="10.2"' not in release:
        parser.error('requires the pinned Rocky 10.2 guest environment')
    args.destination.mkdir(parents=True, exist_ok=False)
    # DNF is supplied by Rocky, not a pip replacement for its system bindings.
    import dnf
    with dnf.Base() as base:
        base.conf.cachedir = str(args.destination / 'cache')
        base.conf.keepcache = True
        base.conf.best = True
        base.conf.substitutions['releasever'] = '10.2'
        base.conf.substitutions['basearch'] = 'x86_64'
        base.conf.gpgcheck = True
        for name in ['BaseOS', 'AppStream', 'CRB']:
            repo = base.repos.add_new_repo(name.lower(), base.conf,
                baseurl=[f'https://download.rockylinux.org/pub/rocky/10.2/{name}/x86_64/os/'])
            repo.gpgcheck = True
            repo.skip_if_unavailable = False
        base.fill_sack(load_system_repo=True)
        base.upgrade_all()
        for name in package_names(args.packages.read_text()):
            base.install(name, strict=True)
        base.resolve()
        packages = list(base.transaction.install_set)
        base.download_packages(packages)
        entries = []
        archives = args.destination / 'rpms'
        archives.mkdir()
        subprocess.run(['rpmkeys', '--import', '/etc/pki/rpm-gpg/RPM-GPG-KEY-Rocky-10'], check=True)
        for package in packages:
            archive = pathlib.Path(package.localPkg())
            # Import only the distribution key already shipped in the pinned base.
            check = subprocess.run(['rpmkeys', '--checksig', str(archive)], check=True,
                                   capture_output=True, text=True)
            if 'signatures OK' not in check.stdout or 'NOKEY' in check.stdout:
                raise ValueError('RPM signature verification failed: ' + archive.name)
            url = package.remote_location()
            parsed = urllib.parse.urlsplit(url)
            if parsed.scheme != 'https' or parsed.hostname != 'download.rockylinux.org':
                raise ValueError('use canonical HTTPS Rocky baseurls before resolving')
            if pathlib.PurePosixPath(parsed.path).name != archive.name:
                raise ValueError('RPM URL/name mismatch')
            with archive.open('rb') as data:
                digest = hashlib.file_digest(data, 'sha256').hexdigest()
            shutil.copy2(archive, archives / archive.name)
            entries.append(dict(name=archive.name, url=url, sha256=digest,
                                nevra=str(package), source_rpm=package.sourcerpm,
                                repository=package.reponame))
        if not entries:
            raise ValueError('empty transaction; use a pristine pinned source image')
        (args.destination / 'rpm-inputs.json').write_text(
            json.dumps(sorted(entries, key=lambda p: p['name']), indent=2) + '\n')
        # Keep metadata in the private build evidence; do not invent a lock from
        # a hand-selected list of versions or discard unresolved dependencies.
        (args.destination / 'removals.json').write_text(
            json.dumps(sorted(str(p) for p in base.transaction.remove_set), indent=2) + '\n')


if __name__ == '__main__':
    main()
