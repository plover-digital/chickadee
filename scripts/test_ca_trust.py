#!/usr/bin/env python3
import base64
import csv
import hashlib
import importlib.util
import io
import json
import os
import pathlib
import shutil
import socket
import ssl
import subprocess
import tempfile
import threading
import unittest
import zipfile

ROOT = pathlib.Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('trust', ROOT / 'guest/refresh-ca-trust.py')
trust = importlib.util.module_from_spec(spec)
spec.loader.exec_module(trust)


class TrustRefresh(unittest.TestCase):
    def test_tampered_input_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            p = pathlib.Path(directory) / 'input.whl'
            p.write_bytes(b'tampered')
            with self.assertRaisesRegex(ValueError, 'checksum mismatch'):
                trust.check_file(p, {'sha256': '0' * 64})

    def test_ensurepip_ca_refresh_keeps_version_and_valid_record(self):
        with tempfile.TemporaryDirectory() as directory:
            wheel = pathlib.Path(directory) / 'pip-25.0-py3-none-any.whl'
            with zipfile.ZipFile(wheel, 'w') as z:
                z.writestr('pip/_vendor/certifi/cacert.pem', b'old roots')
                z.writestr('pip-25.0.dist-info/METADATA', b'Version: 25.0\n')
                z.writestr('pip-25.0.dist-info/RECORD', b'old record')
            trust.refresh_bundled_wheel(wheel, b'official new roots')
            with zipfile.ZipFile(wheel) as z:
                self.assertEqual(z.read('pip/_vendor/certifi/cacert.pem'), b'official new roots')
                self.assertEqual(z.read('pip-25.0.dist-info/METADATA'), b'Version: 25.0\n')
                for name, digest, size in csv.reader(io.StringIO(z.read('pip-25.0.dist-info/RECORD').decode())):
                    if name.endswith('/RECORD'):
                        self.assertEqual((digest, size), ('', ''))
                    else:
                        expected = base64.urlsafe_b64encode(hashlib.sha256(z.read(name)).digest()).rstrip(b'=').decode()
                        self.assertEqual(digest, 'sha256=' + expected)
                        self.assertEqual(int(size), len(z.read(name)))

    @unittest.skipUnless(shutil.which('openssl'), 'OpenSSL needed')
    def test_exact_key_scope_and_node_subprocess_uses_replacement_store(self):
        node_paths = os.environ.get('CHICKADEE_TEST_NODE_BINARIES', shutil.which('node') or '').split(os.pathsep)
        with tempfile.TemporaryDirectory() as directory:
            p = pathlib.Path(directory)
            certs = []
            for i in range(2):
                cert, key = p / f'{i}.pem', p / f'{i}.key'
                subprocess.run(['openssl', 'req', '-x509', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:P-256',
                                '-nodes', '-keyout', str(key), '-out', str(cert), '-days', '1', '-subj', '/CN=localhost',
                                '-addext', 'subjectAltName=DNS:localhost'], check=True, capture_output=True)
                certs.append(cert.read_bytes())
            selected = trust.blocked_certificates(b'\n'.join(certs), {'roots': [{'spki_sha256': trust.spki(certs[0])}]})
            self.assertEqual(selected, [certs[0].strip()])
            # The matching subject on a different key must remain untouched.
            self.assertNotEqual(trust.spki(certs[0]), trust.spki(certs[1]))
            for node in filter(None, node_paths):
                for use_system in (False, True):
                    with self.subTest(node=node, use_system=use_system):
                        listener = socket.socket()
                        listener.bind(('127.0.0.1', 0))
                        listener.listen(1)
                        listener.settimeout(10)
                        port = listener.getsockname()[1]
                        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
                        context.load_cert_chain(str(p / '0.pem'), str(p / '0.key'))
                        def serve():
                            try:
                                conn, _ = listener.accept()
                                with conn, context.wrap_socket(conn, server_side=True) as secure:
                                    secure.recv(1)
                            except (ssl.SSLError, OSError):
                                pass
                            finally:
                                listener.close()
                        thread = threading.Thread(target=serve)
                        thread.start()
                        probe = "const s=require('tls').connect({host:'127.0.0.1',port:" + str(port) + ",servername:'localhost'},()=>{s.destroy();process.exit(0)});s.on('error',e=>{console.error(e.code,e.message);process.exitCode=3});s.setTimeout(5000,()=>{s.destroy();process.exitCode=4});"
                        # Runner -> action/cache Node propagation: a Node parent
                        # starts a child without manually passing trust arguments.
                        parent = "const r=require('child_process').spawnSync(process.execPath,['-e'," + json.dumps(probe) + "]);process.stderr.write(r.stderr);process.exit(r.status ?? 5)"
                        env = {k: v for k, v in os.environ.items() if k not in ('NODE_OPTIONS', 'NODE_EXTRA_CA_CERTS', 'SSL_CERT_FILE', 'SSL_CERT_DIR')}
                        env.update(SSL_CERT_FILE=str(p / '0.pem'), SSL_CERT_DIR=str(p / 'empty-cadir'))
                        env['NODE_OPTIONS'] = '--use-openssl-ca' if use_system else '--use-bundled-ca'
                        result = subprocess.run([node, '-e', parent], env=env, capture_output=True, text=True, timeout=8)
                        thread.join(timeout=10)
                        self.assertEqual(result.returncode, 0 if use_system else 3, result.stderr)


if __name__ == '__main__':
    unittest.main()
