#!/usr/bin/env python3
"""Offline OpenSSL validation of all 12 public incident chains (no private keys)."""
import argparse
import hashlib
import json
import pathlib
import re
import subprocess
import tempfile
import zipfile

NAMES = {f'chains/{kind}-g3{generation}{cross}-emudhra.we-have-a-problem.com.pem'
         for kind in ('dv', 'ov', 'ev') for generation in ('b', 'c') for cross in ('', '-aaa')}
TRUST_ERRORS = ('unable to get local issuer certificate', 'self-signed certificate in certificate chain',
                'certificate rejected', 'certificate revoked', 'unable to verify the first certificate')


def check(chains_zip, ca_bundle, expected):
    policy = json.loads((pathlib.Path(__file__).resolve().parents[1] / 'guest/images/ca-trust.json').read_text())
    if hashlib.sha256(chains_zip.read_bytes()).hexdigest() != policy['chains_zip_sha256']:
        raise ValueError('public incident chain archive checksum mismatch')
    results = []
    with tempfile.TemporaryDirectory() as directory, zipfile.ZipFile(chains_zip) as archive:
        names = {n for n in archive.namelist() if n.endswith('.pem')}
        if names != NAMES:
            raise ValueError('expected exactly the 12 reviewed public TLS chains')
        for name in sorted(NAMES):
            path = pathlib.Path(directory) / pathlib.Path(name).name
            path.write_bytes(archive.read(name))
            hostname = path.stem
            result = subprocess.run(['openssl', 'verify', '-purpose', 'sslserver', '-verify_hostname', hostname,
                                     '-no-CApath', '-no-CAstore', '-CAfile', str(ca_bundle),
                                     '-untrusted', str(path), str(path)], capture_output=True, text=True)
            accepted = result.returncode == 0
            reason = result.stdout + result.stderr
            # Expiry, wrong hostname, malformed fixture, or tool errors do not
            # demonstrate a trust fix. Require an issuer/trust rejection.
            valid = accepted if expected == 'accept' else (not accepted and any(e in reason for e in TRUST_ERRORS))
            results.append({'hostname': hostname, 'accepted': accepted, 'expected': expected, 'pass': valid,
                            'detail': re.sub(re.escape(directory), '<temporary>', reason).strip()})
    return results


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--chains-zip', required=True, type=pathlib.Path)
    parser.add_argument('--ca-bundle', required=True, type=pathlib.Path)
    parser.add_argument('--expect', required=True, choices=['accept', 'reject'])
    args = parser.parse_args()
    results = check(args.chains_zip, args.ca_bundle, args.expect)
    print(json.dumps(results, indent=2))
    raise SystemExit(0 if all(r['pass'] for r in results) else 1)


if __name__ == '__main__':
    main()
