"""Exercise build entrypoints with a vulnerable newer bundled Go stand-in."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

class CompilerPin(unittest.TestCase):
    def test_auto_cannot_keep_vulnerable_newer_bundled_compiler(self):
        source = Path(__file__).resolve().parents[1] / 'Makefile'
        original = source.read_text()
        policy = 'override export GOTOOLCHAIN := go1.26.10\n'
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            launcher = root / 'go'
            launcher.write_text('#!/bin/sh\n'
                'if [ "$GOTOOLCHAIN" != go1.26.10 ]; then\n'
                '  echo "unsafe bundled go1.27.2 selected" >&2; exit 42\n'
                'fi\n'
                'echo "selected go1.26.10"\n')
            launcher.chmod(0o755)
            env = dict(os.environ, PATH=str(root) + ':' + os.environ['PATH'],
                       GOTOOLCHAIN='auto')
            for command_line in ([], ['GOTOOLCHAIN=auto']):
                for enforced in (True, False):
                    with self.subTest(command_line=command_line, enforced=enforced):
                        makefile = root / 'Makefile'
                        makefile.write_text(original if enforced else original.replace(policy, ''))
                        result = subprocess.run(['make', 'build', *command_line], cwd=root,
                                                env=env, capture_output=True, text=True)
                        self.assertEqual(result.returncode == 0, enforced, result.stderr)
                        if enforced:
                            self.assertIn('selected go1.26.10', result.stdout)
                        else:
                            self.assertIn('unsafe bundled go1.27.2 selected', result.stderr)

    def test_direct_image_bootstrap_builds_ignore_ambient_auto(self):
        scripts = Path(__file__).resolve().parents[1] / 'scripts'
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            launcher = root / 'go'
            launcher.write_text('#!/bin/sh\n'
                'test "$GOTOOLCHAIN" = go1.27.3 || exit 42\n')
            launcher.chmod(0o755)
            env = dict(os.environ, PATH=str(root) + ':' + os.environ['PATH'],
                       GOTOOLCHAIN='auto')
            for name in ['build-image.sh', 'build-github-compatible-image.sh',
                         'build-ubuntu-2604-image.sh', 'build-rocky-image.sh']:
                commands = [s for s in (scripts / name).read_text().splitlines()
                            if 'go build ' in s]
                self.assertEqual(len(commands), 1)
                for enforced in (True, False):
                    with self.subTest(builder=name, enforced=enforced):
                        command = commands[0]
                        if not enforced:
                            command = command.replace('GOTOOLCHAIN=go1.27.3 ', '')
                        result = subprocess.run(['bash', '-c', command], cwd=root,
                                                env=env, capture_output=True, text=True)
                        self.assertEqual(result.returncode == 0, enforced, result.stderr)
