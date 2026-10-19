"""Run the actual smoke script's profile/kernel/identity gates in isolation."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
SCRIPT = (ROOT / 'scripts/developer-replacement-smoke.sh').read_text()


class DeveloperReplacementChecks(unittest.TestCase):
    def run_gate(self, gate, **values):
        env = dict(os.environ)
        for key in ('UBUNTU_VERSION', 'PROFILE_SIZE', 'EXPECTED_KERNEL', 'REQUIRE_KERNEL'):
            env.pop(key, None)
        env.update(values)
        return subprocess.run(['bash', '-ec', gate], env=env, capture_output=True, text=True)

    def test_profile_selection_and_legacy_defaults(self):
        gate = SCRIPT.split('. /etc/os-release')[0]
        gate += '\nprintf "%s %s %s %s %s" "$expected_os" "$expected_cpus" "$expected_memory" "$expected_node" "$expected_clang"\n'
        self.assertEqual(self.run_gate(gate).stdout, '26.04 4 8192 v24.21.0 clang-21')
        for distro, node, clang in [('24.04', 'v22.23.3', 'clang-18'), ('26.04', 'v24.21.0', 'clang-21')]:
            for size, cpus, memory in [('small', 2, 4096), ('medium', 4, 8192)]:
                result = self.run_gate(gate, UBUNTU_VERSION=distro, PROFILE_SIZE=size)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(result.stdout, f'{distro} {cpus} {memory} {node} {clang}')
        for values in [dict(UBUNTU_VERSION='24x04'), dict(PROFILE_SIZE='large'),
                       dict(REQUIRE_KERNEL='1'), dict(EXPECTED_KERNEL='6x8x0-147-generic'),
                       dict(EXPECTED_KERNEL='6.8.0-147-generic extra')]:
            self.assertNotEqual(self.run_gate(gate, **values).returncode, 0)

    def test_exact_kernel_and_fresh_runner(self):
        kernel = SCRIPT.split('if [[ -n ${EXPECTED_KERNEL:-} ]]; then')[1].split('fi', 1)[0]
        with tempfile.TemporaryDirectory() as tmp:
            binary = Path(tmp) / 'uname'
            binary.write_text('#!/bin/sh\nprintf "%s\\n" "$ACTUAL_KERNEL"\n')
            binary.chmod(0o755)
            for actual, accepted in [('6.8.0-147-generic', True), ('6.8.0-1470-generic', False),
                                     ('7.0.0-38-generic', False)]:
                result = self.run_gate(kernel, PATH=tmp+':'+os.environ['PATH'],
                                       EXPECTED_KERNEL='6.8.0-147-generic', ACTUAL_KERNEL=actual)
                self.assertEqual(result.returncode == 0, accepted)
        identity = SCRIPT.split('if [[ ${EXPECT_REPLACEMENT:-0} == 1 ]]; then')[1].split('fi', 1)[0]
        for previous, current, accepted in [('one', 'two', True), ('one', 'one', False), ('', 'two', False)]:
            result = self.run_gate(identity, PREVIOUS_RUNNER=previous, CURRENT_RUNNER=current)
            self.assertEqual(result.returncode == 0, accepted)
