#!/usr/bin/env python3
"""Operator-only bounded packet probe; pair results with host positive controls/counters."""
import argparse,ipaddress,json,socket,struct,subprocess
p=argparse.ArgumentParser(description=__doc__);p.add_argument('--public-host',required=True);args=p.parse_args()
routes=json.loads(subprocess.check_output(['ip','-j','-4','route','show','default'],text=True));gateway=routes[0]['gateway'];device=routes[0]['dev']
addr=json.loads(subprocess.check_output(['ip','-j','-4','addr','show','dev',device],text=True))[0]['addr_info'][0]['local']
if not ipaddress.ip_address(gateway) in ipaddress.ip_network('10.203.0.0/16'):raise SystemExit('not a Chickadee guest network')
destination=socket.gethostbyname(args.public_host)
if not ipaddress.ip_address(destination).is_global:raise SystemExit('public positive control requires operator-owned public host')
with socket.create_connection((destination,443),timeout=5):pass
try:
 with socket.create_connection((gateway,18444),timeout=2):pass
 raise SystemExit('FAIL: controlled host listener reachable')
except OSError:pass
# One unassigned private source outside this guest's /30; never impersonate
# another allocated TAP (.2) or spoof an external address.
spoof='.'.join(addr.split('.')[:3]+['6'])
def checksum(data):
 if len(data)%2:data+=b'\0'
 value=sum(struct.unpack('!'+str(len(data)//2)+'H',data));value=(value>>16)+(value&65535);value+=(value>>16);return (~value)&65535
src=socket.inet_aton(spoof);dst=socket.inet_aton(destination)
tcp=struct.pack('!HHIIBBHHH',49199,443,1,0,5<<4,2,1024,0,0)
pseudo=src+dst+struct.pack('!BBH',0,socket.IPPROTO_TCP,len(tcp));tcp=tcp[:16]+struct.pack('!H',checksum(pseudo+tcp))+tcp[18:]
ip=struct.pack('!BBHHHBBH4s4s',69,0,40,1,0,64,socket.IPPROTO_TCP,0,src,dst);ip=ip[:10]+struct.pack('!H',checksum(ip))+ip[12:]
with socket.socket(socket.AF_INET,socket.SOCK_RAW,socket.IPPROTO_RAW) as raw:raw.sendto(ip+tcp,(destination,443))
print(json.dumps({'public_positive_control':'passed','host_canary':'not_reachable','spoof_packets_sent':1,'acceptance':'requires_matching_host_drop_counters_and_canary_controls'}))
