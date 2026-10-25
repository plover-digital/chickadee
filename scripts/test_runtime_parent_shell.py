#!/usr/bin/env python3
"""Run the actual Bash entry points; never launch an appliance or image tool."""
import hashlib
import json
import os
import pathlib
import shlex
import shutil
import subprocess
import sys
import tempfile
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[1]


class RuntimeParentShell(unittest.TestCase):
    def fixture(self, root):
        scripts = root / 'scripts'
        scripts.mkdir()
        for name in ['refresh-runtime-bundle.sh', 'refresh-image-runtimes.sh', 'check-runtime-parent.py']:
            shutil.copyfile(ROOT / 'scripts' / name, scripts / name)
        parent = root / 'parent'
        parent.mkdir()
        (parent / 'SHA256SUMS').write_text('synthetic sealed input check delegated to stub\n')
        (parent / 'manifest.json').write_text(json.dumps({'os': 'rocky'}))
        bindir = root / 'bin'
        bindir.mkdir()
        trace = root / 'trace'
        def stub(name, body):
            path = bindir / name
            path.write_text('#!/bin/bash\nset -eu\n' + body + '\n')
            path.chmod(0o755)
        stub('git', 'exit 0')
        stub('python3', '''case "$1" in
 scripts/check-image-bundle.py) exit 0 ;;
 scripts/check-runtime-parent.py)
  if [[ ${EMPTY_PARENT_RESULT:-0} == 1 ]]; then exit 0; fi
  exec ''' + shlex.quote(sys.executable) + ''' "$@" ;;
 *) echo input-preparation >> "$TRACE"; exit 91 ;;
esac''')
        stub('qemu-img', 'echo image-copy >> "$TRACE"; exit 92')
        stub('virt-customize', 'echo runtime-edit >> "$TRACE"; exit 93')
        env = dict(os.environ, PATH=str(bindir) + os.pathsep + os.environ['PATH'], TRACE=str(trace))
        env.pop('CHICKADEE_RUNTIME_BOOTSTRAP_SHA256', None)
        return scripts, parent, trace, env

    def run_bundle(self, root, old_export=False, empty_result=False):
        scripts, parent, trace, env = self.fixture(root)
        if old_export:
            path = scripts / 'refresh-runtime-bundle.sh'
            text = path.read_text()
            checked = 'CHICKADEE_RUNTIME_BOOTSTRAP_SHA256=$(python3 scripts/check-runtime-parent.py "$source_dir")\n[[ $CHICKADEE_RUNTIME_BOOTSTRAP_SHA256 =~ ^[0-9a-f]{64}$ ]]\nexport CHICKADEE_RUNTIME_BOOTSTRAP_SHA256'
            self.assertIn(checked, text)
            path.write_text(text.replace(checked, 'export CHICKADEE_RUNTIME_BOOTSTRAP_SHA256=$(python3 scripts/check-runtime-parent.py "$source_dir")'))
        # A controlled entry-point stub records reaching preparation. It never
        # executes the image helper, so even the negative control is harmless.
        helper = scripts / 'refresh-image-runtimes.sh'
        helper.write_text('#!/bin/bash\necho input-preparation >> "$TRACE"\nexit 91\n')
        helper.chmod(0o755)
        if empty_result:
            env['EMPTY_PARENT_RESULT'] = '1'
        seal = hashlib.sha256((parent / 'SHA256SUMS').read_bytes()).hexdigest()
        output = root / 'candidate'
        result = subprocess.run(['bash', str(scripts / 'refresh-runtime-bundle.sh'), str(parent), str(output), seal, 'rocky'], env=env, capture_output=True, text=True)
        events = trace.read_text().splitlines() if trace.exists() else []
        self.assertFalse(output.exists())
        return result, events

    def test_real_wrapper_rejects_raw_parent_before_any_work(self):
        with tempfile.TemporaryDirectory() as directory:
            result, events = self.run_bundle(pathlib.Path(directory))
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('requires the reviewed CA/bootstrap baseline', result.stderr)
            self.assertEqual(events, [])

    def test_empty_successful_validation_also_fails_closed(self):
        with tempfile.TemporaryDirectory() as directory:
            result, events = self.run_bundle(pathlib.Path(directory), empty_result=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(events, [])

    def test_old_export_negative_control_reaches_forbidden_preparation(self):
        with tempfile.TemporaryDirectory() as directory:
            result, events = self.run_bundle(pathlib.Path(directory), old_export=True)
            self.assertEqual(result.returncode, 91)
            self.assertEqual(events, ['input-preparation'])

    def test_incremental_image_helper_requires_nonempty_valid_digest_first(self):
        for digest in [None, '', 'invalid']:
            with self.subTest(digest=digest), tempfile.TemporaryDirectory() as directory:
                root = pathlib.Path(directory)
                scripts, parent, trace, env = self.fixture(root)
                if digest is not None:
                    env['CHICKADEE_RUNTIME_BOOTSTRAP_SHA256'] = digest
                result = subprocess.run(['bash', str(scripts / 'refresh-image-runtimes.sh'), '--prepare', str(root / 'inputs'), 'rocky', 'incremental'], env=env, capture_output=True, text=True)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(trace.exists())
                self.assertFalse((root / 'inputs').exists())

    def test_full_builder_is_explicit_separate_path(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            scripts, parent, trace, env = self.fixture(root)
            result = subprocess.run(['bash', str(scripts / 'refresh-image-runtimes.sh'), '--prepare', str(root / 'inputs'), 'rocky', 'full-builder'], env=env, capture_output=True, text=True)
            self.assertEqual(result.returncode, 91)
            self.assertEqual(trace.read_text().splitlines(), ['input-preparation'])

    def test_guest_cli_rejects_empty_or_omitted_install_digest_before_files(self):
        for mode in [[], ['--expected-bootstrap-sha256', '']]:
            result = subprocess.run([sys.executable, str(ROOT / 'guest/refresh-runtime-security.py'), '--runner', '/does-not-exist', '--lock', '/does-not-exist', '--install', *mode, '/does-not-exist'], capture_output=True, text=True)
            self.assertEqual(result.returncode, 2)
            self.assertNotIn('FileNotFoundError', result.stderr)


if __name__ == '__main__':
    unittest.main()
