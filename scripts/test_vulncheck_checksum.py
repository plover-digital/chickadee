"""Real Go must reject an unauthenticated scanner served by a custom proxy."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
import zipfile


class ChecksumPolicyTests(unittest.TestCase):
    def test_bypass_settings_cannot_attest_forged_scanner(self):
        go = shutil.which('go')
        if not go:
            self.skipTest('Go required for checksum verification')
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            repo = root / 'repo'
            (repo / 'scripts').mkdir(parents=True)
            for name in ('vulncheck.sh', 'check-go-source.sh'):
                shutil.copy2(Path(__file__).with_name(name), repo / 'scripts' / name)
            for args in (['init', '-q'], ['add', '.'], ['-c', 'user.name=Test', '-c',
                         'user.email=test@example.invalid', 'commit', '-qm', 'fixture']):
                subprocess.run(['git', '-C', str(repo), *args], check=True, capture_output=True)
            proxy = root / 'proxy'
            versions = proxy / 'golang.org/x/vuln/@v'
            versions.mkdir(parents=True)
            module = 'module golang.org/x/vuln\n\ngo 1.25.3\n'
            (versions / 'v1.9.0.mod').write_text(module)
            (versions / 'v1.9.0.info').write_text('{"Version":"v1.9.0","Time":"2026-01-01T00:00:00Z"}')
            (versions / 'list').write_text('v1.9.0\n')
            with zipfile.ZipFile(versions / 'v1.9.0.zip', 'w') as archive:
                archive.writestr('golang.org/x/vuln@v1.9.0/go.mod', module)
                archive.writestr('golang.org/x/vuln@v1.9.0/cmd/govulncheck/main.go',
                                 'package main\nfunc main(){}\n')
            # Offline proxy advertises sumdb support but supplies no signed record.
            sumdb = proxy / 'sumdb/sum.golang.org'
            (sumdb / 'lookup/golang.org/x').mkdir(parents=True)
            (sumdb / 'supported').write_text('')
            (sumdb / 'lookup/golang.org/x/vuln@v1.9.0').write_text('unauthenticated record\n')
            base = dict(os.environ, GOENV='off', GOWORK='off', GOTOOLCHAIN='local',
                        GOPROXY=proxy.as_uri(), GOMAXPROCS='1', GOFLAGS='-p=1',
                        GONOPROXY='none', GOPRIVATE='', GONOSUMDB='none', GOPATH=str(root / 'gopath'))
            for index, settings in enumerate(({'GOSUMDB': 'off'},
                    {'GOSUMDB': 'sum.golang.org', 'GONOSUMDB': 'golang.org/x/*'},
                    {'GOSUMDB': 'sum.golang.org', 'GOPRIVATE': 'golang.org/x/*', 'GONOSUMDB': ''})):
                with self.subTest(settings=settings):
                    env = dict(base, **settings)
                    env.update(GOMODCACHE=str(root / f'ambient-{index}'), GOBIN=str(root / f'bin-{index}'))
                    # Demonstrate that the bypass actually installs the forged scanner.
                    before = subprocess.run([go, 'install', 'golang.org/x/vuln/cmd/govulncheck@v1.9.0'],
                                            env=env, cwd=repo, capture_output=True, text=True)
                    self.assertEqual(before.returncode, 0, before.stderr)
                    self.assertTrue((Path(env['GOBIN']) / 'govulncheck').is_file())
                    output = root / f'evidence-{index}'
                    env['VULNCHECK_OUTPUT'] = str(output)
                    after = subprocess.run(['bash', 'scripts/vulncheck.sh', str(root / 'candidates')],
                                           env=env, cwd=repo, capture_output=True, text=True)
                    self.assertNotEqual(after.returncode, 0)
                    self.assertIn('verifying module', after.stderr)
                    self.assertIn('malformed record', after.stderr)
                    attempt = output / (output / 'current').read_text().strip()
                    self.assertTrue((attempt / 'status.txt').read_text().startswith('failed'))
                    self.assertFalse((attempt / 'source.txt').exists())
            for directory, _, _ in os.walk(root):
                Path(directory).chmod(0o755)
