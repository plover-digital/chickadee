import hashlib
import importlib.util
import json
import pathlib
import tempfile
import unittest
from unittest import mock

ROOT = pathlib.Path(__file__).resolve().parents[1]

def load(name, path):
    spec = importlib.util.spec_from_file_location(name, ROOT / path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module

php = load('php', 'guest/refresh-php-security.py')
accept = load('php_accept', 'scripts/check-php-security.py')


class PHPSecurity(unittest.TestCase):
    def setUp(self):
        self.policy = json.loads((ROOT / 'guest/images/php-security.json').read_text())

    def rows(self, version, package_version):
        release = self.policy['releases'][version]
        suffixes = ['cli', 'common', 'intl', 'mbstring', 'readline'] + (['opcache'] if version == '24.04' else [])
        text = ''.join(f"{release['source']}-{suffix}\t{package_version}\t{release['source']}\tamd64\tinstalled\n" for suffix in suffixes)
        return php.inventory(text)

    def test_old_deployed_cohorts_fail_normal_acceptance_fixed_cohorts_pass(self):
        # Native dpkg owns version ordering. These known old-version outcomes
        # exercise the actual developer acceptance gate without requiring dpkg
        # on a non-Debian development host or executing any PHP server.
        for version, old in [('24.04', '8.3.6-0ubuntu0.24.04.11'), ('26.04', '8.5.4-0ubuntu1.3')]:
            fixed = self.policy['releases'][version]['minimum_version']
            for installed, expected in [(old, False), (fixed, True)]:
                with self.subTest(version=version, installed=installed):
                    rows = self.rows(version, installed)
                    with mock.patch.object(accept.php, 'query', return_value=rows), mock.patch.object(accept.php, 'smoke', return_value={'executed': True}), mock.patch.object(accept.php, 'compare', return_value=expected):
                        if expected:
                            self.assertEqual(accept.check(self.policy, version)['runtime_packages'], rows)
                        else:
                            with self.assertRaisesRegex(ValueError, 'below the vendor'):
                                accept.check(self.policy, version)

    def test_mixed_versions_fail_even_if_each_meets_minimum(self):
        release = self.policy['releases']['26.04']
        rows = self.rows('26.04', release['minimum_version'])
        rows[0]['version'] = '8.5.4-0ubuntu1.6'
        with mock.patch.object(php, 'compare', return_value=True):
            with self.assertRaisesRegex(ValueError, 'inconsistent'):
                php.require_fixed(rows, release)

    def test_unknown_package_or_other_php_source_cannot_be_silently_left_old(self):
        release = self.policy['releases']['24.04']
        for source, name in [('php8.3', 'php8.3-unreviewed'), ('php8.4', 'php8.4-cli')]:
            with self.subTest(source=source):
                rows = [{'package': name, 'version': 'old', 'source': source, 'architecture': 'amd64'}]
                with self.assertRaises(ValueError):
                    php.cohort(rows, release)

    def test_developer_requires_full_runtime_extensions(self):
        rows = self.rows('24.04', self.policy['releases']['24.04']['version'])[:-1]
        with mock.patch.object(accept.php, 'query', return_value=rows), mock.patch.object(accept.php, 'compare', return_value=True):
            with self.assertRaisesRegex(ValueError, 'incomplete'):
                accept.check(self.policy, '24.04')

    def test_tampered_or_missing_prefetched_packages_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / 'package.deb'
            path.write_bytes(b'bad')
            with self.assertRaisesRegex(ValueError, 'checksum'):
                php.check_file(path, {'size': 3, 'sha256': hashlib.sha256(b'old').hexdigest()})
            with self.assertRaisesRegex(ValueError, 'missing prefetched'):
                php.inputs(self.policy, pathlib.Path(directory), offline=True)

    def test_installed_only_selection_covers_any_locked_sapi_and_retains_unrelated_rows(self):
        release = self.policy['releases']['26.04']
        selected = self.rows('26.04', release['version'])
        selected.append({'package': 'php8.5-fpm', 'source': 'php8.5', 'architecture': 'amd64', 'version': release['version']})
        unrelated = {'package': 'libc6', 'source': 'glibc', 'architecture': 'amd64', 'version': 'preserve'}
        self.assertEqual(php.cohort(selected + [unrelated], release), selected)
        self.assertEqual(unrelated['version'], 'preserve')

    def test_compare_delegates_debian_version_semantics_to_dpkg(self):
        with mock.patch.object(php.subprocess, 'run') as run:
            run.return_value.returncode = 0
            self.assertTrue(php.compare('8.5.4-0ubuntu1.5', 'ge', '8.5.4-0ubuntu1.5'))
            run.assert_called_once_with(['dpkg', '--compare-versions', '8.5.4-0ubuntu1.5', 'ge', '8.5.4-0ubuntu1.5'], check=False)


if __name__ == '__main__':
    unittest.main()
