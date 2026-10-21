#!/usr/bin/env python3
import hashlib
import importlib.util
import json
import os
import pathlib
import shutil
import subprocess
import tempfile
import unittest
from unittest import mock

ROOT = pathlib.Path(__file__).resolve().parents[1]

def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module

bundle = load('bundle', ROOT / 'scripts/check-image-bundle.py')
trust = load('refresh_trust', ROOT / 'guest/refresh-ca-trust.py')


def make_bundle(root):
    root.mkdir()
    for name, data in {'base.qcow2': b'disk', 'vmlinuz-7': b'kernel', 'initrd-7': b'initrd',
                       'manifest.json': json.dumps({'kernel': '7'}).encode()}.items():
        (root / name).write_bytes(data)
    (root / 'SHA256SUMS').write_text(''.join(
        hashlib.sha256(p.read_bytes()).hexdigest() + '  ' + p.name + '\n'
        for p in sorted(root.iterdir())))
    (root / 'vmlinuz').symlink_to('vmlinuz-7')
    (root / 'initrd').symlink_to('initrd-7')


class SealedBundles(unittest.TestCase):
    def test_valid_flat_bundle(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory) / 'image'; make_bundle(root)
            self.assertIn('vmlinuz-7', bundle.validate(root))

    @unittest.skipUnless(shutil.which('sha256sum'), 'sha256sum needed for old-behavior control')
    def test_unsealed_extra_and_kernel_alias_pass_old_digest_check_but_are_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory) / 'image'; make_bundle(root)
            (root / 'unsealed-kernel').write_bytes(b'unreviewed')
            (root / 'vmlinuz').unlink(); (root / 'vmlinuz').symlink_to('unsealed-kernel')
            # This is the previous refresh's admission check: all named hashes
            # remain valid despite an unsealed extra becoming the active kernel.
            old = subprocess.run(['sha256sum', '--check', '--status', 'SHA256SUMS'], cwd=root)
            self.assertEqual(old.returncode, 0)
            with self.assertRaisesRegex(ValueError, 'unsealed'):
                bundle.validate(root)

    def test_alias_cannot_select_wrong_sealed_file_or_sealed_symlink(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory) / 'image'; make_bundle(root)
            (root / 'vmlinuz').unlink(); (root / 'vmlinuz').symlink_to('initrd-7')
            with self.assertRaisesRegex(ValueError, 'expected sealed regular'):
                bundle.validate(root)
            (root / 'vmlinuz').unlink(); (root / 'vmlinuz').symlink_to('vmlinuz-7')
            outside = pathlib.Path(directory) / 'outside'; outside.write_bytes(b'kernel')
            (root / 'vmlinuz-7').unlink(); (root / 'vmlinuz-7').symlink_to(outside)
            with self.assertRaisesRegex(ValueError, 'regular file'):
                bundle.validate(root)


class BootstrapCompiler(unittest.TestCase):
    @unittest.skipUnless(shutil.which('git'), 'git needed for helper integration')
    def test_actual_compiler_must_match_manifest_before_copy(self):
        for legacy in (True, False):
            with self.subTest(legacy_negative_control=legacy), tempfile.TemporaryDirectory() as directory:
                root = pathlib.Path(directory); repo = root / 'repo'; scripts = repo / 'scripts'; scripts.mkdir(parents=True)
                # Exercise the complete old/new helper, rather than a duplicated
                # regex: the old helper really reaches the fake copy command.
                helper = (ROOT / 'scripts/refresh-trust-bundle.sh').read_text()
                if legacy:
                    # Restore the previous permissive version gate as an
                    # executable negative control, without requiring history.
                    start = helper.index("actual=subprocess.check_output(['go','version','-m'")
                    end = helper.index('PYBOOT', start)
                    helper = helper[:start] + helper[end:]
                    helper = helper.replace('python3 scripts/check-image-bundle.py',
                        "go version -m \"$bootstrap\" | grep -Eq 'go1\\.(26\\.10|27\\.3)$'\npython3 scripts/check-image-bundle.py")
                (scripts / 'refresh-trust-bundle.sh').write_text(helper)
                (scripts / 'refresh-image-trust.sh').write_text('#!/bin/sh\nexit 0\n')
                (scripts / 'refresh-image-trust.sh').chmod(0o755)
                shutil.copyfile(ROOT / 'scripts/check-image-bundle.py', scripts / 'check-image-bundle.py')
                def git(*args):
                    return subprocess.check_output(['git', *args], cwd=repo, stderr=subprocess.DEVNULL, text=True).strip()
                git('init'); git('add', '.')
                git('-c', 'commit.gpgsign=false', '-c', 'user.name=Chickadee test', '-c', 'user.email=test@example.invalid', 'commit', '-m', 'fixture')
                commit = git('rev-parse', 'HEAD')
                source = root / 'image'; make_bundle(source)
                binary = root / 'guest'; binary.write_bytes(b'compiled with wrong Go')
                manifest = root / 'manifest.json'; manifest.write_text(json.dumps({
                    'source_commit': commit, 'compiler': 'go1.27.3',
                    'binary_sha256': hashlib.sha256(binary.read_bytes()).hexdigest()}))
                tools = root / 'tools'; tools.mkdir(); marker = root / 'copy-started'
                (tools / 'go').write_text('#!/bin/sh\nprintf "%s: go1.26.10\\n" "$3"\n')
                for name in ('qemu-img', 'virt-customize', 'guestfish'):
                    (tools / name).write_text('#!/bin/sh\ntouch "$COPY_MARKER"\nexit 19\n')
                for tool in tools.iterdir(): tool.chmod(0o755)
                result = subprocess.run(['bash', str(scripts / 'refresh-trust-bundle.sh'), str(source),
                                         str(root / 'output'), str(binary), str(manifest)],
                                        env=dict(os.environ, PATH=str(tools) + os.pathsep + os.environ['PATH'], COPY_MARKER=str(marker)),
                                        capture_output=True, text=True)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(marker.exists(), legacy, result.stderr)
                if not legacy:
                    self.assertIn('actual bootstrap compiler must match', result.stderr)


class RockyAllPurposes(unittest.TestCase):
    def test_all_four_primary_roots_are_blocked_even_when_cs_root_absent_from_tls(self):
        policy = json.loads((ROOT / 'guest/images/ca-trust.json').read_text())
        tls_only = b'\n'.join(r['pem'].encode() for r in policy['roots'] if r['affected_usage'] == 'tls')
        self.assertEqual(len(trust.blocked_certificates(tls_only, policy)), 3)  # old enumeration misses CS
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            trust.install_rocky_blocklist(policy, root)
            actual = {trust.spki(p.read_bytes()) for p in root.glob('*.pem')}
            self.assertEqual(actual, {r['spki_sha256'] for r in policy['roots']})
            cs = next(r for r in policy['roots'] if r['affected_usage'] == 'code-signing')
            self.assertIn(cs['spki_sha256'], actual)
        def extraction(contents):
            def invoke(*args, **kwargs):
                self.assertIn('--filter=blocklist', args)
                self.assertFalse(any(a.startswith('--purpose') for a in args))
                pathlib.Path(args[-1]).write_bytes(contents)
            return invoke
        all_roots = b'\n'.join(r['pem'].encode() for r in policy['roots'])
        real_run = trust.run
        def extract_only(contents):
            def invoke(*args, **kwargs):
                if args[0] == 'trust': return extraction(contents)(*args, **kwargs)
                return real_run(*args, **kwargs)
            return invoke
        with mock.patch.object(trust, 'run', side_effect=extract_only(tls_only)):
            with self.assertRaisesRegex(ValueError, 'purpose-independent'):
                trust.verify_rocky_distrust(policy)
        with mock.patch.object(trust, 'run', side_effect=extract_only(all_roots)):
            self.assertEqual(set(trust.verify_rocky_distrust(policy)), actual)


if __name__ == '__main__':
    unittest.main()
