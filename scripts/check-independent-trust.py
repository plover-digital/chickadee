#!/usr/bin/env python3
"""Read-only acceptance for actual JDK stores and distro NSS builtin anchors."""
import importlib.util
import json
import pathlib
import re
import subprocess
import tempfile

ROOT = pathlib.Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('trust', ROOT / 'guest/refresh-ca-trust.py')
trust = importlib.util.module_from_spec(spec)
spec.loader.exec_module(trust)


def nss_anchors(module):
    # A fresh DB with the actual module explicitly loaded avoids proving only
    # that an empty NSS database rejects everything. Never edits a user DB.
    with tempfile.TemporaryDirectory() as directory:
        db = 'sql:' + directory
        trust.run('certutil', '-N', '-d', db, '--empty-password', capture_output=True)
        trust.run('modutil', '-dbdir', db, '-add', 'CandidateBuiltin', '-libfile',
                  str(module), '-force', capture_output=True)
        modules = trust.run('modutil', '-dbdir', db, '-list', capture_output=True, text=True).stdout
        if str(module) not in modules or 'status: loaded' not in modules:
            raise ValueError('NSS builtin module identity not loaded')
        listing = trust.run('certutil', '-L', '-d', db, '-h', 'all', capture_output=True, text=True).stdout
        anchors = {}
        for line in listing.splitlines():
            entry = re.fullmatch(r'(.+?)\s{2,}([A-Za-z]*,[A-Za-z]*,[A-Za-z]*)\s*', line)
            if not entry or not any(flag in entry[2] for flag in 'CT'):
                continue
            pem = trust.run('certutil', '-L', '-d', db, '-n', entry[1], '-a', capture_output=True).stdout
            if len(trust.PEM.findall(pem)) != 1:
                raise ValueError('invalid exported NSS anchor')
            anchors[trust.spki(pem)] = {'nickname': entry[1], 'trust': entry[2]}
        if not anchors:
            raise ValueError('no NSS trust anchors enumerated; invalid acceptance')
        return anchors


def main():
    policy = json.loads((ROOT / 'guest/images/ca-trust.json').read_text())
    blocked = {r['spki_sha256'] for r in policy['roots']}
    jdk = []
    for java in sorted(pathlib.Path('/usr/lib/jvm').glob('*/bin/java')):
        home = java.resolve().parent.parent
        store = (home / 'lib/security/cacerts').resolve(strict=True)
        entries = trust.java_store_entries(home / 'bin/keytool', store)
        bad = {a: c for a, c in entries.items() if c['spki_sha256'] in blocked}
        jdk.append({'home': str(home), 'store': str(store), 'affected_anchors': bad})
    if not jdk:
        raise ValueError('no JDK discovered')
    modules = sorted({p.resolve() for p in pathlib.Path('/usr/lib').glob('**/libnssckbi.so')})
    if not modules:
        raise ValueError('no distro NSS builtin module discovered')
    nss = [{'module': str(p), 'affected_anchors': {k: v for k, v in nss_anchors(p).items() if k in blocked}}
           for p in modules]
    report = {'jdk': jdk, 'distro_nss': nss,
              'scope': 'anchor inventory only; require actual all12 client controls; no browser/Listener clearance'}
    print(json.dumps(report, indent=2))
    if any(r['affected_anchors'] for r in [*jdk, *nss]):
        raise SystemExit('independent runtime still trusts authenticated incident root keys')


if __name__ == '__main__':
    main()
