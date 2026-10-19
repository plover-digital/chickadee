"""Compile against a local module proxy to exercise ambient cache isolation."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
import zipfile


class ModuleCacheTests(unittest.TestCase):
    def test_modified_ambient_dependency_cannot_change_build_or_scanner(self):
        go = shutil.which('go')
        if not go:
            self.skipTest('Go is required for the module cache regression')
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            repo = root / 'repo'
            (repo / 'scripts').mkdir(parents=True)
            for name in ('vulncheck.sh', 'check-go-source.sh'):
                shutil.copy2(Path(__file__).with_name(name), repo / 'scripts' / name)
            proxy = root / 'proxy/example.invalid/dependency/@v'
            proxy.mkdir(parents=True)
            module = 'module example.invalid/dependency\n\ngo 1.25.3\n'
            (proxy / 'v1.0.0.mod').write_text(module)
            (proxy / 'v1.0.0.info').write_text('{"Version":"v1.0.0","Time":"2026-01-01T00:00:00Z"}')
            with zipfile.ZipFile(proxy / 'v1.0.0.zip', 'w') as archive:
                archive.writestr('example.invalid/dependency@v1.0.0/go.mod', module)
                archive.writestr('example.invalid/dependency@v1.0.0/value.go',
                                 'package dependency\nconst Value = "verified dependency"\n')
            (repo / 'go.mod').write_text('module fixture\n\ngo 1.25.3\n\nrequire example.invalid/dependency v1.0.0\n')
            (repo / 'main.go').write_text('package main\nimport("fmt"; "example.invalid/dependency")\nfunc main(){fmt.Println(dependency.Value)}\n')
            ambient = root / 'ambient'
            env = dict(os.environ, GOENV='off', GOWORK='off', GOMODCACHE=str(ambient),
                       GOPROXY=(root / 'proxy').as_uri(), GOSUMDB='off', GOFLAGS='-p=1',
                       GOTOOLCHAIN='local', GOMAXPROCS='1')
            def run(args, **kwargs):
                result = subprocess.run(args, cwd=repo, env=env,
                                        capture_output=True, text=True, **kwargs)
                self.assertEqual(result.returncode, 0, result.stderr)
                return result
            run([go, 'mod', 'download', 'example.invalid/dependency'])
            modified = ambient / 'example.invalid/dependency@v1.0.0/value.go'
            modified.chmod(0o644)
            modified.write_text('package dependency\nconst Value = "ambient modification"\n')
            self.assertEqual(run([go, 'run', '.']).stdout.strip(), 'ambient modification')
            for args in (['init', '-q'], ['add', '.'], ['-c', 'user.name=Test', '-c',
                         'user.email=test@example.invalid', 'commit', '-qm', 'fixture']):
                run(['git', *args])
            tools = root / 'tools'
            tools.mkdir()
            # Real Go compilation is used for both paths. Only the scanner CLI
            # is substituted with the tiny fixture to keep this test offline.
            (tools / 'go').write_text('''#!/bin/sh
if [ "$1" = install ]; then
  exec "$REAL_GO" build -buildvcs=false -o "$GOBIN/govulncheck" .
fi
exec "$REAL_GO" "$@"
''')
            (tools / 'make').write_text('''#!/bin/sh
set -eu
for name in chickadee chickadee-worker chickadee-guest; do
  "$REAL_GO" build -buildvcs=false -o "$TEST_BIN/$name" .
done
''')
            for path in tools.iterdir():
                path.chmod(0o755)
            output = root / 'reports'
            binaries = root / 'bin'
            env.update(PATH=f'{tools}:{env["PATH"]}', REAL_GO=go, TEST_BIN=str(binaries),
                       VULNCHECK_OUTPUT=str(output))
            run(['bash', 'scripts/vulncheck.sh', '--build', str(binaries)])
            attempt = output / (output / 'current').read_text().strip()
            self.assertEqual((attempt / 'status.txt').read_text(), 'complete\n')
            self.assertEqual(run([str(binaries / 'chickadee-worker')]).stdout.strip(), 'verified dependency')
            self.assertEqual((attempt / 'source.txt').read_text().strip(), 'verified dependency')
            self.assertIn('verified dependency', (attempt / 'environment.txt').read_text())
            self.assertIn('ambient modification', modified.read_text())
            metadata = (attempt / 'environment.txt').read_text()
            decoder = json.JSONDecoder()
            effective, _ = decoder.raw_decode(metadata[metadata.index('{'):])
            self.assertNotEqual(effective['GOMODCACHE'], str(ambient))
            self.assertFalse(Path(effective['GOMODCACHE']).exists())
            # The ambient downloaded tree is also read-only; clean our fixture.
            for directory, _, _ in os.walk(ambient):
                Path(directory).chmod(0o755)
