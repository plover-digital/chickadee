#!/usr/bin/env python3
"""Emit a proposed guest Go lock from supported Go and Actions release metadata.

Never modifies the input lock. Review the new lock and verify downloaded archives
with fetch-image-tools.py before using it in a fresh image build.
"""
import argparse
import json
import pathlib
import re
import urllib.request


def get_json(url):
    with urllib.request.urlopen(url, timeout=60) as response:
        return json.load(response)


def refresh(lock, supported, releases):
    versions = {r['version'].removeprefix('go') for r in supported if r['stable']}
    tools = []
    for release in releases:
        version = release['tag_name'].split('-')[0]
        if version not in versions or release.get('draft') or release.get('prerelease'):
            raise ValueError('release must be a current supported stable Go version')
        filename = f'go-{version}-linux-x64.tar.gz'
        assets = [a for a in release['assets'] if a['name'] == filename]
        if len(assets) != 1:
            raise ValueError('missing or ambiguous linux-x64 asset')
        asset = assets[0]
        digest = asset.get('digest') or ''
        expected = f"https://github.com/actions/go-versions/releases/download/{release['tag_name']}/{filename}"
        if not re.fullmatch(r'sha256:[0-9a-f]{64}', digest) or asset['browser_download_url'] != expected:
            raise ValueError('missing digest or unexpected asset URL')
        tools.append(dict(name='go', version=version, filename=filename,
                          url=expected, sha256=digest[7:], platform='linux-x64'))
    if not tools or len({t['version'] for t in tools}) != len(tools):
        raise ValueError('provide distinct supported releases')
    tools.sort(key=lambda t: tuple(map(int, t['version'].split('.'))))
    result = dict(lock)
    result['tools'] = tools + [t for t in lock['tools'] if t['name'] != 'go']
    result['default_go'] = tools[-1]['version']
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('input', type=pathlib.Path)
    parser.add_argument('output', type=pathlib.Path)
    parser.add_argument('--release', action='append', required=True,
                        help='exact actions/go-versions release tag; repeat per supported series')
    args = parser.parse_args()
    if any(not re.fullmatch(r'1\.\d+\.\d+-\d+', tag) for tag in args.release):
        parser.error('expected exact Actions Go release tags')
    result = refresh(json.loads(args.input.read_text()), get_json('https://go.dev/dl/?mode=json'),
                     [get_json('https://api.github.com/repos/actions/go-versions/releases/tags/' + tag)
                      for tag in args.release])
    with args.output.open('x') as output:
        output.write(json.dumps(result, indent=2) + '\n')


if __name__ == '__main__':
    main()
