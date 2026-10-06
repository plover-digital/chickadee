import importlib.util
from pathlib import Path
import os
import stat
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('setup_app', Path(__file__).with_name('setup-app.py'))
setup = importlib.util.module_from_spec(spec)
spec.loader.exec_module(setup)


class SetupSecurityTests(unittest.TestCase):
    def test_manifest_requests_only_runner_management(self):
        m = setup.manifest('example', 'test-runner', 'http://127.0.0.1:18734/callback')
        self.assertEqual(m['default_permissions'], {'organization_self_hosted_runners': 'write'})
        self.assertFalse(m['public'])
        self.assertFalse(m['hook_attributes']['active'])
        self.assertEqual(m['default_events'], [])
        self.assertNotIn('callback_urls', m)

    def test_private_file_is_exclusive_and_not_world_readable(self):
        with tempfile.TemporaryDirectory() as root:
            p = Path(root) / 'key'
            setup.save_new(p, 'test fixture')
            self.assertEqual(stat.S_IMODE(p.stat().st_mode), 0o600)
            with self.assertRaises(FileExistsError):
                setup.save_new(p, 'replacement')
            self.assertEqual(p.read_text(), 'test fixture')

    def test_symlink_cannot_redirect_private_write(self):
        with tempfile.TemporaryDirectory() as root:
            target = Path(root) / 'target'
            target.write_text('keep')
            link = Path(root) / 'key'
            link.symlink_to(target)
            with self.assertRaises(OSError):
                setup.save_new(link, 'replacement')
            self.assertEqual(target.read_text(), 'keep')

    def test_unsafe_output_directory_rejected(self):
        with tempfile.TemporaryDirectory() as root:
            p = Path(root) / 'credentials'
            p.mkdir(mode=0o755)
            with self.assertRaises(ValueError):
                setup.private_directory(p)
            os.chmod(p, 0o700)
            setup.private_directory(p)
            link = Path(root) / 'link'
            link.symlink_to(p, target_is_directory=True)
            with self.assertRaises(ValueError):
                setup.private_directory(link)


if __name__ == '__main__':
    unittest.main()
