#!/usr/bin/env python3
"""Replay PHP lock authentication against retained Ubuntu signed metadata."""
import argparse
import hashlib
import json
import lzma
import pathlib
import re
import subprocess


def records(data):
    for block in lzma.decompress(data).decode().split('\n\n'):
        yield dict(line.split(': ', 1) for line in block.splitlines() if ': ' in line and not line.startswith(' '))


def verify(policy, evidence, keyring):
    for release in policy['releases'].values():
        suite = release['suite'].removesuffix('-security')
        auth = release['authentication']
        path = evidence / (suite + '-InRelease')
        signed = path.read_bytes()
        if hashlib.sha256(signed).hexdigest() != auth['inrelease_sha256']:
            raise ValueError('signed release digest mismatch')
        status = subprocess.run(['gpgv', '--keyring', str(keyring), '--status-fd', '1', str(path)], check=True, capture_output=True, text=True).stdout
        if '[GNUPG:] VALIDSIG ' + auth['signing_fingerprint'] + ' ' not in status:
            raise ValueError('unexpected Ubuntu archive signer')
        text = signed.decode()
        if '\nSuite: ' + release['suite'] + '\n' not in text or '\nDate: ' + auth['date'] + '\n' not in text:
            raise ValueError('signed release identity mismatch')
        hashes = text.split('SHA256:\n', 1)[1]
        signed_index = {line.split()[2]: (line.split()[0], int(line.split()[1])) for line in hashes.splitlines() if re.fullmatch(r' [0-9a-f]{64} +[0-9]+ +\S+', line)}
        candidates = {}
        for index in auth['indexes']:
            if signed_index[index['path']] != (index['sha256'], index['size']):
                raise ValueError('index digest not bound by signed release')
            component = index['path'].split('/')[0]
            data = (evidence / (suite + '-' + component + '-Packages.xz')).read_bytes()
            if len(data) != index['size'] or hashlib.sha256(data).hexdigest() != index['sha256']:
                raise ValueError('package index hash mismatch')
            for r in records(data):
                if r.get('Source', r.get('Package', '')).split(' ')[0] == release['source'] and r.get('Version') == release['version'] and r.get('Architecture') in ['all', 'amd64']:
                    candidates[r['Package']] = r
        if set(candidates) != {e['package'] for e in release['packages']}:
            raise ValueError('incomplete authenticated PHP source cohort')
        for e in release['packages']:
            r = candidates[e['package']]
            expected = (r['Version'], r['Architecture'], r['SHA256'], int(r['Size']), 'https://security.ubuntu.com/ubuntu/' + r['Filename'])
            actual = (e['version'], e['architecture'], e['sha256'], e['size'], e['url'])
            if actual != expected or pathlib.Path(r['Filename']).name != e['filename']:
                raise ValueError('PHP lock differs from authenticated index')
        print(release['suite'] + ': authenticated complete cohort, ' + str(len(candidates)) + ' packages')


if __name__ == '__main__':
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--policy', required=True, type=pathlib.Path)
    p.add_argument('--evidence', required=True, type=pathlib.Path)
    p.add_argument('--keyring', required=True, type=pathlib.Path)
    a = p.parse_args()
    verify(json.loads(a.policy.read_text()), a.evidence, a.keyring)
