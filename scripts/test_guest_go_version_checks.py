"""Exercise real guest smoke assertions with a controlled Go launcher."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


class GuestGoVersionChecks(unittest.TestCase):
    def test_version_assertions_reject_prefix_and_regex_lookalikes(self):
        root = Path(__file__).resolve().parents[1]
        with tempfile.TemporaryDirectory() as directory:
            launcher = Path(directory) / 'go'
            launcher.write_text('#!/bin/sh\n'
                                'if [ "$*" = "env GOVERSION" ]; then\n'
                                '  printf "%s\\n" "$MOCK_GO_VERSION"\n'
                                'elif [ "$*" = "version" ]; then\n'
                                '  printf "go version %s linux/amd64\\n" "$MOCK_GO_VERSION"\n'
                                'else exit 99; fi\n')
            launcher.chmod(0o755)
            for filename in ['scripts/developer-replacement-smoke.sh',
                             '.github/workflows/ubuntu-2604-smoke.yml',
                             'scripts/rocky-replacement-smoke.sh',
                             '.github/workflows/rocky-replacement-smoke.yml']:
                checks = [line.strip() for line in (root / filename).read_text().splitlines()
                          if 'go env GOVERSION' in line or 'go version |' in line]
                self.assertEqual(len(checks), 2 if filename.endswith("rocky-replacement-smoke.yml") else 1)
                for version, accepted in [('go1.27.2', True), ('go1.27.20', False),
                                          ('go1x27y2', False), ('go1.27.2rc1', False)]:
                    with self.subTest(file=filename, version=version):
                        env = dict(os.environ, PATH=directory + ':' + os.environ['PATH'],
                                   MOCK_GO_VERSION=version)
                        result = subprocess.run(['bash', '-ec', '\n'.join(checks)], env=env,
                                                capture_output=True, text=True)
                        self.assertEqual(result.returncode == 0, accepted)
