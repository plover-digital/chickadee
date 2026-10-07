import importlib.util
import io
from contextlib import redirect_stdout
import json
from pathlib import Path
import subprocess
import tempfile
import time
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location('performance', Path(__file__).with_name('performance-benchmark.py'))
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


class PerformanceBenchmark(unittest.TestCase):
    def test_parallelism_partitions_fixed_total_work(self):
        for total in (128, 512, 1024):
            for processes in (1, 2, 4):
                parts = m.partition(total, processes)
                self.assertEqual(sum(parts), total)
                self.assertEqual(len(parts), processes)
                self.assertLessEqual(max(parts) - min(parts), 1)
        self.assertEqual(len(m.cpu_fixture()), m.MIB)
        compression = m.cpu_work('compression_roundtrip', 2)
        hashed = m.cpu_work('cache_resident_sha256', 2)
        self.assertTrue(compression['validated'])
        self.assertEqual(compression['fixture_sha256'], hashed['fixture_sha256'])
        self.assertEqual(compression['bytes'], 2 * m.MIB)

    def test_disk_correctness_and_corruption_detection(self):
        with tempfile.TemporaryDirectory() as directory:
            written = m.write_work(directory, 1)
            read = m.read_work(directory, 1, written['sha256'])
            self.assertEqual(written['bytes'], read['bytes'])
            with (Path(directory) / 'sequential.bin').open('r+b') as stream:
                stream.write(b'corrupt')
            with self.assertRaises(ValueError):
                m.read_work(directory, 1, written['sha256'])
            metadata = m.metadata_work(directory, 8)
            self.assertEqual(metadata['files'], 8)
            self.assertFalse((Path(directory) / 'small-files').exists())

    def test_expired_deadline_never_starts_work(self):
        with mock.patch.object(m.subprocess, 'Popen') as start:
            with self.assertRaises(subprocess.TimeoutExpired):
                m.run_case('compression_roundtrip', [['--units', '1']], time.monotonic() - 1)
            start.assert_not_called()

    def test_context_excludes_guest_double_count_and_private_identity(self):
        a = {'cpu_ticks': [0] * 8, 'pressure': {'cpu': None, 'memory': None, 'io': None}, 'memory': {}}
        b = dict(a, cpu_ticks=[100, 0, 20, 60, 10, 0, 0, 10])
        self.assertEqual(m.context_delta(a, b)['steal_percent'], 5)
        files = {'/etc/os-release': 'ID=ubuntu\nVERSION_ID="26.04"\nHOSTNAME=private-machine\n',
                 '/proc/cpuinfo': 'model name : public-cpu-model\nSerial : private-serial\n'}
        with mock.patch.object(m, 'read_text', side_effect=lambda path: files.get(path, '')):
            data = json.dumps(m.environment())
        self.assertNotIn('private-machine', data)
        self.assertNotIn('private-serial', data)
        self.assertNotIn('hostname', data)

    def test_longest_mount_match_and_component_boundaries(self):
        info = ('1 0 8:1 / / rw - ext4 /dev/private-root rw\n'
                '2 1 0:2 / /tmp rw - tmpfs private-source rw\n'
                '3 1 8:2 / /workspace rw - xfs /dev/private-disk rw\n')
        self.assertEqual(m.filesystem_info('/workspace/scratch', info),
                         {'filesystem_type': 'xfs', 'memory_backed': False})
        self.assertTrue(m.filesystem_info('/tmp/scratch', info)['memory_backed'])
        self.assertFalse(m.filesystem_info('/tmp-sibling/scratch', info)['memory_backed'])
        self.assertTrue(m.filesystem_info('/workspace/../tmp/scratch', info)['memory_backed'])
        with self.assertRaises(m.ScratchStorageError):
            m.require_disk_filesystem(m.filesystem_info('/tmp/scratch', info))
        self.assertNotIn('private', json.dumps(m.filesystem_info('/workspace/scratch', info)))
        self.assertNotIn('/workspace', json.dumps(m.filesystem_info('/workspace/scratch', info)))

    def test_mountinfo_decodes_escapes_once_and_rejects_unknown_backing(self):
        info = ('1 0 8:1 / / rw - ext4 /dev/root rw\n'
                r'2 1 0:2 / /private\040folder rw - ramfs ramfs rw' + '\n')
        self.assertEqual(m.filesystem_info('/private folder/scratch', info),
                         {'filesystem_type': 'ramfs', 'memory_backed': True})
        self.assertEqual(m.mount_path(r'/literal\134040'), r'/literal\040')
        self.assertEqual(m.mount_path(r'/tab\011newline\012'), '/tab\tnewline\n')
        overlay = '1 0 0:1 / / rw - overlay overlay rw,upperdir=/private/path\n'
        with self.assertRaises(m.ScratchStorageError):
            m.require_disk_filesystem(m.filesystem_info('/workspace', overlay))
        with self.assertRaises(m.ScratchStorageError):
            m.filesystem_info('/workspace', 'x' * (512 * 1024 + 1))

    def test_memory_backed_storage_retains_cpu_results_and_never_runs_disk(self):
        output = io.StringIO()
        def fake_case(name, *args):
            self.assertIn(name, ('compression_roundtrip', 'cache_resident_sha256'))
            return {'name': name, 'validated': True}
        with mock.patch.object(m.sys, 'argv', ['benchmark', '--output', '-']), \
             mock.patch.object(m, 'run_case', side_effect=fake_case) as cases, \
             mock.patch.object(m, 'filesystem_info', return_value={'filesystem_type': 'tmpfs', 'memory_backed': True}), \
             mock.patch.object(m, 'revision', return_value='a' * 40), redirect_stdout(output):
            self.assertEqual(m.main(), 1)
        data = json.loads(output.getvalue())
        self.assertEqual(data['schema_version'], 2)
        self.assertEqual(data['status'], 'incomplete')
        self.assertEqual(data['error'], 'memory_backed_scratch')
        self.assertEqual(cases.call_count, 6)
        self.assertEqual(len(data['results']), 6)


if __name__ == '__main__':
    unittest.main()
