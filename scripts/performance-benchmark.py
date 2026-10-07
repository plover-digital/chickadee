#!/usr/bin/env python3
"""Bounded, deterministic Linux guest microbenchmarks; no host identities/secrets."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import random
import resource
import re
import shutil
import ssl
import subprocess
import sys
import tempfile
import time
import zlib

VERSION = 2
MIB = 1024 * 1024
SEED = 20261006
DEADLINE_SECONDS = 75


def fixture():
    return random.Random(SEED).randbytes(MIB)


def cpu_fixture():
    # Exactly one MiB: half seeded entropy, half a repeated deterministic pattern.
    data = fixture()
    pattern = b'chickadee-benchmark-v1\x00'
    return data[:MIB // 2] + (pattern * (MIB // len(pattern) + 1))[:MIB // 2]


def partition(total, processes):
    if total < processes or processes not in (1, 2, 4):
        raise ValueError('invalid fixed-work partition')
    return [total // processes + (index < total % processes) for index in range(processes)]


def measured(operation):
    started = time.monotonic()
    checks = operation()
    usage = resource.getrusage(resource.RUSAGE_SELF)
    return dict(wall_seconds=time.monotonic() - started,
                user_seconds=usage.ru_utime, system_seconds=usage.ru_stime,
                max_rss_kib=usage.ru_maxrss, checks=checks)


def cpu_work(kind, units):
    data = cpu_fixture()
    expected = hashlib.sha256(data).hexdigest()
    compressed_bytes = 0
    for _ in range(units):
        if kind == 'compression_roundtrip':
            encoded = zlib.compress(data, level=6)
            if zlib.decompress(encoded) != data:
                raise ValueError('compression correctness failed')
            compressed_bytes += len(encoded)
        elif kind == 'cache_resident_sha256':
            if hashlib.sha256(data).hexdigest() != expected:
                raise ValueError('hash correctness failed')
        else:
            raise ValueError('invalid CPU workload')
    return dict(validated=True, units=units, bytes=units * len(data),
                fixture_sha256=expected, compressed_bytes=compressed_bytes)


def write_work(directory, size_mib):
    block = fixture()
    digest = hashlib.sha256()
    path = Path(directory) / 'sequential.bin'
    with path.open('xb', buffering=0) as stream:
        for _ in range(size_mib):
            if stream.write(block) != len(block):
                raise ValueError('short sequential write')
            digest.update(block)
        os.fdatasync(stream.fileno())
    if path.stat().st_size != size_mib * MIB:
        raise ValueError('write size correctness failed')
    return dict(validated=True, bytes=size_mib * MIB, sha256=digest.hexdigest(),
                durability='fdatasync', buffering='OS_page_cache')


def read_work(directory, size_mib, expected):
    digest = hashlib.sha256()
    count = 0
    with (Path(directory) / 'sequential.bin').open('rb', buffering=0) as stream:
        while True:
            block = stream.read(MIB)
            if not block:
                break
            digest.update(block)
            count += len(block)
    if count != size_mib * MIB or digest.hexdigest() != expected:
        raise ValueError('read correctness failed')
    return dict(validated=True, bytes=count, sha256=digest.hexdigest(),
                cache='warm_guest_page_cache_expected', includes_sha256=True)


def metadata_work(directory, count=512):
    path = Path(directory) / 'small-files'
    path.mkdir(mode=0o700)
    suffix = fixture()[:4092]
    digest = hashlib.sha256()
    for index in range(count):
        data = index.to_bytes(4, 'little') + suffix
        with (path / ('f%04d' % index)).open('xb') as stream:
            if stream.write(data) != 4096:
                raise ValueError('short small-file write')
    fd = os.open(path, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(fd)
        for index in range(count):
            file = path / ('f%04d' % index)
            data = index.to_bytes(4, 'little') + suffix
            if file.stat().st_size != 4096 or file.read_bytes() != data:
                raise ValueError('small-file correctness failed')
            digest.update(data)
            file.unlink()
        os.fsync(fd)
    finally:
        os.close(fd)
    if any(path.iterdir()):
        raise ValueError('small-file cleanup failed')
    path.rmdir()
    return dict(validated=True, files=count, bytes=count * 4096,
                sha256=digest.hexdigest(), directory_fsyncs=2,
                file_data='buffered_not_individually_fdatasynced')


class ScratchStorageError(ValueError):
    pass


def mount_path(value):
    # Kernel mountinfo escapes whitespace/backslashes using these octal forms.
    # Decode once: an escaped literal backslash followed by 040 is not a space.
    return re.sub(r'\\(040|011|012|134)', lambda match: chr(int(match.group(1), 8)), value)


def filesystem_info(directory, mountinfo=None):
    target = Path(directory).resolve()
    if mountinfo is None:
        try:
            with Path('/proc/self/mountinfo').open(encoding='utf-8', errors='surrogateescape') as stream:
                mountinfo = stream.read(512 * 1024 + 1)
        except OSError:
            mountinfo = ''
    if len(mountinfo) > 512 * 1024:
        raise ScratchStorageError('scratch_filesystem_unverified')
    selected = None
    depth = -1
    for line in mountinfo.splitlines():
        left, separator, right = line.partition(' - ')
        fields = left.split()
        suffix = right.split()
        if not separator or len(fields) < 6 or len(suffix) < 3:
            continue
        mount = Path(mount_path(fields[4]))
        kind = suffix[0]
        if not mount.is_absolute() or '..' in mount.parts or not re.fullmatch(r'[A-Za-z0-9_.+-]{1,32}', kind):
            continue
        if (mount == target or mount in target.parents) and len(mount.parts) >= depth:
            selected, depth = kind, len(mount.parts)
    # A layered filesystem's backing type cannot be proven from this mount line.
    memory = selected in ('tmpfs', 'ramfs') if selected else None
    if selected in ('overlay', 'aufs'):
        memory = None
    return dict(filesystem_type=selected, memory_backed=memory)


def require_disk_filesystem(info):
    if info['memory_backed'] is True:
        raise ScratchStorageError('memory_backed_scratch')
    if info['memory_backed'] is not False:
        raise ScratchStorageError('scratch_filesystem_unverified')


def read_text(path):
    try:
        return Path(path).read_text()
    except OSError:
        return ''


def snapshot():
    memory = {}
    for line in read_text('/proc/meminfo').splitlines():
        key, _, value = line.partition(':')
        if key in ('MemTotal', 'MemAvailable', 'Cached', 'Dirty', 'Writeback', 'SwapTotal', 'SwapFree'):
            memory[key + '_kib'] = int(value.split()[0])
    pressure = {}
    for kind in ('cpu', 'memory', 'io'):
        rows = {}
        for line in read_text('/proc/pressure/' + kind).splitlines():
            fields = line.split()
            rows[fields[0]] = {key: int(value) if key == 'total' else float(value)
                              for key, value in (item.split('=', 1) for item in fields[1:])}
        pressure[kind] = rows or None
    cpu = next((line.split()[1:] for line in read_text('/proc/stat').splitlines()
                if line.startswith('cpu ')), [])
    return dict(memory=memory, pressure=pressure,
                cpu_ticks=[int(value) for value in cpu[:8]])


def context_delta(before, after):
    first, last = before['cpu_ticks'], after['cpu_ticks']
    steal = None
    if len(first) == len(last) == 8:
        delta = [b - a for a, b in zip(first, last)]
        if min(delta) >= 0 and sum(delta):
            steal = 100 * delta[7] / sum(delta)
    stalls = {}
    for kind in ('cpu', 'memory', 'io'):
        a, b = before['pressure'][kind], after['pressure'][kind]
        if a is not None and b is not None:
            stalls[kind] = {row: max(0, b[row]['total'] - a[row]['total'])
                            for row in a if row in b}
    return dict(steal_percent=steal, pressure_stall_microseconds=stalls,
                before=before, after=after)


def environment():
    release = {}
    for line in read_text('/etc/os-release').splitlines():
        key, _, value = line.partition('=')
        if key in ('ID', 'VERSION_ID'):
            release[key.lower()] = value.strip('"')
    models = [line.split(':', 1)[1].strip() for line in read_text('/proc/cpuinfo').splitlines()
              if line.startswith('model name')]
    quota = read_text('/sys/fs/cgroup/cpu.max').split()
    memory_limit = read_text('/sys/fs/cgroup/memory.max').strip()
    return dict(os=release, kernel=platform.release(), architecture=platform.machine(),
                logical_cpus=os.cpu_count(), affinity_cpu_count=len(os.sched_getaffinity(0)),
                cpu_model=models[0] if models else None,
                python=platform.python_version(), python_implementation=platform.python_implementation(),
                zlib_compile=zlib.ZLIB_VERSION, zlib_runtime=zlib.ZLIB_RUNTIME_VERSION,
                openssl=ssl.OPENSSL_VERSION, cgroup_cpu_max=quota or None,
                cgroup_memory_max=memory_limit or None)


def revision():
    value = os.environ.get('GITHUB_SHA', '')
    if re.fullmatch('[0-9a-f]{40,64}', value):
        return value
    try:
        value = subprocess.check_output(['git', 'rev-parse', 'HEAD'], stderr=subprocess.DEVNULL,
                                        text=True, timeout=2).strip()
        return value if re.fullmatch('[0-9a-f]{40,64}', value) else None
    except (OSError, subprocess.SubprocessError):
        return None


def run_case(name, arguments, deadline, expected_units=None):
    if deadline <= time.monotonic():
        raise subprocess.TimeoutExpired('benchmark', 0)
    before = snapshot()
    started = time.monotonic()
    children = []
    try:
        for args in arguments:
            children.append(subprocess.Popen([sys.executable, str(Path(__file__).resolve()), '--worker', name] + args,
                            stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
                            env={'PATH': os.defpath, 'LANG': 'C'}, text=True))
        records = []
        for child in children:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise subprocess.TimeoutExpired('benchmark', 0)
            data, _ = child.communicate(timeout=remaining)
            if child.returncode != 0 or len(data) > 8192:
                raise ValueError('benchmark worker failed')
            record = json.loads(data)
            if not record['checks']['validated']:
                raise ValueError('benchmark correctness failed')
            records.append(record)
        elapsed = time.monotonic() - started
        if expected_units is not None and sum(item['checks']['units'] for item in records) != expected_units:
            raise ValueError('fixed work changed across parallelism')
        return dict(name=name, processes=len(children), wall_seconds=elapsed,
                    user_seconds=sum(item['user_seconds'] for item in records),
                    system_seconds=sum(item['system_seconds'] for item in records),
                    max_rss_kib=max(item['max_rss_kib'] for item in records),
                    rss_scope='largest_worker_lifetime_peak_not_process_tree_peak',
                    peak_rss_sum_upper_bound_kib=sum(item['max_rss_kib'] for item in records),
                    workers=records, context=context_delta(before, snapshot()))
    finally:
        for child in children:
            if child.poll() is None:
                child.kill()
        stop_deadline = time.monotonic() + 5
        for child in children:
            try:
                child.wait(timeout=max(0, stop_deadline - time.monotonic()))
            except subprocess.TimeoutExpired:
                pass  # Guest/job teardown remains responsible for a kernel-stuck process.


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', default='benchmark.json')
    parser.add_argument('--comparison', choices=('baseline', 'candidate', 'hosted'), default='baseline')
    parser.add_argument('--sample', type=int, default=1)
    parser.add_argument('--cpu-mib', type=int, choices=(32, 64, 128), default=128)
    parser.add_argument('--disk-mib', type=int, choices=(128, 256), default=256)
    parser.add_argument('--image-digest', default='')
    parser.add_argument('--scratch-dir', help='local disk-backed scratch parent; defaults to checkout/current directory')
    parser.add_argument('--worker', choices=('compression_roundtrip', 'cache_resident_sha256', 'buffered_write_fdatasync', 'cached_read_sha256', 'small_file_metadata'))
    parser.add_argument('--units', type=int, default=1)
    parser.add_argument('--directory')
    parser.add_argument('--expected-digest')
    args = parser.parse_args()
    if sys.platform != 'linux' or not 1 <= args.sample <= 100:
        parser.error('Linux and sample 1..100 required')
    if args.image_digest and not re.fullmatch('[0-9a-f]{64}', args.image_digest):
        parser.error('image digest must be SHA256 hex')
    if args.worker:
        if args.worker not in ('compression_roundtrip', 'cache_resident_sha256'):
            try:
                require_disk_filesystem(filesystem_info(args.directory))
            except (OSError, ScratchStorageError):
                return 1
        limit = 512 if args.worker == 'compression_roundtrip' else 1024
        if not 1 <= args.units <= limit:
            parser.error('worker units exceed bounded workload')
        if args.worker in ('compression_roundtrip', 'cache_resident_sha256'):
            operation = lambda: cpu_work(args.worker, args.units)
        elif args.worker == 'buffered_write_fdatasync':
            operation = lambda: write_work(args.directory, args.disk_mib)
        elif args.worker == 'cached_read_sha256':
            operation = lambda: read_work(args.directory, args.disk_mib, args.expected_digest)
        else:
            operation = lambda: metadata_work(args.directory)
        try:
            print(json.dumps(measured(operation), allow_nan=False))
            return 0
        except (OSError, ValueError):
            return 1
    started = time.monotonic()
    deadline = started + DEADLINE_SECONDS
    result = dict(schema_version=VERSION, status='complete', comparison=args.comparison,
                  sample=args.sample, source_revision=revision(),
                  script_sha256=hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
                  image_digest=args.image_digest or None, environment=environment(),
                  parameters=dict(cpu_mib=args.cpu_mib, compression_mib=args.cpu_mib * 4, hash_mib=args.cpu_mib * 8,
                                  disk_mib=args.disk_mib, block_bytes=MIB, seed=SEED,
                                  compression_level=6, process_counts=[1, 2, 4],
                                  small_files=512, small_file_bytes=4096,
                                  deadline_seconds=DEADLINE_SECONDS), results=[])
    result['storage'] = dict(filesystem_type=None, memory_backed=None)
    try:
        # CPU cases remain valid even if storage is RAM-backed or unverified.
        for kind, units in [('compression_roundtrip', args.cpu_mib * 4),
                            ('cache_resident_sha256', args.cpu_mib * 8)]:
            for processes in (1, 2, 4):
                partitions = partition(units, processes)
                result['results'].append(run_case(kind, [['--units', str(count)] for count in partitions], deadline, units))
        scratch = Path(args.scratch_dir).resolve() if args.scratch_dir else Path.cwd()
        with tempfile.TemporaryDirectory(dir=scratch, prefix='.chickadee-perf-') as directory:
            result['storage'] = filesystem_info(directory)
            require_disk_filesystem(result['storage'])
            if shutil.disk_usage(directory).free < (args.disk_mib + 16) * MIB:
                raise ValueError('insufficient temporary disk space')
            io_args = ['--directory', directory, '--disk-mib', str(args.disk_mib)]
            write = run_case('buffered_write_fdatasync', [io_args], deadline)
            result['results'].append(write)
            digest = write['workers'][0]['checks']['sha256']
            result['results'].append(run_case('cached_read_sha256', [io_args + ['--expected-digest', digest]], deadline))
            result['results'].append(run_case('small_file_metadata', [['--directory', directory]], deadline))
    except ScratchStorageError as error:
        result['status'] = 'incomplete'
        result['error'] = str(error)
    except (OSError, ValueError, subprocess.SubprocessError):
        result['status'] = 'incomplete'
        result['error'] = 'worker_failure_or_deadline_exceeded'
    result['total_wall_seconds'] = time.monotonic() - started
    payload = json.dumps(result, indent=2, allow_nan=False) + '\n'
    if args.output != '-':
        fd = os.open(args.output, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, 'w') as output:
            output.write(payload)
    print(payload, end='')
    return 0 if result['status'] == 'complete' else 1


if __name__ == '__main__':
    raise SystemExit(main())
