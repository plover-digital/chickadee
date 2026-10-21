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
