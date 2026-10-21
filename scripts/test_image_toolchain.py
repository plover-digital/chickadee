import os
import pathlib
import subprocess
import tempfile
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[1]

class ToolchainSelection(unittest.TestCase):
    def test_metadata_commands_override_an_older_go_launcher(self):
        # Execute the real shell command from each builder with an older-launcher
        # stand-in: without an explicit selection it would fetch the module's
        # older toolchain. No image/QEMU or network is needed for this regression.
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            launcher = root / 'go'
            launcher.write_text('#!/bin/sh\n'
                'test "$GOTOOLCHAIN" = go1.27.3 || { echo "would select older module toolchain" >&2; exit 42; }\n'
                'test "$1 $2" = "version -m" || exit 43\n'
                'echo "recorded go1.27.3"\n')
            launcher.chmod(0o755)
            for child in ['images', 'build/github-compatible', 'output']:
                (root / child).mkdir(parents=True)
            env = dict(os.environ, PATH=str(root) + os.pathsep + os.environ['PATH'])
            env.pop('GOTOOLCHAIN', None)
            # Demonstrate that this harness actually catches the previous command.
            old = subprocess.run(['bash', '-c', 'go version -m fixture'], env=env,
                                 cwd=root, capture_output=True)
            self.assertEqual(old.returncode, 42)
            for name in ['build-image.sh', 'build-rocky-image.sh',
                         'build-ubuntu-2604-image.sh', 'build-github-compatible-image.sh']:
                with self.subTest(builder=name):
                    commands = [line for line in (ROOT / 'scripts' / name).read_text().splitlines()
                                if 'go version -m ' in line]
                    self.assertEqual(len(commands), 1)
                    result = subprocess.run(['bash', '-c', 'work=build; out=output; ' + commands[0]],
                                            env=env, cwd=root, capture_output=True, text=True)
                    self.assertEqual(result.returncode, 0, result.stderr)

    def test_install_guide_matches_current_snapshot(self):
        import json
        lock = json.loads((ROOT / 'guest/images/ubuntu-2604.json').read_text())
        guide = (ROOT / 'docs/install.md').read_text()
        self.assertIn('archive snapshot `' + lock['snapshot'] + '`', guide)
