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

    def test_offline_uses_supported_patched_local_go_without_downloads(self):
        original = (Path(__file__).resolve().parents[1] / 'Makefile').read_text()
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            launcher = root / 'go'
            launcher.write_text('#!/bin/sh\n'
                'test "$GOTOOLCHAIN" = local || { echo "would download pinned compiler" >&2; exit 42; }\n'
                'test "$GOPROXY" = off && test "$GOSUMDB" = off || exit 43\n'
                'if [ "$1 $2" = "env GOVERSION" ]; then echo "$TEST_LOCAL_GO";\n'
                'elif [ "$1" = test ]; then echo "offline unit tests ran";\n'
                'else exit 44; fi\n')
            launcher.chmod(0o755)
            env = dict(os.environ, PATH=str(root) + ':' + os.environ['PATH'],
                       GOTOOLCHAIN='auto', GOPROXY='https://example.invalid')
            for version, accepted in [('go1.26.9', False), ('go1.26.10', True),
                                      ('go1.26.11', True), ('go1.27.2', False),
                                      ('go1.27.3', True), ('go1.27.4', True),
                                      ('go1.27.3rc1', False), ('go1.27.3-custom', False)]:
                for arguments in ([], ['GOTOOLCHAIN=auto', 'GOPROXY=https://example.invalid']):
                    with self.subTest(version=version, arguments=arguments):
                        (root / 'Makefile').write_text(original)
                        result = subprocess.run(['make', 'test-offline', *arguments], cwd=root,
                            env=dict(env, TEST_LOCAL_GO=version), capture_output=True, text=True)
                        self.assertEqual(result.returncode == 0, accepted, result.stderr)
                        self.assertEqual('offline unit tests ran' in result.stdout, accepted)
            # Removing just the local target override reproduces the offline fetch failure.
            (root / 'Makefile').write_text(original.replace(
                'test-offline: override export GOTOOLCHAIN := local\n', ''))
            result = subprocess.run(['make', 'test-offline'], cwd=root,
                env=dict(env, TEST_LOCAL_GO='go1.27.3'), capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('would download pinned compiler', result.stderr)

            # Target-local settings must not escape into another requested build goal.
            (root / 'Makefile').write_text(original)
            launcher.write_text('#!/bin/sh\n'
                'if [ "$1 $2" = "env GOVERSION" ]; then\n'
                '  test "$GOTOOLCHAIN" = local || exit 42; echo go1.27.3;\n'
                'elif [ "$1" = test ]; then\n'
                '  test "$GOTOOLCHAIN" = local || exit 43; echo "offline unit tests ran";\n'
                'elif [ "$1" = build ]; then\n'
                '  test "$GOTOOLCHAIN" = go1.26.10 || exit 44; echo "pinned release build";\n'
                'else exit 45; fi\n')
            for goals in (['test-offline', 'build'], ['build', 'test-offline']):
                result = subprocess.run(['make', *goals, 'GOTOOLCHAIN=auto'],
                    cwd=root, env=env, capture_output=True, text=True)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn('offline unit tests ran', result.stdout)
                self.assertIn('pinned release build', result.stdout)
