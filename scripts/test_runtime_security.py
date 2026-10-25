#!/usr/bin/env python3
import copy
import hashlib
import importlib.util
import json
import pathlib
import tempfile
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[1]


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, ROOT / path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


nss = load('nss', 'guest/refresh-nss-security.py')
runtime = load('runtime', 'guest/refresh-runtime-security.py')
check = load('check', 'scripts/check-runtime-security.py')
metadata = load('metadata', 'scripts/refresh-runtime-metadata.py')


class RuntimeSecurity(unittest.TestCase):
    def test_old_node_inventory_fails(self):
        for actual in [['22.23.3', '24.21.0'], ['22.23.4', '24.22.1', '20.20.2']]:
            with self.assertRaises(ValueError):
                check.require_node_versions(actual)
        check.require_node_versions(['22.23.4', '24.22.1'])

    def test_dotnet_overlay_retains_vulnerable_runtime_fails(self):
        lock = json.loads((ROOT / 'guest/images/github-extra-tools.json').read_text())
        sdks = [t['version'] for t in lock['tools'] if t['name'] == 'dotnet']
        current = ['8.0.32', '9.0.21', '10.0.13']
        check.require_dotnet(sdks, current, lock)
        for actual_sdks, runtimes in [(sdks + ['8.0.425'], current), (sdks, current + ['8.0.31'])]:
            with self.assertRaises(ValueError):
                check.require_dotnet(actual_sdks, runtimes, lock)

    def test_native_cohort_only_existing_members(self):
        policy = json.loads((ROOT / 'guest/images/nss-security.json').read_text())
        for release in policy['releases'].values():
            rows = [{'package': 'libnss3', 'source': 'nss', 'version': 'old', 'status': 'installed'},
                    {'package': 'unrelated', 'source': 'other', 'version': 'exact', 'status': 'installed'}]
            self.assertEqual([e['package'] for e in nss.select(rows, release)], ['libnss3'])
            rows.append({'package': 'unknown-nss-package', 'source': 'nss', 'version': 'old', 'status': 'installed'})
            with self.assertRaises(ValueError):
                nss.select(rows, release)

    def test_authenticated_inputs_reject_old_corruption(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            target = root / 'official.tar.gz'
            target.write_bytes(b'fixed official bytes')
            entry = {'filename': target.name, 'sha256': hashlib.sha256(target.read_bytes()).hexdigest()}
            runtime.verify(root, [entry])
            target.write_bytes(b'old official bytes')
            with self.assertRaises(ValueError):
                runtime.verify(root, [entry])
            entry['filename'] = '../outside'
            with self.assertRaises(ValueError):
                runtime.verify(root, [entry])

    def test_effective_inventory_preserves_non_node_exactly(self):
        original = {'default_go': '1.27.3', 'snapshot': 'parent-snapshot', 'tools': [{'name': 'go', 'version': 'exact'}, {'name': 'node', 'version': 'old'}, {'name': 'python', 'version': 'exact'}]}
        before = copy.deepcopy(original)
        nodes = [{'name': 'node', 'version': '22.23.4'}, {'name': 'node', 'version': '24.22.1'}]
        result = metadata.updated_inventory(original, nodes, '24.22.1')
        self.assertEqual(original, before)
        self.assertEqual([t for t in result['tools'] if t['name'] != 'node'], [t for t in before['tools'] if t['name'] != 'node'])
        self.assertEqual(result['snapshot'], 'parent-snapshot')
        check.require_node_versions([t['version'] for t in result['tools'] if t['name'] == 'node'])

    def test_extra_replacement_removes_old_runtime_roots_before_extract(self):
        # Compile and inspect the real function: a nested import used to shadow
        # shutil before the cleanup branch, which pure inventory tests miss.
        module = load('extras', 'guest/install-github-extra-tools.py')
        self.assertNotIn('shutil', module.main.__code__.co_varnames)
        source = (ROOT / 'guest/install-github-extra-tools.py').read_text()
        self.assertLess(source.index('if args.replace_installed:'), source.index("elif tool['name'] == 'dotnet':"))
        for root in ['/usr/share/dotnet', '/opt/firefox', '/opt/rustup']:
            self.assertIn(root, source)

    def test_runner_pins_coherent_and_eol_explicit(self):
        lock = json.loads((ROOT / 'guest/images/runner-linux-x64.json').read_text())
        self.assertEqual(lock['version'], '2.338.1')
        self.assertEqual(lock['bundled_node24'], '24.22.1')
        self.assertIn('EOL', lock['bundled_node20_status'])
        for name in ['scripts/build-image.sh', 'scripts/build-rocky-image.sh']:
            source = (ROOT / name).read_text()
            self.assertIn('runner=' + lock['version'], source)
            self.assertIn('runner_sha=' + lock['sha256'], source)
        ubuntu = json.loads((ROOT / 'guest/images/ubuntu-2604.json').read_text())
        self.assertEqual((ubuntu['runner_version'], ubuntu['runner_sha256']), (lock['version'], lock['sha256']))


if __name__ == '__main__':
    unittest.main()
