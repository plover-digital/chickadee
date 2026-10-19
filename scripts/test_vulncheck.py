"""Exercise scan failure propagation without a network or a Go compilation."""
import os
import json
import shutil
from pathlib import Path
import subprocess
import tempfile
import unittest

SCRIPT = Path(__file__).with_name('vulncheck.sh').resolve()


class VulnerabilityCheckTests(unittest.TestCase):
    def run_scan(self, failure='', dirty='', build=False, ambient_config=False, early_failure=''):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            repo = root / 'repo'
            (repo / 'scripts').mkdir(parents=True)
            for name in ('vulncheck.sh', 'check-go-source.sh'):
                target = repo / 'scripts' / name
                target.write_bytes(SCRIPT.with_name(name).read_bytes())
                target.chmod(0o755)
            (repo / '.gitignore').write_text('/bin/\n/build/\n')
            (repo / 'input.go').write_text('package input\n')
            for args in (['init', '-q'], ['add', '.'],
                         ['-c', 'user.name=Test', '-c', 'user.email=test@example.invalid',
                          'commit', '-qm', 'fixture']):
                subprocess.run(['git', '-C', str(repo), *args], check=True,
                               capture_output=True)
            (repo / 'bin').mkdir()
            (repo / 'bin' / 'ignored-binary').write_bytes(b'output')
            (repo / 'build').mkdir()
            (repo / 'build' / 'ignored-report.txt').write_text('report')
            if dirty == 'tracked':
                (repo / 'input.go').write_text('package changed\n')
            elif dirty == 'untracked':
                (repo / 'extra.go').write_text('package input\n')
            elif dirty == 'ignored-source':
                (repo / 'build' / 'generated.go').write_text('package generated\n')
            tools = root / 'tools'
            tools.mkdir()
            binaries = root / 'bin'
            binaries.mkdir()
            for name in ('chickadee', 'chickadee-worker', 'chickadee-guest'):
                (binaries / name).write_text('candidate, never executed')
            scanner = root / 'scanner'
            scanner.write_text('''#!/usr/bin/env bash
if [[ "$*" == "-version" ]]; then echo 'scanner v1.9.0'; exit 0; fi
printf '%s CGO=%s\n' "$*" "${CGO_ENABLED:-unset}"
if [[ -n "$FAIL_SCAN" && "$*" == *"$FAIL_SCAN"* ]]; then exit 3; fi
''')
            scanner.chmod(0o755)
            go = tools / 'go'
            go.write_text('''#!/usr/bin/env bash
if [[ "${EARLY_FAILURE:-}" == install && "$1" == install ]]; then exit 23; fi
if [[ "${EARLY_FAILURE:-}" == metadata && "$1" == version ]]; then exit 24; fi
if [[ "$1" == install ]]; then
  test "$2" = 'golang.org/x/vuln/cmd/govulncheck@v1.9.0' || exit 9
  cp "$FAKE_SCANNER" "$GOBIN/govulncheck"
elif [[ "$1" == env && -n "${REAL_GO:-}" ]]; then
  "$REAL_GO" "$@"
else
  echo 'test build metadata'
fi
''')
            go.chmod(0o755)
            make = tools / 'make'
            make.write_text('''#!/usr/bin/env bash
set -eu
if [[ "${EARLY_FAILURE:-}" == build ]]; then exit 22; fi
test "$GOENV" = off
test "$(cat input.go)" = 'package input'
printf snapshot > "$BUILD_MARKER"
''')
            make.chmod(0o755)
            marker = root / 'build-marker'
            output = root / 'reports'
            env = dict(os.environ, PATH=f'{tools}:{os.environ["PATH"]}',
                       FAKE_SCANNER=str(scanner), FAIL_SCAN=failure, BUILD_MARKER=str(marker),
                       VULNCHECK_OUTPUT=str(output))
            if ambient_config:
                real_go = shutil.which('go')
                if not real_go:
                    self.skipTest('Go needed to verify persisted environment behavior')
                config = root / 'ambient-goenv'
                overlay = root / 'external-overlay.json'
                outside = root / 'outside.go'
                outside.write_text('package externally_replaced\n')
                overlay.write_text(json.dumps({'Replace': {str(repo / 'input.go'): str(outside)}}))
                expected = '-overlay=' + str(overlay)
                config.write_text('GOFLAGS=' + expected + '\n')
                env['GOENV'] = str(config)
                env['REAL_GO'] = real_go
                env.pop('GOFLAGS', None)
                before = subprocess.check_output([real_go, 'env', 'GOFLAGS'], env=env, text=True)
                self.assertEqual(before.strip(), expected)
            if early_failure:
                previous = subprocess.run(['bash', str(repo / 'scripts' / 'vulncheck.sh'), '--build', str(binaries)],
                                          env=env, capture_output=True, text=True)
                self.assertEqual(previous.returncode, 0, previous.stderr)
                old_run = output / (output / 'current').read_text().strip()
                old_reports = {p.name: p.read_bytes() for p in old_run.iterdir()}
                (output / 'user-notes').write_text('preserve me')
                env['EARLY_FAILURE'] = early_failure
            result = subprocess.run(['bash', str(repo / 'scripts' / 'vulncheck.sh'), *(['--build'] if build else []), str(binaries)],
                                    env=env, capture_output=True, text=True)
            run = output / (output / 'current').read_text().strip() if output.exists() else None
            reports = {p.name: p.read_text() for p in run.iterdir()} if run else {}
            if early_failure:
                self.assertNotEqual(run, old_run)
                self.assertEqual(old_reports, {p.name: p.read_bytes() for p in old_run.iterdir()})
                self.assertEqual((output / 'user-notes').read_text(), 'preserve me')
                self.assertTrue(reports['status.txt'].startswith('failed'))
                self.assertNotIn('source.txt', reports)
                self.assertNotIn('chickadee-worker.txt', reports)
            if marker.exists():
                reports['build-marker'] = marker.read_text()
            return result, reports

    def test_success_records_all_artifacts_and_release_source_mode(self):
        result, reports = self.run_scan(build=True)
        self.assertEqual(reports.get('build-marker'), 'snapshot')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(reports['status.txt'], 'complete\n')
        self.assertIn('CGO=0', reports['source.txt'])
        for name in ('chickadee', 'chickadee-worker', 'chickadee-guest'):
            self.assertIn('-mode=binary', reports[f'{name}.txt'])
            self.assertIn(f'{name}.sha256', reports)
            self.assertIn(f'{name}.build.txt', reports)

    def test_early_failure_identifies_new_attempt_and_preserves_history(self):
        for stage in ('build', 'install', 'metadata'):
            with self.subTest(stage=stage):
                result, reports = self.run_scan(build=True, early_failure=stage)
                self.assertNotEqual(result.returncode, 0)

    def test_persisted_goenv_overlay_is_disabled(self):
        result, reports = self.run_scan(build=True, ambient_config=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(reports.get('build-marker'), 'snapshot')
        self.assertIn('GOENV=off\n', reports['environment.txt'])
        self.assertIn('"GOENV": ""', reports['environment.txt'])
        self.assertIn('"GOFLAGS": ""', reports['environment.txt'])
        self.assertNotIn('external-overlay', reports['environment.txt'])

    def test_dirty_inputs_are_rejected_before_evidence(self):
        for dirty in ('tracked', 'untracked', 'ignored-source'):
            with self.subTest(dirty=dirty):
                result, reports = self.run_scan(dirty=dirty, build=True)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn('Vulnerability evidence', result.stderr)
                self.assertEqual(reports, {})

    def test_source_failure_survives_tee_and_still_scans_binaries(self):
        result, reports = self.run_scan('./...')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('chickadee-guest.txt', reports)

    def test_binary_failure_is_not_hidden_by_later_success(self):
        result, reports = self.run_scan('chickadee-worker')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('chickadee-guest.txt', reports)


if __name__ == '__main__':
    unittest.main()
