#!/usr/bin/env python3
"""Serial authenticated supplemental inputs; no runtime execution or package installation."""
import argparse
import hashlib
import json
import pathlib
import subprocess

ROOT = pathlib.Path(__file__).resolve().parents[1]


def entries():
    read = lambda name: json.loads((ROOT / 'guest/images' / name).read_text())
    result = [read('runner-linux-x64.json'), read('rocky-ca-security.json')['package']]
    result += [t for t in read('github-ubuntu-2404.json')['tools'] if t['name'] in {'node', 'python'}]
    result += [t for t in read('github-ubuntu-2604.json')['tools'] if t['name'] == 'python']
    result += read('ca-trust.json')['python_wheels']
    result += [e for r in read('nss-security.json')['releases'].values() for e in r['packages']]
    result += read('github-extra-tools.json')['tools']
    unique = {}
    for e in result:
        if e['filename'] in unique and unique[e['filename']] != e:
            raise ValueError('conflicting supplemental basename')
        unique[e['filename']] = e
    return list(unique.values())


def verify(path, entry):
    if not path.is_file() or path.stat().st_size > 400 * 1024 ** 2:
        raise ValueError('missing/oversized runtime input: ' + path.name)
    algorithm = entry.get('hash_algorithm', 'sha256')
    with path.open('rb') as stream:
        digest = hashlib.file_digest(stream, algorithm).hexdigest()
    if digest != entry.get('digest', entry.get('sha256')):
        raise ValueError('runtime input checksum mismatch: ' + path.name)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--verify-only', action='store_true')
    parser.add_argument('directory', type=pathlib.Path)
    args = parser.parse_args()
    args.directory.mkdir(parents=True, exist_ok=True)
    total = 0
    for e in entries():
        if pathlib.Path(e['filename']).name != e['filename'] or not e['url'].startswith('https://'):
            raise ValueError('invalid runtime input')
        target = args.directory / e['filename']
        if not target.exists() and not args.verify_only:
            part = target.with_suffix('.partial')
            try:
                subprocess.run(['nice', '-n', '19', 'curl', '--fail', '--location', '--silent', '--show-error', '--proto', '=https', '--limit-rate', '4M', '--max-time', '600', '--max-filesize', str(400 * 1024 ** 2), e['url'], '-o', str(part)], check=True)
                verify(part, e)
                part.rename(target)
            finally:
                part.unlink(missing_ok=True)
        verify(target, e)
        total += target.stat().st_size
        if total > 3 * 1024 ** 3:
            raise ValueError('supplemental inputs exceed 3 GiB')
    print(json.dumps({'files': len(entries()), 'total_bytes': total}))


if __name__ == '__main__':
    main()
