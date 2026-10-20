import copy
import importlib.util
import pathlib
import unittest

spec = importlib.util.spec_from_file_location('refresh', pathlib.Path(__file__).with_name('refresh-image-go-lock.py'))
refresh = importlib.util.module_from_spec(spec)
spec.loader.exec_module(refresh)

class GoLock(unittest.TestCase):
    def setUp(self):
        self.supported = [{'version': 'go1.27.2', 'stable': True}]
        self.release = {'tag_name': '1.27.2-123', 'assets': [{
            'name': 'go-1.27.2-linux-x64.tar.gz', 'digest': 'sha256:' + 'a' * 64,
            'browser_download_url': 'https://github.com/actions/go-versions/releases/download/1.27.2-123/go-1.27.2-linux-x64.tar.gz'}]}
        self.lock = {'tools': [{'name': 'node', 'version': '24'}, {'name': 'go', 'version': '1.24.13'}]}

    def test_replaces_unsupported_go_preserves_other_tools_and_input(self):
        original = copy.deepcopy(self.lock)
        result = refresh.refresh(self.lock, self.supported, [self.release])
        self.assertEqual(self.lock, original)
        self.assertEqual(result['default_go'], '1.27.2')
        self.assertEqual(result['tools'][-1], self.lock['tools'][0])
        self.assertEqual(len(result['tools']), 2)

    def test_missing_digest_rejected(self):
        self.release['assets'][0]['digest'] = None
        with self.assertRaisesRegex(ValueError, 'digest'):
            refresh.refresh(self.lock, self.supported, [self.release])

    def test_wrong_origin_rejected(self):
        self.release['assets'][0]['browser_download_url'] = 'https://example.org/archive'
        with self.assertRaisesRegex(ValueError, 'URL'):
            refresh.refresh(self.lock, self.supported, [self.release])

    def test_unsupported_or_duplicate_release_rejected(self):
        with self.assertRaisesRegex(ValueError, 'supported'):
            refresh.refresh(self.lock, [], [self.release])
        with self.assertRaisesRegex(ValueError, 'distinct'):
            refresh.refresh(self.lock, self.supported, [self.release, self.release])

class BaselineConsistency(unittest.TestCase):
    def test_supported_guest_defaults_and_smoke_workflows(self):
        import json
        root = pathlib.Path(__file__).resolve().parents[1]
        for name in ['github-ubuntu-2404.json', 'github-ubuntu-2604.json']:
            lock = json.loads((root / 'guest/images' / name).read_text())
            versions = {t['version'] for t in lock['tools'] if t['name'] == 'go'}
            self.assertEqual(versions, {'1.26.9', '1.27.2'})
            self.assertIn(lock['default_go'], versions)
            self.assertEqual(lock['ubuntu_snapshot'], '20261020T000000Z')
        for name in ['image-compatibility.yml', 'ubuntu-2604-smoke.yml']:
            workflow = (root / '.github/workflows' / name).read_text()
            self.assertIn("go-version: '1.27.2'", workflow)
            self.assertNotIn('1.26.8', workflow)

    def test_minimal_builders_and_active_guides_match_profile_snapshot(self):
        import json
        import re
        root = pathlib.Path(__file__).resolve().parents[1]
        snapshot = json.loads((root / 'guest/images/github-ubuntu-2404.json').read_text())['ubuntu_snapshot']
        minimal = json.loads((root / 'guest/images/ubuntu-2604.json').read_text())
        self.assertEqual(minimal['snapshot'], snapshot)
        builder = (root / 'scripts/build-image.sh').read_text()
        self.assertEqual(re.search(r'^snapshot=(\d{8}T\d{6}Z)$', builder, re.M).group(1), snapshot)
        for name in ['install.md', 'ubuntu-2604.md', 'image-security.md']:
            with self.subTest(guide=name):
                dates = set(re.findall(r'20\d{6}T\d{6}Z', (root / 'docs' / name).read_text()))
                self.assertEqual(dates, {snapshot})
