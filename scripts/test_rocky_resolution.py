import json
import re
import urllib.parse
import importlib.util
import pathlib
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('resolver', ROOT / 'guest/resolve-rocky-inputs.py')
resolver = importlib.util.module_from_spec(spec)
spec.loader.exec_module(resolver)

class RockyResolution(unittest.TestCase):
    def test_package_groups_comments_and_duplicates(self):
        self.assertEqual(resolver.package_names('# ignored\n git curl # comment\n curl gcc-c++\n'),
                         ['git', 'curl', 'gcc-c++'])

    def test_repository_list_parses_as_individual_native_packages(self):
        names = resolver.package_names((ROOT / 'guest/images/rocky-102-developer-packages.txt').read_text())
        self.assertIn('gcc-c++', names)
        self.assertIn('container-selinux', names)
        self.assertGreater(len(names), 50)

    def test_urls_and_options_rejected(self):
        for value in ['https://example.com/a.rpm', '--nogpgcheck']:
            with self.assertRaises(ValueError):
                resolver.package_names(value)

    def test_committed_lock_has_unique_canonical_hashed_rpms(self):
        entries = json.loads((ROOT / 'guest/images/rocky-102-rpms.json').read_text())
        self.assertTrue(entries)
        self.assertEqual(len(entries), len({entry['name'] for entry in entries}))
        for entry in entries:
            with self.subTest(rpm=entry['name']):
                url = urllib.parse.urlsplit(entry['url'])
                self.assertEqual((url.scheme, url.netloc), ('https', 'download.rockylinux.org'))
                self.assertTrue(url.path.startswith('/pub/rocky/10.2/'))
                self.assertEqual(pathlib.PurePosixPath(url.path).name, entry['name'])
                self.assertTrue(re.fullmatch(r'[a-f0-9]{64}', entry['sha256']))
                self.assertTrue(entry['source_rpm'].endswith('.src.rpm'))
                self.assertIn(entry['repository'], ['baseos', 'appstream', 'crb'])
                self.assertTrue(entry['nevra'])
