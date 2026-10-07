#!/usr/bin/env python3
"""Canonical policy reproduction only. Invoke inside private root net AND mount namespaces."""
import argparse
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import sys
import tempfile
import time

SERVER = """import socket,sys
s=socket.socket(socket.AF_INET6 if ':' in sys.argv[1] else socket.AF_INET)
s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1)
s.bind((sys.argv[1],18444));s.listen(8)
while True:
 c,a=s.accept();c.close()
"""
CONNECT = """import socket,sys
try:
 s=socket.create_connection((sys.argv[1],18444),timeout=.4);s.close()
except OSError:sys.exit(2)
"""
SPOOF = """import socket,struct
def checksum(data):
 v=sum(struct.unpack('!'+str(len(data)//2)+'H',data));v=(v>>16)+(v&65535);v+=(v>>16);return (~v)&65535
s=socket.socket(socket.AF_INET,socket.SOCK_RAW,socket.IPPROTO_RAW)
a=socket.inet_aton('10.203.1.6');b=socket.inet_aton('203.0.113.2')
t=struct.pack('!HHIIBBHHH',49199,18444,1,0,80,2,1024,0,0)
pseudo=a+b+struct.pack('!BBH',0,6,len(t))
t=t[:16]+struct.pack('!H',checksum(pseudo+t))+t[18:]
h=struct.pack('!BBHHHBBH4s4s',69,0,40,1,0,64,6,0,a,b)
s.sendto(h+t,('203.0.113.2',18444))
"""


def require_private_namespaces(proc=Path('/proc')):
    if os.geteuid()!=0:raise ValueError('root required')
    for kind in ('net','mnt'):
        if os.stat(proc/'self/ns'/kind).st_ino==os.stat(proc/'1/ns'/kind).st_ino:
            raise ValueError('private network and mount namespaces required')


def counter_rules(plan):
    # Every canonical drop gets a counter, with the original predicate/verdict intact.
    return re.sub(r'\bdrop\b', 'counter drop', plan)


def run(args, namespace=None, check=True):
    return subprocess.run((['ip','netns','exec',namespace] if namespace else [])+args,
                          capture_output=True,text=True,timeout=4,check=check)


def counters():
    data=json.loads(run(['nft','-j','list','table','inet','chickadee']).stdout)
    result={}
    for entry in data['nftables']:
        rule=entry.get('rule',{})
        for expression in rule.get('expr',[]):
            if 'counter' in expression:
                result[(rule['chain'],rule['handle'])]=expression['counter']['packets']
    return result


def connect(namespace,address):
    return run([sys.executable,'-c',CONNECT,address],namespace,False).returncode==0


def main():
    argparse.ArgumentParser(description=__doc__).parse_args()
    require_private_namespaces()
    signal.signal(signal.SIGALRM,lambda *_: (_ for _ in ()).throw(TimeoutError('suite deadline')))
    signal.alarm(55)
    servers=[];names=['outer','guest1','guest2'];results=[]
    try:
        # /run is hidden by a private tmpfs: ip-netns bind mounts cannot reach host /run/netns.
        run(['mount','-t','tmpfs','-o','size=1m,mode=755','tmpfs','/run'])
        Path('/run/netns').mkdir()
        run(['ip','link','set','lo','up'])
        for namespace in names:
            run(['ip','netns','add',namespace]);run(['ip','link','set','lo','up'],namespace)
        for device,peer,namespace,host,guest in [('eth0','wan','outer','203.0.113.1/24','203.0.113.2/24'),('ck01','g1','guest1','10.203.1.1/30','10.203.1.2/30'),('ck02','g2','guest2','10.203.2.1/30','10.203.2.2/30')]:
            run(['ip','link','add',device,'type','veth','peer','name',peer])
            run(['ip','link','set',peer,'netns',namespace])
            run(['ip','addr','add',host,'dev',device]);run(['ip','link','set',device,'up'])
            run(['ip','addr','add',guest,'dev',peer],namespace);run(['ip','link','set',peer,'up'],namespace)
            run(['ip','route','add','default','via',host.split('/')[0]],namespace)
        # Match deployed hosts' reverse-route availability: loose rp_filter
        # must not discard the spoof probe before the source-guard hook.
        run(['ip','route','add','default','via','203.0.113.2'])
        for address in ('10.55.0.2/24','169.254.77.2/24'):
            run(['ip','addr','add',address,'dev','wan'],'outer')
            run(['ip','route','add',str(__import__('ipaddress').ip_interface(address).network),'dev','eth0'])
        # Explicit IPv6 routing means the forwarding drops really execute.
        for namespace,device,address in [(None,'eth0','2001:db8:1::1/64'),('outer','wan','2001:db8:1::2/64'),(None,'ck01','2001:db8:2::1/64'),('guest1','g1','2001:db8:2::2/64')]:
            run(['ip','-6','addr','add',address,'dev',device,'nodad'],namespace)
        # IPv6 INPUT is intentionally blocked, including neighbor discovery.
        # Static neighbors in this isolated fixture make forwarding tests reach
        # the intended hook rather than failing earlier during NDP resolution.
        def mac(namespace, device):
            return json.loads(run(['ip','-j','link','show','dev',device],namespace).stdout)[0]['address']
        for namespace, device, address, peer_ns, peer_dev in [
            (None,'eth0','2001:db8:1::2','outer','wan'),
            ('outer','wan','2001:db8:1::1',None,'eth0'),
            (None,'ck01','2001:db8:2::2','guest1','g1'),
            ('guest1','g1','2001:db8:2::1',None,'ck01')]:
            run(['ip','-6','neigh','replace',address,'lladdr',mac(peer_ns,peer_dev),'nud','permanent','dev',device],namespace)
        run(['ip','-6','route','add','default','via','2001:db8:2::1'],'guest1')
        run(['ip','-6','route','add','2001:db8:2::/64','via','2001:db8:1::1'],'outer')
        run(['sysctl','-q','-w','net.ipv4.ip_forward=1'])
        run(['sysctl','-q','-w','net.ipv6.conf.all.forwarding=1'])
        plan=run(['bash',str(Path(__file__).with_name('network.sh')),'plan','eth0']).stdout
        with tempfile.NamedTemporaryFile(mode='w') as file:
            file.write(counter_rules(plan));file.flush();run(['nft','-f',file.name])
        for namespace,address in [('outer','203.0.113.2'),('outer','10.55.0.2'),('outer','169.254.77.2'),('outer','2001:db8:1::2'),('guest2','10.203.2.2'),('guest1','10.203.1.2'),('guest1','2001:db8:2::2'),(None,'10.203.1.1')]:
            cmd=(['ip','netns','exec',namespace] if namespace else [])+[sys.executable,'-c',SERVER,address]
            servers.append(subprocess.Popen(cmd,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL))
        time.sleep(.15)
        for namespace,address in [('outer','203.0.113.2'),('outer','10.55.0.2'),('outer','169.254.77.2'),('outer','2001:db8:1::2'),('guest2','10.203.2.2'),('guest1','10.203.1.2'),('guest1','2001:db8:2::2'),(None,'10.203.1.1')]:
            if not connect(namespace,address):raise ValueError('listener positive control failed')
        if not connect('guest1','203.0.113.2'):raise ValueError('public NAT control failed')
        cases=[('host-input','guest1','10.203.1.1','input'),('cross-guest','guest1','10.203.2.2','forward'),('private-ipv4','guest1','10.55.0.2','forward'),('link-local','guest1','169.254.77.2','forward'),('ipv6-outbound','guest1','2001:db8:1::2','forward'),('ipv6-inbound','outer','2001:db8:2::2','forward'),('unsolicited-inbound','outer','10.203.1.2','forward')]
        controls={'host-input':None,'cross-guest':'guest2','private-ipv4':'outer','link-local':'outer','ipv6-outbound':'outer','ipv6-inbound':'guest1','unsolicited-inbound':'guest1'}
        for label,namespace,address,chain in cases:
            if not connect(controls[label],address):raise ValueError('before listener control failed')
            before=counters()
            if connect(namespace,address):raise ValueError('blocked canary reachable')
            after=counters();delta=sum(count-before.get(key,0) for key,count in after.items() if key[0]==chain)
            if delta<1:raise ValueError('no attributable drop counter delta: '+label)
            if not connect(controls[label],address):raise ValueError('after listener control failed')
            results.append({'case':label,'drop_packets':delta})
        before=counters();run([sys.executable,'-c',SPOOF],'guest1');time.sleep(.05)
        after=counters();delta=sum(count-before.get(key,0) for key,count in after.items() if key[0]=='forward')
        if delta<1:raise ValueError('spoof drop not observed')
        results.append({'case':'source-spoof','drop_packets':delta})
        print(json.dumps({'schema_version':1,'policy_reproduction':'passed','live_host_acceptance':'not_tested','cases':results}))
    finally:
        for server in servers:
            server.terminate()
        for server in servers:
            try:server.wait(timeout=1)
            except subprocess.TimeoutExpired:server.kill();server.wait(timeout=1)
        for namespace in names:
            run(['ip','netns','del',namespace],check=False)
        signal.alarm(0)

if __name__=='__main__':
    try:main()
    except Exception:
        print(json.dumps({'schema_version':1,'policy_reproduction':'failed','live_host_acceptance':'not_tested'}))
        raise SystemExit(1)
