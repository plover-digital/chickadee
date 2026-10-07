#!/usr/bin/env python3
"""Bounded guest evidence; blocked connections require external positive controls."""
import argparse
import ipaddress
import json
import os
import re
from pathlib import Path
import socket
import subprocess


def command(args):
    return subprocess.run(args, capture_output=True, text=True, timeout=5, check=True).stdout


def canaries(value):
    if not isinstance(value, list) or len(value) > 8:
        raise ValueError('expected at most eight canaries')
    result = []
    allowed = {'gateway', 'host-management', 'cross-guest', 'private', 'metadata', 'ipv6'}
    for entry in value:
        if set(entry) != {'category', 'address', 'port'} or entry['category'] not in allowed:
            raise ValueError('invalid canary fields')
        addr = ipaddress.ip_address(entry['address'])
        if not (addr.is_private or addr.is_link_local) or addr.is_loopback or addr.is_unspecified or addr.is_multicast:
            raise ValueError('canaries must be approved private or link-local unicast')
        if type(entry['port']) is not int or not 1 <= entry['port'] <= 65535:
            raise ValueError('invalid port')
        result.append((entry['category'], str(addr), entry['port']))
    return result


def probe(entries):
    results = []
    for category, address, port in entries:
        try:
            with socket.create_connection((address, port), timeout=1):
                outcome = 'reachable_isolation_failure'
        except OSError:
            outcome = 'not_reachable_requires_positive_control'
        # Do not emit private inventory addresses or arbitrary exception strings.
        results.append({'category': category, 'outcome': outcome})
    return results


def local_evidence(cpus=None, memory=None):
    mounts = json.loads(command(['findmnt', '--json', '--output', 'TARGET,FSTYPE']))
    def flatten(items):
        for item in items:
            yield item
            yield from flatten(item.get('children', []))
    entries = list(flatten(mounts['filesystems']))
    if any(m['fstype'] in {'9p', 'virtiofs', 'vboxsf', 'fuse.vmhgfs-fuse'} for m in entries):
        raise ValueError('shared host filesystem exposed')
    if Path('/run/host-docker.sock').exists():
        raise ValueError('host Docker socket exposed')
    docker = Path('/var/run/docker.sock')
    docker_local = None
    if docker.exists():
        mount = json.loads(command(['findmnt', '--json', '--target', str(docker), '--output', 'TARGET,FSTYPE']))['filesystems'][0]
        docker_local = mount['target'] in {'/', '/run', '/var/run'} and mount['fstype'] in {'ext4', 'xfs', 'btrfs', 'tmpfs'}
        if not docker_local:
            raise ValueError('Docker socket is separately mounted or shared')
    observed_cpus = os.cpu_count()
    mib = int(next(line.split()[1] for line in Path('/proc/meminfo').read_text().splitlines() if line.startswith('MemTotal:'))) // 1024
    if cpus is not None and observed_cpus != cpus:
        raise ValueError('CPU allocation mismatch')
    if memory is not None and not memory * 15 // 16 <= mib <= memory:
        raise ValueError('memory allocation mismatch')
    routes = json.loads(command(['ip', '-j', '-6', 'route', 'show', 'default']))
    addresses = json.loads(command(['ip', '-j', '-6', 'address', 'show']))
    global_count = sum(a.get('scope') == 'global' for interface in addresses for a in interface.get('addr_info', []))
    return {'cpus': observed_cpus, 'memory_mib': mib, 'shared_filesystem_present': False,
            'docker_socket_guest_local': docker_local, 'ipv6_default_routes': len(routes),
            'ipv6_global_addresses': global_count, 'hypervisor_flag': 'hypervisor' in Path('/proc/cpuinfo').read_text().split()}


def audit_snapshot(text):
    """Review exported canonical nft rules, not live packet acceptance."""
    if len(text) > 262144:
        raise ValueError('snapshot exceeds limit')
    # Only anonymous nft counter statements are non-semantic annotations here.
    # Preserve quoted strings and every predicate/verdict; named counters remain unsupported.
    text = re.sub(r'"(?:[^"\\]|\\.)*"|\bcounter\s+packets\s+[0-9]+\s+bytes\s+[0-9]+\b',
                  lambda match: match.group() if match.group().startswith('"') else '', text)
    def block_after(pattern, value):
        match = re.search(pattern, value)
        if match is None:
            raise ValueError('required policy block absent')
        start = match.end()
        depth = 1
        # Quoted strings are single tokens, so braces within strings are ignored.
        for token in re.finditer(r'"(?:[^"\\]|\\.)*"|[{}]', value[start:]):
            if token.group() == '{':
                depth += 1
            elif token.group() == '}':
                depth -= 1
                if depth == 0:
                    return value[start:start + token.start()]
        raise ValueError('unterminated policy block')
    table = block_after(r'\btable\s+inet\s+chickadee\s*{', text)
    input_chain = block_after(r'\bchain\s+input\s*{', table)
    if not re.search(r'\btype\s+filter\s+hook\s+input\s+priority\s+(?:-10|filter\s*-\s*10)\s*;', input_chain):
        raise ValueError('canonical input hook absent')
    # Entire rule must match; forwarding fallback and conditional drops cannot substitute.
    input_rules = [re.sub(r'\s+', ' ', rule.strip()) for rule in re.split(r'[;\n]', input_chain)]
    if 'iifname "ck*" drop' not in input_rules:
        raise ValueError('host-input drop absent')
    lines = [re.sub(r'\s+', ' ', line.strip()) for line in text.splitlines()]
    def require(fragment):
        for i, line in enumerate(lines):
            if fragment in line:
                return i
        raise ValueError('required filter absent')
    # Find the actual accept, never let an earlier drop masquerade as this rule.
    outbound = next((i for i, line in enumerate(lines) if 'iifname "ck*" oifname ' in line and line.endswith('accept')), None)
    if outbound is None:
        raise ValueError('outbound rule absent')
    required = ['iifname "ck*" oifname "ck*" drop', 'iifname "ck*" meta nfproto ipv6 drop',
                'oifname "ck*" meta nfproto ipv6 drop', 'iifname "ck*" fib daddr type local drop']
    for fragment in required:
        if require(fragment) >= outbound:
            raise ValueError('filter follows outbound acceptance')
    for slot in range(1, 33):
        if require(f'iifname "ck{slot:02}" ip saddr != 10.203.{slot}.2 drop') >= outbound:
            raise ValueError('source guard follows outbound acceptance')
    private = next((i for i, line in enumerate(lines) if 'iifname "ck*" ip daddr {' in line and line.endswith('drop')), None)
    if private is None or private >= outbound:
        raise ValueError('private filter missing or late')
    for subnet in ['0.0.0.0/8','10.0.0.0/8','100.64.0.0/10','127.0.0.0/8','169.254.0.0/16','172.16.0.0/12','192.168.0.0/16','198.18.0.0/15','224.0.0.0/3']:
        if subnet not in lines[private]:
            raise ValueError('private filter incomplete')
    established = require('oifname "ck*" ct state established,related accept')
    if not any(i > established and line == 'oifname "ck*" drop' for i, line in enumerate(lines)):
        raise ValueError('unsolicited inbound drop absent')
    return {'schema_version': 1, 'snapshot_policy_checks': 'passed', 'network_acceptance': 'incomplete_requires_live_controls'}

def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--nft-snapshot', type=Path, help='read-only exported canonical scripts/network.sh nft rules; alternative policies need manual review')
    p.add_argument('--canaries', type=Path, help='local operator-approved JSON file; never scans ranges')
    p.add_argument('--cpus', type=int)
    p.add_argument('--memory-mib', type=int)
    args = p.parse_args()
    try:
        if args.nft_snapshot:
            if args.nft_snapshot.stat().st_size > 262144:
                raise ValueError('snapshot exceeds limit')
            print(json.dumps(audit_snapshot(args.nft_snapshot.read_text()), sort_keys=True))
            return 0
        entries = []
        if args.canaries:
            if args.canaries.stat().st_size > 4096:
                raise ValueError('canary file exceeds limit')
            entries = canaries(json.loads(args.canaries.read_text()))
        result = {'schema_version': 1, 'guest': local_evidence(args.cpus, args.memory_mib), 'canaries': probe(entries),
                  'network_acceptance': 'incomplete_requires_host_controls',
                  'limitations': ['No guest-only negative probe proves filtering.', 'KVM and hard host limits require operator process/cgroup evidence.']}
        print(json.dumps(result, sort_keys=True))
        return int(any(r['outcome'] == 'reachable_isolation_failure' for r in result['canaries']))
    except (ValueError, OSError, subprocess.SubprocessError, KeyError):
        print(json.dumps({'schema_version': 1, 'error': 'isolation_probe_failed'}))
        return 1

if __name__ == '__main__':
    raise SystemExit(main())
