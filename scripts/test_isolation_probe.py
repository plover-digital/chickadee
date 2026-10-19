import importlib.util
from pathlib import Path
import socket
import contextlib
import io
import json
import os
import tempfile
from unittest import mock
import unittest

spec = importlib.util.spec_from_file_location('probe', Path(__file__).with_name('isolation-probe.py'))
probe = importlib.util.module_from_spec(spec)
spec.loader.exec_module(probe)

class ProbeTests(unittest.TestCase):
    def test_ip_queries_work_when_runner_path_lacks_sbin(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'bin').mkdir()
            (root / 'sbin').mkdir()
            ip = root / 'sbin/ip'
            ip.write_text('#!/bin/sh\ncase "$*" in\n'
                          '"-j -6 route show default"|"-j -6 address show") printf "[]\\n" ;;\n'
                          '*) exit 23 ;;\nesac\n')
            ip.chmod(0o755)
            with mock.patch.dict(os.environ, PATH=str(root / 'bin')), \
                 mock.patch.object(probe, 'SYSTEM_IP_PATHS', (str(ip),)):
                for arguments in (['ip', '-j', '-6', 'route', 'show', 'default'],
                                  ['ip', '-j', '-6', 'address', 'show']):
                    self.assertEqual(json.loads(probe.command(arguments)), [])
                ip.chmod(0o644)
                with self.assertRaises(probe.ProbeCommandError) as failure:
                    probe.command(['ip', '-j', '-6', 'address', 'show'])
                self.assertEqual(failure.exception.reason, 'ip_unavailable')

    def test_command_failure_reason_does_not_leak_output(self):
        with tempfile.TemporaryDirectory() as directory:
            failing = Path(directory) / 'command'
            failing.write_text('#!/bin/sh\necho private-diagnostic-sentinel >&2\nexit 23\n')
            failing.chmod(0o755)
            output = io.StringIO()
            with mock.patch('sys.argv', ['isolation-probe.py']), \
                 mock.patch.object(probe, 'local_evidence',
                                   side_effect=lambda *args: probe.command([str(failing)])), \
                 contextlib.redirect_stdout(output):
                self.assertEqual(probe.main(), 1)
            self.assertEqual(json.loads(output.getvalue()),
                             {'schema_version': 1, 'error': 'isolation_probe_failed',
                              'reason': 'command_failed'})
            self.assertNotIn('private-diagnostic-sentinel', output.getvalue())
            self.assertNotIn(directory, output.getvalue())

    def test_limits_and_no_public_scans(self):
        for address in ['8.8.8.8', '127.0.0.1', '::', '224.0.0.1']:
            with self.assertRaises(ValueError):
                probe.canaries([{'category': 'private', 'address': address, 'port': 80}])
        with self.assertRaises(ValueError):
            probe.canaries([{'category': 'private', 'address': '10.0.0.1', 'port': True}])
        with self.assertRaises(ValueError):
            probe.canaries([{}] * 9)
    def test_failed_connection_not_proof(self):
        listener = socket.socket()
        listener.bind(('127.0.0.1', 0))
        port = listener.getsockname()[1]
        # Bound but not listening: deterministic refusal, not an arbitrary unused IP.
        self.addCleanup(listener.close)
        r = probe.probe([('gateway', '127.0.0.1', port)])
        self.assertEqual(r[0]['outcome'], 'not_reachable_requires_positive_control')
        self.assertNotIn('127.0.0.1', str(r))
    def test_snapshot_guards_and_order(self):
        source = Path(__file__).with_name('network.sh').read_text()
        prefix = source.split('cat <<RULES', 1)[1].split('RULES', 1)[0]
        guards = '\n'.join(f'  iifname "ck{i:02}" ip saddr != 10.203.{i}.2 drop' for i in range(1, 33))
        suffix = source.split('cat <<RULES', 2)[2].split('RULES', 1)[0]
        text = prefix + guards + suffix
        self.assertEqual(probe.audit_snapshot(text)['snapshot_policy_checks'], 'passed')
        # Public generic nft-list formatting: multiline chain, no rule semicolon.
        formatted = text.replace('chain input { type filter hook input priority -10; policy accept; iifname "ck*" drop; }',
                                 'chain input {\n type filter hook input priority filter - 10; policy accept;\n iifname "ck*" drop\n}')
        self.assertEqual(probe.audit_snapshot(formatted)['snapshot_policy_checks'], 'passed')
        counted = formatted.replace(' drop', ' counter packets 12 bytes 840 drop').replace(' accept\n', ' counter packets 0 bytes 0 accept\n')
        self.assertEqual(probe.audit_snapshot(counted)['snapshot_policy_checks'], 'passed')
        for bad in [counted.replace('iifname "ck*" counter packets 12 bytes 840 drop\n}',
                                    'iifname "ck*" counter packets 12 bytes 840 accept\n}', 1),
                    counted.replace('iifname "ck*" counter packets 12 bytes 840 drop\n}',
                                    'iifname "ck*" ip saddr 10.203.31.2 counter packets 12 bytes 840 drop\n}', 1)]:
            with self.assertRaises(ValueError):probe.audit_snapshot(bad)
        # The forward chain still contains the same fallback drop: not sufficient.
        missing_input = formatted.replace(' iifname "ck*" drop\n}', '\n}', 1)
        with self.assertRaises(ValueError):
            probe.audit_snapshot(missing_input)
        wrong_table = formatted.replace('table inet chickadee {', 'table inet unrelated {')
        with self.assertRaises(ValueError):
            probe.audit_snapshot(wrong_table)
        for broken in [text.replace('169.254.0.0/16', ''),
                       text.replace('iifname "ck01" ip saddr != 10.203.1.2 drop', ''),
                       text.replace('iifname "ck*" meta nfproto ipv6 drop', ''),
                       text.replace('  oifname "ck*" drop', '')]:
            with self.assertRaises(ValueError):
                probe.audit_snapshot(broken)
        guard = 'iifname "ck01" ip saddr != 10.203.1.2 drop'
        with self.assertRaises(ValueError):
            probe.audit_snapshot(text.replace(guard, '') + '\n' + guard)
    def test_live_listener_is_failure(self):
        listener = socket.socket()
        listener.bind(('127.0.0.1', 0)); listener.listen(1)
        self.addCleanup(listener.close)
        r = probe.probe([('cross-guest', '127.0.0.1', listener.getsockname()[1])])
        self.assertEqual(r[0]['outcome'], 'reachable_isolation_failure')

if __name__ == '__main__':
    unittest.main()
