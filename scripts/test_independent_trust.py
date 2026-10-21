#!/usr/bin/env python3
import importlib.util
import json
import pathlib
import subprocess
import tempfile
import unittest
from unittest.mock import patch

ROOT = pathlib.Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('independent', ROOT / 'scripts/check-independent-trust.py')
probe = importlib.util.module_from_spec(spec)
spec.loader.exec_module(probe)
trust = probe.trust
POLICY = json.loads((ROOT / 'guest/images/ca-trust.json').read_text())


class IndependentTrust(unittest.TestCase):
    def test_installed_java_package_without_pending_triggers_is_noop(self):
        with patch.object(trust, 'run', return_value=subprocess.CompletedProcess([], 0, 'ca-certificates-java\tinstalled\t\n')) as run:
            self.assertFalse(trust.complete_java_triggers())
            self.assertEqual(run.call_count, 1)
            self.assertEqual(run.call_args.args[0], 'dpkg-query')

    def test_pending_java_trigger_is_processed_exactly(self):
        with patch.object(trust, 'run', side_effect=[subprocess.CompletedProcess([], 0, 'ca-certificates-java\ttriggers-pending\tupdate-ca-certificates-java-fresh\n'), subprocess.CompletedProcess([], 0)]) as run:
            self.assertTrue(trust.complete_java_triggers())
            self.assertEqual(run.call_args.args, ('dpkg', '--triggers-only', 'ca-certificates-java'))

    def test_java_trigger_state_errors_are_not_suppressed(self):
        for output in ['garbage', 'other-package\tinstalled\t\n',
                       'ca-certificates-java\ttriggers-pending\t\n',
                       'ca-certificates-java\ttriggers-pending\t   \n',
                       'ca-certificates-java\tinstalled\tpending\n',
                       'ca-certificates-java\tunpacked\t\n',
                       'ca-certificates-java\ttriggers-pending\tinvalid$trigger\n']:
            with self.subTest(output=output), patch.object(trust, 'run', return_value=subprocess.CompletedProcess([], 0, output)) as run:
                with self.assertRaises(ValueError):trust.complete_java_triggers()
                self.assertEqual(run.call_count, 1)
        with patch.object(trust, 'run', side_effect=subprocess.CalledProcessError(1, 'dpkg-query')):
            with self.assertRaises(subprocess.CalledProcessError):trust.complete_java_triggers()
        with patch.object(trust, 'run', side_effect=[subprocess.CompletedProcess([], 0, 'ca-certificates-java\ttriggers-pending\tupdate-ca-certificates-java\n'), subprocess.CalledProcessError(2, 'dpkg')]):
            with self.assertRaises(subprocess.CalledProcessError):trust.complete_java_triggers()

    def test_java_shared_store_exact_key_removal_preserves_alias_der(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            real = root / 'java-17'; (real / 'bin').mkdir(parents=True)
            (real / 'lib/security').mkdir(parents=True)
            for name in ('java', 'keytool'):
                (real / 'bin' / name).touch()
            (root / 'default-java').symlink_to(real)
            blocked = POLICY['roots'][0]['spki_sha256']
            before = {'debian:incident': {'sha256': 'affected-der', 'spki_sha256': blocked},
                      # Same alias/subject words do not select an unrelated key.
                      'debian:emsign-unrelated': {'sha256': 'unchanged-der', 'spki_sha256': 'unrelated-key'}}
            store = real / 'lib/security/cacerts'; store.write_text(json.dumps(before))
            def entries(keytool, path):
                return json.loads(path.read_text())
            def command(*args, **kwargs):
                self.assertIn('-delete', args)
                target = pathlib.Path(args[args.index('-keystore') + 1])
                data = json.loads(target.read_text()); del data[args[args.index('-alias') + 1]]
                target.write_text(json.dumps(data))
                return subprocess.CompletedProcess(args, 0)
            with patch.object(trust, 'java_store_entries', side_effect=entries), patch.object(trust, 'run', side_effect=command):
                # spki validation calls OpenSSL; pin those four independently
                # to metadata here, rather than mock any alias selection.
                with patch.object(trust, 'spki', side_effect=[r['spki_sha256'] for r in POLICY['roots']]):
                    reports = trust.refresh_java_stores(POLICY, root)
            self.assertEqual(len(reports), 1)
            self.assertEqual(json.loads(store.read_text()), {'debian:emsign-unrelated': before['debian:emsign-unrelated']})
            self.assertEqual(list(reports[0]['removed']), ['debian:incident'])

    def test_java_failed_edit_keeps_original_store(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory); home = root / 'jdk'; (home / 'bin').mkdir(parents=True)
            (home / 'bin/java').touch(); (home / 'bin/keytool').touch(); (home / 'lib/security').mkdir(parents=True)
            store = home / 'lib/security/cacerts'; store.write_bytes(b'original')
            entries = {'bad': {'sha256': 'der', 'spki_sha256': POLICY['roots'][0]['spki_sha256']}}
            with patch.object(trust, 'java_store_entries', return_value=entries), patch.object(trust, 'run', side_effect=RuntimeError('edit failed')), patch.object(trust, 'spki', side_effect=[r['spki_sha256'] for r in POLICY['roots']]):
                with self.assertRaisesRegex(RuntimeError, 'edit failed'):
                    trust.refresh_java_stores(POLICY, root)
            self.assertEqual(store.read_bytes(), b'original')

    def test_java_unaffected_alias_change_aborts_before_replace(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory); home = root / 'jdk'; (home / 'bin').mkdir(parents=True)
            (home / 'bin/java').touch(); (home / 'bin/keytool').touch(); (home / 'lib/security').mkdir(parents=True)
            store = home / 'lib/security/cacerts'; store.write_bytes(b'original')
            before = {'bad': {'sha256': 'der', 'spki_sha256': POLICY['roots'][0]['spki_sha256']},
                      'keep': {'sha256': 'untouched', 'spki_sha256': 'unrelated'}}
            after = {'keep': {'sha256': 'changed', 'spki_sha256': 'unrelated'}}
            with patch.object(trust, 'java_store_entries', side_effect=[before, after]), patch.object(trust, 'run'), patch.object(trust, 'spki', side_effect=[r['spki_sha256'] for r in POLICY['roots']]):
                with self.assertRaisesRegex(ValueError, 'changed unaffected'):
                    trust.refresh_java_stores(POLICY, root)
            self.assertEqual(store.read_bytes(), b'original')

    def test_java_empty_or_unparsed_store_fails(self):
        with patch.object(trust, 'run', return_value=subprocess.CompletedProcess([], 0, b'Keystore type: JKS\n')):
            with self.assertRaisesRegex(ValueError, 'empty or unparseable'):
                trust.java_store_entries(pathlib.Path('/keytool'), pathlib.Path('/cacerts'))

    def test_java_parses_aliases_and_exact_der(self):
        root = POLICY['roots'][0]
        output = ('Alias name: case:sensitive\nEntry type: trustedCertEntry\n' + root['pem']).encode()
        with patch.object(trust, 'run', return_value=subprocess.CompletedProcess([], 0, output)), patch.object(trust, 'spki', return_value=root['spki_sha256']):
            entries = trust.java_store_entries(pathlib.Path('/keytool'), pathlib.Path('/cacerts'))
        self.assertEqual(entries['case:sensitive']['sha256'].upper(), root['sha256'])

    def test_nss_old_trusted_incident_key_visible_and_distrust_not_anchor(self):
        module = pathlib.Path('/module/libnssckbi.so')
        def command(*args, **kwargs):
            out = b''
            if args[0] == 'modutil' and '-list' in args:out = (str(module) + '\nstatus: loaded').encode()
            if args[0] == 'certutil' and '-L' in args:
                out = POLICY['roots'][3]['pem'].encode() if '-a' in args else b'Old incident anchor  C,C,C\nExplicitly blocked  p,p,p\n'
            return subprocess.CompletedProcess(args, 0, out.decode() if kwargs.get('text') else out)
        with patch.object(trust, 'run', side_effect=command), patch.object(trust, 'spki', return_value=POLICY['roots'][3]['spki_sha256']):
            anchors = probe.nss_anchors(module)
        self.assertEqual(set(anchors), {POLICY['roots'][3]['spki_sha256']})
        self.assertEqual(next(iter(anchors.values()))['trust'], 'C,C,C')

    def test_nss_empty_or_failed_module_is_not_rejection(self):
        for listing in ('', 'Some unrelated module status: loaded'):
            with patch.object(trust, 'run', return_value=subprocess.CompletedProcess([], 0, listing)):
                with self.assertRaisesRegex(ValueError, 'module identity not loaded'):
                    probe.nss_anchors(pathlib.Path('/expected/module.so'))


if __name__ == '__main__':
    unittest.main()
