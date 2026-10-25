#!/usr/bin/env python3
"""Bound native Ubuntu updates from signed indexes; native APT must validate the exact plan later.

This is a conservative closure preparer, not an APT solver. Unsupported or
unresolved dependencies stop preparation; native removals/extra packages stop
installation. Installed virtual providers require an exact-version authenticated index witness.
"""
import argparse
import hashlib
import json
import lzma
import pathlib
import re
import subprocess

ROOT = pathlib.Path(__file__).resolve().parents[1]


def paragraphs(text):
    for paragraph in text.strip().split('\n\n'):
        result, key = {}, None
        for line in paragraph.splitlines():
            if line.startswith((' ', '\t')):
                if key:
                    result[key] += ' ' + line.strip()
            elif ':' in line:
                key, value = line.split(':', 1)
                value = value.lstrip()
                result[key] = value
        if result:
            yield result


def version_compare(left, right):
    """Debian epoch/upstream/revision ordering, including tilde and numeric runs."""
    def split(value):
        if not re.fullmatch(r'[A-Za-z0-9.+:~\-]+', value):
            raise ValueError('invalid Debian version')
        epoch, sep, rest = value.partition(':')
        epoch, rest = (int(epoch), rest) if sep else (0, value)
        upstream, sep, revision = rest.rpartition('-')
        return epoch, upstream if sep else rest, revision if sep else '0'

    def part(a, b):
        def order(c):
            return -1 if c == '~' else 0 if not c else ord(c) if c.isalpha() else ord(c) + 256
        while a or b:
            while (a and not a[0].isdigit()) or (b and not b[0].isdigit()):
                x = order(a[0] if a and not a[0].isdigit() else '')
                y = order(b[0] if b and not b[0].isdigit() else '')
                if x != y:
                    return (x > y) - (x < y)
                if a and not a[0].isdigit():
                    a = a[1:]
                if b and not b[0].isdigit():
                    b = b[1:]
            ma, mb = re.match(r'\d*', a)[0], re.match(r'\d*', b)[0]
            x, y = ma.lstrip('0'), mb.lstrip('0')
            if len(x) != len(y):
                return (len(x) > len(y)) - (len(x) < len(y))
            if x != y:
                return (x > y) - (x < y)
            a, b = a[len(ma):], b[len(mb):]
        return 0
    a, b = split(left), split(right)
    return ((a[0] > b[0]) - (a[0] < b[0])) or part(a[1], b[1]) or part(a[2], b[2])


def relation(text):
    match = re.fullmatch(r'([a-z0-9+.-]+)(?::(?:any|native|amd64))?(?:\s*\((<<|<=|=|>=|>>)\s*([^()]+)\))?', text.strip())
    if not match:
        raise ValueError('unsupported binary dependency: ' + text)
    return match.groups()


def fulfills(actual, operator, required):
    if not operator:
        return True
    result = version_compare(actual, required)
    return {'<<': result < 0, '<=': result <= 0, '=': result == 0,
            '>=': result >= 0, '>>': result > 0}[operator]


def signed_catalog(policy, family, metadata, keyring):
    catalog, evidence = {}, []
    release = policy['releases'][family]
    for entry in release['indexes']:
        suite = entry['suite']
        path = metadata / (suite + '-InRelease')
        if hashlib.sha256(path.read_bytes()).hexdigest() != entry['inrelease_sha256']:
            raise ValueError('unpinned InRelease')
        signature = subprocess.run(['gpgv', '--status-fd', '1', '--keyring', str(keyring), str(path)], capture_output=True, text=True)
        if signature.returncode or 'VALIDSIG ' + policy['signing_fingerprint'] + ' ' not in signature.stdout:
            raise ValueError('Ubuntu archive signature rejected')
        text = path.read_text()
        if '\nSuite: ' + suite + '\n' not in text or '\nCodename: ' + release['suite'] + '\n' not in text:
            raise ValueError('signed release identity mismatch')
        hashes = text.split('SHA256:\n', 1)[1]
        for component in entry['components']:
            index = metadata / (suite + '-' + component + '-Packages.xz')
            match = re.search(r'^ ([0-9a-f]{64})\s+(\d+) ' + component + r'/binary-amd64/Packages.xz$', hashes, re.M)
            if not match or index.stat().st_size != int(match[2]) or hashlib.sha256(index.read_bytes()).hexdigest() != match[1]:
                raise ValueError('signed package index mismatch')
            evidence.append({'suite': suite, 'component': component, 'inrelease_sha256': entry['inrelease_sha256'], 'index_sha256': match[1], 'index_bytes': int(match[2])})
            with lzma.open(index, 'rt') as stream:
                for row in paragraphs(stream.read()):
                    if row.get('Architecture') not in ('all', 'amd64'):
                        continue
                    row.update(suite=suite, component=component)
                    name = row['Package']
                    previous = catalog.get(name)
                    if not previous or version_compare(row['Version'], previous['Version']) > 0:
                        catalog[name] = row
                    elif row['Version'] == previous['Version'] and row['SHA256'] != previous['SHA256']:
                        raise ValueError('ambiguous equal-version archive content')
    return catalog, evidence


def closure(inventory, catalog):
    installed = {r['name'].split(':')[0]: r for r in inventory['packages']}
    if len(installed) != len(inventory['packages']):
        raise ValueError('multiarch/duplicate inventory unsupported')
    witnesses = {name: row for name, row in catalog.items() if name in installed
                 and row['Version'] == installed[name]['version']
                 and row['Architecture'] == installed[name]['architecture']}
    selected = {name: row for name, row in catalog.items() if name in installed and version_compare(row['Version'], installed[name]['version']) > 0}

    def satisfies(text):
        name, operator, required = relation(text)
        row = selected.get(name)
        version = row['Version'] if row else installed.get(name, {}).get('version')
        if version and fulfills(version, operator, required):
            return True
        for provider in set(installed) | set(selected):
            row = selected.get(provider) or witnesses.get(provider)
            if not row:
                continue
            for value in filter(None, row.get('Provides', '').split(',')):
                provided, op, revision = relation(value)
                if provided == name and (not operator or op == '=' and fulfills(revision, operator, required)):
                    return True
        return False

    pending = list(selected)
    while pending:
        row = selected[pending.pop()]
        for group in filter(None, (row.get('Pre-Depends', '') + ',' + row.get('Depends', '')).split(',')):
            alternatives = group.split('|')
            if any(satisfies(alt) for alt in alternatives):
                continue
            for alternative in alternatives:
                name, _, _ = relation(alternative)
                candidate = catalog.get(name)
                if not candidate or name in installed and version_compare(candidate['Version'], installed[name]['version']) < 0:
                    continue
                previous = selected.get(name)
                selected[name] = candidate
                if satisfies(alternative):
                    if previous != candidate:
                        pending.append(name)
                    break
                if previous:
                    selected[name] = previous
                else:
                    del selected[name]
            else:
                raise ValueError('unresolved dependency for ' + row['Package'] + ': ' + group)
    return selected, witnesses


def prepare(policy, family, metadata, keyring, inventory):
    if inventory.get('os') != 'ubuntu' or inventory.get('os_version') != family:
        raise ValueError('wrong Ubuntu parent inventory')
    catalog, evidence = signed_catalog(policy, family, metadata, keyring)
    selected, witnesses = closure(inventory, catalog)
    kernel = policy['releases'][family]['kernel']
    if 'linux-image-' + kernel not in selected or 'linux-image-generic' not in selected:
        raise ValueError('expected newer generic kernel transaction missing')
    packages = []
    for name, row in sorted(selected.items()):
        filename = pathlib.PurePosixPath(row['Filename'])
        if not str(filename).startswith('pool/') or '..' in filename.parts or filename.suffix != '.deb' or not re.fullmatch('[0-9a-f]{64}', row['SHA256']):
            raise ValueError('invalid signed deb identity')
        packages.append({'name': name, 'version': row['Version'], 'architecture': row['Architecture'], 'source': row.get('Source', name), 'filename': filename.name, 'url': policy['archive_url'] + str(filename), 'sha256': row['SHA256'], 'size': int(row['Size']), 'installed_size_kib': int(row.get('Installed-Size', 0)), 'suite': row['suite'], 'component': row['component']})
    total = sum(row['size'] for row in packages)
    if len(packages) > 512 or total > 1024 ** 3:
        raise ValueError('transaction exceeds finite preparation budget')
    return {'schema': 1, 'family': family, 'snapshot': policy['snapshot'], 'kernel': kernel, 'signing_fingerprint': policy['signing_fingerprint'], 'signed_indexes': evidence, 'parent_packages': inventory['packages'], 'provider_witnesses': [{'package': row['Package'], 'version': row['Version'], 'provides': row.get('Provides', '')} for row in witnesses.values() if row.get('Provides')], 'packages': packages, 'download_bytes': total, 'installed_size_kib': sum(r['installed_size_kib'] for r in packages), 'native_apt_exact_plan_validation': 'REQUIRED BEFORE INSTALL; static closure is not native solver acceptance'}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--policy', type=pathlib.Path, default=ROOT / 'guest/images/ubuntu-os-security.json')
    parser.add_argument('--family', choices=['24.04', '26.04'], required=True)
    parser.add_argument('--metadata', type=pathlib.Path, required=True)
    parser.add_argument('--keyring', type=pathlib.Path, required=True)
    parser.add_argument('--inventory', type=pathlib.Path, required=True)
    parser.add_argument('--output', type=pathlib.Path, required=True)
    args = parser.parse_args()
    plan = prepare(json.loads(args.policy.read_text()), args.family, args.metadata, args.keyring, json.loads(args.inventory.read_text()))
    args.output.write_text(json.dumps(plan, indent=2) + '\n')


if __name__ == '__main__':
    main()
