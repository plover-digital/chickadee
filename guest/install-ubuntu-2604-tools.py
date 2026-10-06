#!/usr/bin/env python3
"""Build-time developer profile using Ubuntu26-native packages and toolcache."""
import hashlib,json,os,pathlib,shutil,subprocess,tarfile

run=lambda *args:subprocess.run(args,check=True)
run('bash','-c','set -e; . /etc/os-release; test "$ID:$VERSION_ID" = ubuntu:26.04')
lock=json.load(open('/tmp/ubuntu-2604-tool-lock.json'))
snapshot=lock['ubuntu_snapshot']
for p in pathlib.Path('/etc/apt/sources.list.d').glob('*'):p.unlink()
pathlib.Path('/etc/apt/sources.list').write_text(''.join(f'deb [signed-by=/usr/share/keyrings/ubuntu-archive-keyring.gpg check-valid-until=no] https://snapshot.ubuntu.com/ubuntu/{snapshot} {suite} main universe restricted multiverse\n' for suite in ['resolute','resolute-updates','resolute-security']))
policy=pathlib.Path('/usr/sbin/policy-rc.d');previous=policy.read_bytes() if policy.exists() else None
policy.write_text('#!/bin/sh\nexit 101\n');policy.chmod(0o755)
os.environ['DEBIAN_FRONTEND']='noninteractive'
try:
    run('apt-get','-o','Acquire::Retries=3','-o','APT::Update::Error-Mode=any','update')
    packages=[lock['package_aliases'].get(p,p) for p in lock['apt_packages']+lock['developer_packages']]
    run('apt-get','install','-y','--no-install-recommends',*sorted(set(packages)))
finally:
    if previous is None:policy.unlink(missing_ok=True)
    else:policy.write_bytes(previous)
cache=pathlib.Path('/opt/hostedtoolcache');cache.mkdir(parents=True,exist_ok=True)
staging=pathlib.Path('/tmp/ubuntu-2604-tools');staging.mkdir(exist_ok=True)
with tarfile.open('/tmp/ubuntu-2604-tools.tar') as archive:archive.extractall(staging,filter='data')
for tool in lock['tools']:
    archive=staging/tool['filename']
    with archive.open('rb') as data:assert hashlib.file_digest(data,'sha256').hexdigest()==tool['sha256']
    if tool['name']=='python':assert tool['platform']=='linux-26.04-x64', 'Ubuntu Python archives must match the OS'
    name='Python' if tool['name']=='python' else tool['name']
    target=cache/name/tool['version']/'x64';target.mkdir(parents=True)
    with tarfile.open(archive) as data:data.extractall(target,filter='data')
    (target/'setup.sh').unlink(missing_ok=True)
    if name=='Python':
        binary='python'+'.'.join(tool['version'].split('.')[:2])
        for alias in ['python','python3',binary.replace('.','')]:
            path=target/'bin'/alias
            if not path.exists():path.symlink_to(binary)
        (target/'python').symlink_to('bin/'+binary)
        run(str(target/'bin'/binary),'-c','import ssl,sqlite3,ctypes,zlib,pip; import sys; print(sys.version)')
    elif name=='go':run(str(target/'bin/go'),'version')
    else:run(str(target/'bin/node'),'--version')
    (target.parent/'x64.complete').touch()
run('chown','-R','runner:runner',str(cache))
for command,src in [('node',f"node/{lock['default_node']}/x64/bin/node"),('npm',f"node/{lock['default_node']}/x64/bin/npm"),('npx',f"node/{lock['default_node']}/x64/bin/npx"),('go',f"go/{lock['default_go']}/x64/bin/go")]:
    destination=pathlib.Path('/usr/local/bin')/command
    destination.unlink(missing_ok=True);destination.symlink_to(cache/src)
for command in ['clang','clang++','clang-format','clang-tidy']:
    destination=pathlib.Path('/usr/local/bin')/command
    destination.unlink(missing_ok=True);destination.symlink_to('/usr/bin/'+command+'-21')
config=pathlib.Path('/etc/chickadee');config.mkdir(exist_ok=True)
(config/'runner.env').write_text('RUNNER_TOOL_CACHE=/opt/hostedtoolcache\nAGENT_TOOLSDIRECTORY=/opt/hostedtoolcache\nJAVA_HOME=/usr/lib/jvm/java-17-openjdk-amd64\nJAVA_HOME_11_X64=/usr/lib/jvm/java-11-openjdk-amd64\nJAVA_HOME_17_X64=/usr/lib/jvm/java-17-openjdk-amd64\nJAVA_HOME_21_X64=/usr/lib/jvm/java-21-openjdk-amd64\nJAVA_HOME_25_X64=/usr/lib/jvm/java-25-openjdk-amd64\n')
run('update-java-alternatives','--set','java-1.17.0-openjdk-amd64')
run('usermod','-aG','docker','runner')
run('systemctl','enable','docker.service')
bootstrap=pathlib.Path('/etc/systemd/system/chickadee-bootstrap.service.d');bootstrap.mkdir(parents=True,exist_ok=True)
(bootstrap/'docker.conf').write_text('[Unit]\nAfter=docker.service\n')
for service in ['postgresql','mysql','apache2','nginx','pollinate','ssh']:run('systemctl','disable',service+'.service')
shutil.rmtree(staging)
pathlib.Path('/tmp/ubuntu-2604-tools.tar').unlink()
report={'upstream_commit':lock['upstream_commit'],'upstream_image_version':lock['upstream_image_version'],'status':'core developer compatibility; not complete GitHub hosted image parity','installed_toolcache':lock['tools'],'apt_snapshot':snapshot,'remaining':['Android SDK/NDKs','additional browser drivers and Edge','Ruby/PyPy toolcache','Swift/Julia/Kotlin/Haskell','AWS/Azure/Google CLIs','PowerShell/Azure modules','Homebrew','CodeQL','Helm/Kubernetes tools','exact upstream third-party package versions']}
(config/'image-compatibility.json').write_text(json.dumps(report,indent=2)+'\n')
