#!/usr/bin/env python3
"""Measure the same checkout with isolated Go caches; emit no credentials."""
import json
import os
from pathlib import Path
import platform
import subprocess
import tempfile
import time

result = {
    'platform': os.environ['BENCH_PLATFORM'],
    'sample': int(os.environ['BENCH_SAMPLE']),
    'commit': os.environ['GITHUB_SHA'],
    'cpu_count': os.cpu_count(),
    'kernel': platform.release(),
    'go': subprocess.check_output(['go', 'version'], text=True).strip(),
    'memory_kib': int(next(line.split()[1] for line in Path('/proc/meminfo').read_text().splitlines() if line.startswith('MemTotal:'))),
}
with tempfile.TemporaryDirectory(prefix='chickadee-benchmark-') as cache:
    env = dict(os.environ, GOCACHE=cache + '/build', GOMODCACHE=cache + '/modules', GOTOOLCHAIN='local')
    timings = {}
    for name, command in [
        ('module_download', ['go', 'mod', 'download']),
        ('cold_build', ['make', 'build']),
        ('cold_test', ['make', 'test']),
        ('warm_build', ['make', 'build']),
        ('warm_test', ['make', 'test']),
    ]:
        start = time.monotonic()
        subprocess.run(command, env=env, check=True)
        timings[name] = round(time.monotonic() - start, 3)
    result['seconds'] = timings
Path('benchmark.json').write_text(json.dumps(result, indent=2) + '\n')
print(json.dumps(result, indent=2))
