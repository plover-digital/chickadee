#!/usr/bin/env python3
"""Compare image package inventory to the pinned official Ubuntu hosted inventory."""
import argparse,hashlib,json,pathlib,re,urllib.request

def package_table(text):
    section=text.split('### Installed apt packages',1)[1]
    result={}
    for line in section.splitlines():
        if line.startswith('##'):break
        match=re.match(r'^\|\s*([a-z0-9][a-z0-9+.-]*)\s*\|\s*([^|]+?)\s*\|$',line)
        if match and match.group(1)!='Name':result[match.group(1)]=match.group(2)
    return result

def compare(expected,actual):
    actual=dict(actual)
    # Official inventory calls this virtual package netcat; Ubuntu installs its provider.
    for virtual,provider in {'netcat':'netcat-openbsd','upx':'upx-ucl'}.items():
        if virtual not in actual and provider in actual:actual[virtual]=actual[provider]
    missing=[];different=[]
    for name,version in sorted(expected.items()):
        if name not in actual:missing.append(name)
        elif actual[name]!=version:different.append({'name':name,'expected':version,'actual':actual[name]})
    return {'expected_packages':len(expected),'missing_packages':missing,'different_versions':different}

if __name__=='__main__':
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('bundle');parser.add_argument('--inventory',help='cached pinned official Ubuntu2404-Readme.md')
    args=parser.parse_args();bundle=pathlib.Path(args.bundle)
    lock=json.load(open(bundle/'tool-lock.json'))
    if args.inventory:data=pathlib.Path(args.inventory).read_bytes()
    else:
        url=f"https://raw.githubusercontent.com/actions/runner-images/{lock['upstream_commit']}/images/ubuntu/Ubuntu2404-Readme.md"
        with urllib.request.urlopen(url,timeout=30) as response:data=response.read(1024*1024)
    if hashlib.sha256(data).hexdigest()!=lock['upstream_inventory_sha256']:raise SystemExit('Official inventory checksum mismatch')
    actual={}
    for line in (bundle/'packages.txt').read_text().splitlines():
        name,version=line.split('\t',1);actual[name.split(':',1)[0]]=version
    result=compare(package_table(data.decode()),actual)
    result['upstream_commit']=lock['upstream_commit']
    compatibility=json.load(open(bundle/'compatibility.json'))
    result['remaining_capabilities']=compatibility['remaining']+compatibility.get('remaining_extra_tools',[])
    result['tool_version_differences']=[{'name':t['name'],'expected':t['upstream_expected_version'],'actual':t['version']} for t in compatibility.get('extra_tools',[]) if t.get('upstream_expected_version',t['version'])!=t['version']]
    print(json.dumps(result,indent=2))
    raise SystemExit(bool(result['missing_packages'] or result['different_versions'] or result['remaining_capabilities']))
