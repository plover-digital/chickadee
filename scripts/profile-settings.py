#!/usr/bin/env python3
"""Installer/preflight resource calculations; Go validates the config first."""
import json, os, pathlib, subprocess, sys

def settings(c):
    if c.get('profiles'):
        profiles = [dict(c['resource_classes'][p['resources']], **c['images'][p['image']], max_vms=p['max_vms']) for p in c['profiles'].values()]
        limits = c['limits']
        count, cpu, memory = limits['max_vms'], limits['max_vcpus'], limits['max_memory_mib']
    else:
        profiles = [dict(c, path=c['image_dir'])]
        count, cpu, memory = c['max_vms'], c['max_vms']*c['cpus'], c['max_vms']*c['memory_mib']
    return profiles, count, cpu, memory

def main():
    c=json.load(open(sys.argv[2])); profiles,count,cpu,memory=settings(c)
    if sys.argv[1]=='resources':
        print(f'[Service]\nMemoryMax={memory+count*512+512}M\nMemorySwapMax=0\nCPUQuota={cpu*100}%\nTasksMax={64+count*(max(p["cpus"] for p in profiles)+160)}')
        return
    assert sys.argv[1]=='preflight'
    assert c['state_dir']=='/var/lib/chickadee', 'installer uses a fixed state directory'
    total=int(next(line.split()[1] for line in pathlib.Path('/proc/meminfo').read_text().splitlines() if line.startswith('MemTotal:')))//1024
    assert memory+count*512+512 <= total-1024, 'guest budget plus overhead must leave at least 1 GiB for host'
    flags=set(next(line.split(':',1)[1].split() for line in pathlib.Path('/proc/cpuinfo').read_text().splitlines() if line.startswith('flags')))
    checked=set()
    for p in profiles:
        root=pathlib.Path(p['path'])
        # Root-owned artifacts cannot be changed by the emulator/service account.
        for file in [root, *root.iterdir()]:
            st=file.stat()
            assert st.st_uid==0 and not st.st_mode & 0o022, 'image bundles must be root-owned and not group/other writable'
        m=json.load(open(root/'manifest.json'))
        assert p['disk_gib']==m['disk_gib'], 'disk size must match the built filesystem'
        if c.get('profiles'):
            assert m.get('os')==p['os'] and m.get('os_version')==p['version'] and m.get('architecture')=='amd64', 'manifest OS/version/architecture mismatch'
            assert m.get('machine')==p['machine'], 'manifest QEMU machine mismatch'
        if m.get('minimum_cpu')=='x86-64-v3':
            required={'avx','avx2','bmi1','bmi2','f16c','fma','abm','movbe','xsave','cx16','lahf_lm','popcnt','sse4_1','sse4_2','ssse3'}
            assert required <= flags, 'image requires x86-64-v3; QEMU uses -cpu host'
        if root in checked:
            continue
        checked.add(root)
        lines=(root/'SHA256SUMS').read_text().splitlines()
        entries=set()
        for line in lines:
            digest, name=line.split(maxsplit=1);name=name.lstrip('*')
            assert len(digest)==64 and all(ch in '0123456789abcdef' for ch in digest)
            assert name and '/' not in name and name not in ('.','..') and name not in entries, 'unsafe checksum filename'
            entries.add(name)
        assert {'base.qcow2','manifest.json'} <= entries
        for name in ('vmlinuz','initrd'):
            resolved=(root/name).resolve(strict=True)
            assert resolved.parent==root.resolve() and resolved.name in entries, 'boot artifacts must be checksummed in the bundle'
        subprocess.run(['sha256sum','--check','--status','SHA256SUMS'],cwd=root,check=True)
    v=os.statvfs(c['state_dir'])
    assert v.f_bavail*v.f_frsize >= count*(max(p['disk_gib'] for p in profiles)+1)*2**30, 'insufficient space for bounded overlays'

if __name__=='__main__':
    main()
