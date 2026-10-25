#!/usr/bin/env python3
"""Require a sealed CA/bootstrap/PHP baseline before preserving it during a runtime refresh."""
import json
import pathlib
import re
import sys


def validate(root):
    read = lambda name: json.loads((root / name).read_text())
    manifest = read('manifest.json')
    if manifest.get('trust_refresh') != 'emudhra-20261021-v1':
        raise ValueError('runtime refresh requires the reviewed CA/bootstrap baseline first')
    bootstrap = read('bootstrap-build-provenance.json')
    trust = read('trust-build-provenance.json')
    digest = bootstrap['binary_sha256']
    if bootstrap['compiler'] != 'go1.27.3' or not re.fullmatch('[0-9a-f]{64}', digest) or trust['bootstrap_sha256'] != digest:
        raise ValueError('parent bootstrap provenance mismatch')
    php = read('php-security.json')
    if manifest['os'] == 'ubuntu' and manifest.get('php_security_refresh') != 'USN-8948-1':
        raise ValueError('runtime refresh requires the PHP security baseline first')
    # The sealed report is prerequisite provenance; the actual ELF and tuple
    # are verified again inside the disposable image before any runtime edit.
    if not isinstance(php, dict):
        raise ValueError('invalid parent PHP security report')
    return digest


if __name__ == '__main__':
    print(validate(pathlib.Path(sys.argv[1])))
