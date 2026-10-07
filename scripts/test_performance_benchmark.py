import importlib.util
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


if __name__ == '__main__':
    unittest.main()
