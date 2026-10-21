#!/usr/bin/env python3
"""Developer-profile PHP acceptance. Does not install packages or start a server."""
import importlib.util
import json
import pathlib

ROOT = pathlib.Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('php_security', ROOT / 'guest/refresh-php-security.py')
php = importlib.util.module_from_spec(spec)
spec.loader.exec_module(php)


def check(policy, version):
    release = policy['releases'][version]
    rows = php.require_fixed(php.query(), release)
    required = {release['source'] + '-' + suffix for suffix in ['cli', 'common', 'intl', 'mbstring', 'readline']}
    if version == '24.04':
        required.add('php8.3-opcache')
    if not required <= {r['package'] for r in rows}:
        raise ValueError('developer PHP runtime/extension cohort is incomplete')
    smoke = php.smoke(rows)
    if smoke.get('composer') == 'not installed':
        raise ValueError('developer Composer command is missing')
    return {'notice': policy['notice'], 'minimum_version': release['minimum_version'], 'runtime_packages': rows, 'smoke': smoke}


if __name__ == '__main__':
    release = dict(line.split('=', 1) for line in pathlib.Path('/etc/os-release').read_text().splitlines() if '=' in line)
    if release['ID'].strip('"') != 'ubuntu':
        raise ValueError('Ubuntu developer PHP check only')
    print(json.dumps(check(json.loads((ROOT / 'guest/images/php-security.json').read_text()), release['VERSION_ID'].strip('"')), indent=2))
