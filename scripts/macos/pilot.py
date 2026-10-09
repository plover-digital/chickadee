#!/usr/bin/env python3
"""One trusted native macOS workflow acceptance, not a fleet worker.

Prints bounded lifecycle events. Read one JIT configuration from private stdin
after READY. Never emits credentials or renders guest diagnostics. Only the
fresh cloned image is credentialed; it is retired on every outcome.
"""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import selectors
import shutil
import socket
import stat
import subprocess
import time

PAGE = 4096
DISK = 1024*1024
CONFIG = 65536
PAYLOAD = CONFIG+PAGE
MAX_PAYLOAD = 61440
LOGS = 131072
MAX_LOGS = 512*1024


def strict_json(data):
    def pairs(values):
        result = {}
        for key, value in values:
            if key in result:
                raise ValueError('Duplicate field')
            result[key] = value
        return result
    return json.loads(data, object_pairs_hook=pairs)


def private(path, directory=False):
    s = path.lstat()
    expected = stat.S_ISDIR if directory else stat.S_ISREG
    if not expected(s.st_mode) or s.st_uid != os.getuid() or s.st_mode & 0o077:
        raise ValueError('Expected private owned path')


def persist(path, data):
    fd = os.open(path, os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW, 0o600)
    try:
        with os.fdopen(fd, 'wb', closefd=False) as f:
            f.write(data); f.flush(); os.fsync(fd)
    finally:
        os.close(fd)
    parent = os.open(path.parent, os.O_RDONLY)
    try:
        os.fsync(parent)
    finally:
        os.close(parent)


def status(fd, nonce):
    try:
        data = os.pread(fd, PAGE, PAGE).rstrip(b'\0')
        if not data:
            return None
        s = strict_json(data)
        if set(s) != {'v','nonce','type','sequence','code','log_size','log_sha256'}:
            return None
        expected = {'READY':0,'ACK':1,'RUNNER_STARTED':2,'EXIT':3}
        if s['v'] != 1 or s['nonce'] != nonce or s['type'] not in expected or s['sequence'] != expected[s['type']]:
            return None
        if type(s['code']) is not int or not 0 <= s['code'] <= 255 or type(s['log_size']) is not int or not 0 <= s['log_size'] <= MAX_LOGS:
            return None
        if s['type'] != 'EXIT' and (s['code'] or s['log_size'] or s['log_sha256']):
            return None
        if s['log_size'] and not re.fullmatch('[a-f0-9]{64}', s['log_sha256']):
            return None
        return s
    except (ValueError, TypeError, KeyError):
        return None


def deliver(fd, state, nonce, jit, runner_name):
    if not isinstance(jit, str) or not 0 < len(jit) <= 48*1024 or not re.fullmatch('[a-zA-Z0-9_.-]{1,100}', runner_name):
        raise ValueError('Invalid runner configuration')
    base64.b64decode(jit, validate=True)
    # Durable intent must precede every credential byte, including failed writes.
    persist(state/'credential-intent.json', json.dumps({'v':1,'spent':True,'runner_name':runner_name}).encode())
    payload = json.dumps({'v':1,'nonce':nonce,'type':'CONFIG','jit':jit}).encode()
    if len(payload) > MAX_PAYLOAD:
        raise ValueError('Oversize configuration')
    os.pwrite(fd, payload.ljust(MAX_PAYLOAD,b'\0'), PAYLOAD); os.fsync(fd)
    commit = json.dumps({'v':1,'nonce':nonce,'length':len(payload),'sha256':hashlib.sha256(payload).hexdigest()}).encode()
    os.pwrite(fd, commit.ljust(PAGE,b'\0'), CONFIG); os.fsync(fd)


def emit(event, **data):
    print(json.dumps({'event':event, **data}), flush=True)


def wait_status(fd, nonce, process, expected, timeout):
    deadline = time.monotonic()+timeout
    while time.monotonic() < deadline and process.poll() is None:
        s = status(fd, nonce)
        if s and s['type'] in expected:
            return s
        time.sleep(0.1)
    raise RuntimeError('VM lifecycle timeout or failure')


def run(a):
    private(a.base, True)
    a.runtime.mkdir(mode=0o700, parents=True, exist_ok=True); private(a.runtime, True)
    logs = a.runtime/'logs'; logs.mkdir(mode=0o700, exist_ok=True); private(logs, True)
    state = a.runtime/('pilot-'+secrets.token_hex(8))
    subprocess.run([str(a.native),'clone',str(a.base),str(state)], check=True, stdout=subprocess.DEVNULL, timeout=60)
    private(state, True)
    nonce = secrets.token_hex(16)
    persist(state/'runner-control.json',json.dumps({'v':1,'nonce':nonce}).encode())
    fd = os.open(state/'runner-control.raw',os.O_RDWR|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,0o600)
    os.ftruncate(fd,DISK)
    header = json.dumps({'v':1,'kind':'chickadee-runner-control','nonce':nonce}).encode()
    os.pwrite(fd,header.ljust(PAGE,b'\0'),0);os.fsync(fd)
    native = proxy = None
    sockets = []
    handles = []
    stopped = False
    started = time.monotonic()
    try:
        native_log = state/'native.log'; persist(native_log,b''); out = native_log.open('ab'); handles.append(out)
        command = [str(a.native),'boot',str(state)]
        pass_fds = ()
        if not a.warm_probe:
            if not a.deny or not a.netproxy:
                raise ValueError('Approved deny config and network proxy required')
            left,right = socket.socketpair(socket.AF_UNIX,socket.SOCK_DGRAM); sockets.extend([left,right])
            proxy_log = state/'netproxy.log'; persist(proxy_log,b''); netlog = proxy_log.open('ab'); handles.append(netlog)
            proxy = subprocess.Popen([str(a.netproxy),'-fd',str(left.fileno()),'-deny',a.deny],pass_fds=(left.fileno(),),stdout=netlog,stderr=netlog)
            left.close()
            deadline = time.monotonic()+15
            while time.monotonic() < deadline and proxy.poll() is None:
                if b'NET_PROXY_READY' in proxy_log.read_bytes()[:4096]:
                    break
                time.sleep(0.1)
            else:
                raise RuntimeError('Network proxy failed readiness')
            command.extend(['--network-fd',str(right.fileno())]); pass_fds=(right.fileno(),)
        native = subprocess.Popen(command,pass_fds=pass_fds,stdout=out,stderr=out)
        for s in sockets:s.close()
        wait_status(fd,nonce,native,{'READY'},90)
        emit('READY',vm_id=state.name,boot_seconds=round(time.monotonic()-started,3),network=not a.warm_probe)
        if a.warm_probe:
            return 0
        selector = selectors.DefaultSelector(); selector.register(0,selectors.EVENT_READ)
        if not selector.select(120):
            raise RuntimeError('Configuration input timeout')
        line = os.read(0,64*1024+1)
        while not line.endswith(b'\n') and len(line) <= 64*1024:
            if not selector.select(5):raise RuntimeError('Incomplete configuration input')
            more=os.read(0,64*1024+1-len(line))
            if not more:break
            line+=more
        selector.close()
        if len(line)>64*1024:
            raise ValueError('Oversize configuration input')
        data = strict_json(line)
        if set(data) != {'jit','runner_name'}:
            raise ValueError('Unexpected configuration fields')
        injected = time.monotonic()
        deliver(fd,state,nonce,data['jit'],data['runner_name']); data.clear(); line=b''
        emit('CONFIG_DELIVERED')
        seen = set(); deadline = time.monotonic()+a.job_timeout
        final = None
        while time.monotonic()<deadline and native.poll() is None:
            s = status(fd,nonce)
            if s and s['sequence'] >= 1 and s['type'] not in seen:
                seen.add(s['type']);emit(s['type'],after_jit_seconds=round(time.monotonic()-injected,3),code=s['code'])
                if s['type']=='EXIT':final=s;break
            time.sleep(0.1)
        if final is None:
            raise RuntimeError('Runner lifecycle timeout')
        if final['log_size']:
            data = os.pread(fd,final['log_size'],LOGS)
            if len(data)==final['log_size'] and hashlib.sha256(data).hexdigest()==final['log_sha256']:
                persist(logs/(state.name+'.log.gz'),data);emit('DIAGNOSTICS_SAVED',bytes=len(data))
        return final['code']
    finally:
        if native is not None:
            if native.poll() is None:native.terminate()
            try:
                native.wait(timeout=30)
                stopped = native.returncode==0 and b'VM_STOPPED' in (state/'native.log').read_bytes()[:8192]
            except subprocess.TimeoutExpired:
                stopped=False
        else:
            stopped=True
        if proxy is not None:
            if proxy.poll() is None:proxy.terminate()
            try:proxy.wait(timeout=10)
            except subprocess.TimeoutExpired:stopped=False
        for s in sockets:s.close()
        for f in handles:f.close()
        os.close(fd)
        if stopped:
            # Preserve bounded host lifecycle logs outside the disposable VM state.
            for name in ['native.log','netproxy.log']:
                if (state/name).exists():persist(logs/(state.name+'-'+name),(state/name).read_bytes()[:8192])
            shutil.rmtree(state);emit('VM_DESTROYED',vm_id=state.name)
        else:
            emit('EXIT_UNCONFIRMED_STATE_RETAINED',vm_id=state.name)
            raise RuntimeError('VM exit unconfirmed; capacity/state retained')


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--base',type=Path,required=True)
    p.add_argument('--runtime',type=Path,required=True)
    p.add_argument('--native',type=Path,required=True)
    p.add_argument('--netproxy',type=Path)
    p.add_argument('--deny')
    p.add_argument('--warm-probe',action='store_true')
    p.add_argument('--job-timeout',type=int,default=600)
    a=p.parse_args()
    if os.uname().sysname!='Darwin' or not 1 <= a.job_timeout <= 900:
        p.error('Requires macOS and bounded timeout')
    try:
        raise SystemExit(run(a))
    except Exception:
        emit('PILOT_FAILED');raise SystemExit(1)


if __name__=='__main__':main()
