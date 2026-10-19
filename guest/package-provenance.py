#!/usr/bin/env python3
"""Capture installed guest package identities for vendor advisory review.

Run only inside a disposable image during construction. This inventory is not a
vulnerability scan, and a vendor package version is not proof of a backport.
"""
import json
import pathlib
import subprocess


def parse_packages(output, kind):
    packages = []
    for line in output.splitlines():
        fields = line.split('\t')
        if kind == 'deb':
            if len(fields) != 6:
                raise ValueError('invalid dpkg inventory row')
            status, name, version, arch, source, source_version = fields
            if len(status) < 2 or status[1] != 'i':
                continue
        elif kind == 'rpm':
            if len(fields) != 4:
                raise ValueError('invalid rpm inventory row')
            name, version, arch, source = fields
            source_version = None
        else:
            raise ValueError('unsupported package manager')
        packages.append(dict(name=name, version=version, architecture=arch,
                             source=source, source_version=source_version))
    return sorted(packages, key=lambda p: (p['name'], p['architecture'], p['version']))


def main():
    release = {}
    for line in pathlib.Path('/etc/os-release').read_text().splitlines():
        if '=' in line:
            key, value = line.split('=', 1)
            release[key] = value.strip('"')
    if release.get('ID') == 'ubuntu':
        kind = 'deb'
        command = ['dpkg-query', '-W', '-f=${db:Status-Abbrev}\t${binary:Package}\t${Version}\t${Architecture}\t${source:Package}\t${source:Version}\n']
    elif release.get('ID') == 'rocky':
        kind = 'rpm'
        command = ['rpm', '-qa', '--qf', '%{NAME}\t%{EPOCHNUM}:%{VERSION}-%{RELEASE}\t%{ARCH}\t%{SOURCERPM}\n']
    else:
        raise ValueError('expected an Ubuntu or Rocky guest root')
    output = subprocess.check_output(command, text=True)
    print(json.dumps(dict(schema=1, os=release['ID'], os_version=release['VERSION_ID'],
                          package_manager=kind, packages=parse_packages(output, kind)), indent=2))


if __name__ == '__main__':
    main()
