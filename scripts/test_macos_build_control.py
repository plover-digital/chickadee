"""Negative tests for untrusted build status input; no macOS VM required."""
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


class BuildControlTests(unittest.TestCase):
    def run_report(self, payload, symlink=False, device_nonce='a'*32):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            disk = root / 'build-control.raw'
            target = root / 'target' if symlink else disk
            with target.open('wb') as f:
                f.truncate(1024*1024)
                f.write(json.dumps({'v': 1, 'kind': 'chickadee-build-control', 'nonce': device_nonce}).encode().ljust(4096, b'\0'))
                f.seek(4096)
                f.write(payload)
            target.chmod(0o600)
            metadata = root / 'build-control.json'
            metadata.write_text(json.dumps({'v': 1, 'kind': 'chickadee-build-control', 'nonce': 'a'*32}))
            metadata.chmod(0o600)
            if symlink:
                disk.symlink_to(target)
            return subprocess.run([sys.executable, str(Path(__file__).parent / 'macos/control-device.py'), 'read', directory], capture_output=True, text=True)

    def test_valid_status(self):
        result = self.run_report(json.dumps({'v': 1, 'nonce': 'a'*32, 'status': 'PROVISIONED_OFFLINE'}).encode())
        self.assertEqual(result.returncode, 0)
        self.assertEqual(result.stdout.strip(), 'PROVISIONED_OFFLINE')

    def test_stale_nonce_rejected(self):
        self.assertNotEqual(self.run_report(json.dumps({'v': 1, 'nonce': 'b'*32, 'status': 'PROVISIONED_OFFLINE'}).encode()).returncode, 0)

    def test_extra_fields_and_ready_rejected(self):
        for payload in [dict(v=1, nonce='a'*32, status='READY'), dict(v=1, nonce='a'*32, status='PROVISIONING', credential='fake')]:
            self.assertNotEqual(self.run_report(json.dumps(payload).encode()).returncode, 0)

    def test_oversized_or_incomplete_report_rejected(self):
        for payload in [b'x'*8192, b'{"v":1']:
            self.assertNotEqual(self.run_report(payload).returncode, 0)

    def test_symlink_rejected(self):
        self.assertNotEqual(self.run_report(b'{}', symlink=True).returncode, 0)

    def test_guest_cannot_replace_expected_nonce(self):
        payload = json.dumps(dict(v=1, nonce='b'*32, status='PROVISIONED_OFFLINE')).encode()
        self.assertNotEqual(self.run_report(payload, device_nonce='b'*32).returncode, 0)

    def test_duplicate_fields_rejected(self):
        payload = ('{"v":1,"nonce":"' + 'a'*32 + '","status":"READY","status":"PROVISIONED_OFFLINE"}').encode()
        self.assertNotEqual(self.run_report(payload).returncode, 0)
