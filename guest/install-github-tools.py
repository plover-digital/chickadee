#!/usr/bin/env python3
"""Build-time only: install verified GitHub tool inputs inside the guest root."""
import hashlib,json,os,pathlib,shutil,subprocess,tarfile
run=lambda *args:subprocess.run(args,check=True)
lock=json.load(open('/tmp/github-image-lock.json'))
run('bash','-c','set -e; . /etc/os-release; test "$ID:$VERSION_ID" = ubuntu:24.04')
snapshot=lock['ubuntu_snapshot']
for p in pathlib.Path('/etc/apt/sources.list.d').glob('*'):p.unlink()
pathlib.Path('/etc/apt/sources.list').write_text(''.join(f'deb [signed-by=/usr/share/keyrings/ubuntu-archive-keyring.gpg check-valid-until=no] https://snapshot.ubuntu.com/ubuntu/{snapshot} {suite} main universe restricted multiverse\n' for suite in ['noble','noble-updates','noble-security']))
# Service startup is suppressed in offline image editing; guests enable Docker.
policy=pathlib.Path('/usr/sbin/policy-rc.d');previous=policy.read_bytes() if policy.exists() else None
policy.write_text('#!/bin/sh\nexit 101\n');policy.chmod(0o755)
os.environ['DEBIAN_FRONTEND']='noninteractive'
try:
 run('apt-get','-o','Acquire::Retries=3','-o','APT::Update::Error-Mode=any','update')
 packages=[{'netcat':'netcat-openbsd'}.get(p,p) for p in lock['apt_packages']]
 packages+=['libatk-bridge2.0-0t64','libatk1.0-0t64','libatspi2.0-0t64','libcups2t64','libcurl4t64','libgbm1','libnspr4','libnss3','libvulkan1','libxcomposite1','libxdamage1','libxfixes3','libxkbcommon0','libxrandr2','xdg-utils','fonts-liberation','libgtk-3-0t64','libasound2t64','gzip','zstd','python3-venv','python3-pip','pipx','ruby','ruby-dev','cmake','ninja-build','clang-16','clang-17','clang-18','clang-format-16','clang-format-17','clang-format-18','clang-tidy-16','clang-tidy-17','clang-tidy-18','gcc-12','gcc-13','gcc-14','g++-12','g++-13','g++-14','gfortran-12','gfortran-13','gfortran-14','openjdk-17-jdk','openjdk-21-jdk','maven','ant','git-lfs','docker.io','docker-compose-v2','docker-buildx','podman','buildah','skopeo','postgresql-16','mysql-server','apache2','nginx','php8.3-cli','composer']
 run('apt-get','install','-y','--no-install-recommends',*sorted(set(packages)))
finally:
 if previous is None:policy.unlink(missing_ok=True)
 else:policy.write_bytes(previous)
cache=pathlib.Path('/opt/hostedtoolcache');cache.mkdir(parents=True,exist_ok=True)
staging=pathlib.Path('/tmp/github-tools');staging.mkdir(exist_ok=True)
with tarfile.open('/tmp/github-tools.tar') as tar:tar.extractall(staging,filter='data')
for tool in lock['tools']:
 archive=staging/tool['filename']
 with archive.open('rb') as data:assert hashlib.file_digest(data,'sha256').hexdigest()==tool['sha256'],'tool checksum mismatch'
 name='Python' if tool['name']=='python' else tool['name']
 target=cache/name/tool['version']/'x64';target.mkdir(parents=True)
 with tarfile.open(archive) as tar:tar.extractall(target,filter='data')
 (target/'setup.sh').unlink(missing_ok=True)
 if name=='Python':
  major='.'.join(tool['version'].split('.')[:2]);binary='python'+major
  for alias in ['python','python3','python'+major.replace('.','')]:
   p=target/'bin'/alias
   if not p.exists():p.symlink_to(binary)
  (target/'python').symlink_to('bin/'+binary)
  run(str(target/'bin'/binary),'-c','import ssl,sqlite3,ctypes,zlib; import sys; print(sys.version)')
 elif name=='go':run(str(target/'bin/go'),'version')
 else:run(str(target/'bin/node'),'--version')
 (target.parent/'x64.complete').touch()
# Match setup-* expectations: writable cache in each disposable VM.
run('chown','-R','runner:runner',str(cache))
for command,src in [('node','node/22.23.3/x64/bin/node'),('npm','node/22.23.3/x64/bin/npm'),('npx','node/22.23.3/x64/bin/npx'),('go','go/1.24.13/x64/bin/go')]:
 dest=pathlib.Path('/usr/local/bin')/command
 if dest.exists() or dest.is_symlink():dest.unlink()
 dest.symlink_to(cache/src)
for alias,target in [('clang','clang-18'),('clang++','clang++-18'),('clang-format','clang-format-18'),('clang-tidy','clang-tidy-18'),('gfortran','gfortran-13')]:
 dest=pathlib.Path('/usr/local/bin')/alias;dest.unlink(missing_ok=True);dest.symlink_to('/usr/bin/'+target)
config=pathlib.Path('/etc/chickadee');config.mkdir(exist_ok=True)
(config/'runner.env').write_text('RUNNER_TOOL_CACHE=/opt/hostedtoolcache\nAGENT_TOOLSDIRECTORY=/opt/hostedtoolcache\nJAVA_HOME=/usr/lib/jvm/java-17-openjdk-amd64\nJAVA_HOME_17_X64=/usr/lib/jvm/java-17-openjdk-amd64\nJAVA_HOME_21_X64=/usr/lib/jvm/java-21-openjdk-amd64\nImageOS=ubuntu24\nImageVersion=chickadee-github-core-20260927\n')
run('update-alternatives','--set','java','/usr/lib/jvm/java-17-openjdk-amd64/bin/java')
run('update-alternatives','--set','javac','/usr/lib/jvm/java-17-openjdk-amd64/bin/javac')
pathlib.Path('/etc/sudoers.d/runner').write_text('runner ALL=(ALL) NOPASSWD:ALL\n');pathlib.Path('/etc/sudoers.d/runner').chmod(0o440)
run('usermod','-aG','docker','runner')
run('systemctl','enable','docker.service')
bootstrap=pathlib.Path('/etc/systemd/system/chickadee-bootstrap.service.d');bootstrap.mkdir(parents=True,exist_ok=True)
(bootstrap/'docker.conf').write_text('[Unit]\nAfter=docker.service\n')
for service in ['postgresql','mysql','apache2','nginx','haveged','pollinate','sphinxsearch','ssh']:run('systemctl','disable',service+'.service')
shutil.rmtree(staging);pathlib.Path('/tmp/github-tools.tar').unlink()
run('apt-get','clean')
shutil.rmtree('/var/lib/apt/lists',ignore_errors=True)
# libguestfs injects build-time identities; every boot must get a fresh identity.
pathlib.Path('/etc/machine-id').write_text('')
pathlib.Path('/var/lib/dbus/machine-id').unlink(missing_ok=True)
pathlib.Path('/var/lib/systemd/random-seed').unlink(missing_ok=True)
with open('/image-packages.txt','w') as f:subprocess.run(['dpkg-query','-W'],stdout=f,check=True)
report={'upstream_commit':lock['upstream_commit'],'upstream_image_version':lock['upstream_image_version'],'status':'core compatibility; not complete hosted image parity','installed_toolcache':lock['tools'],'apt_snapshot':snapshot,'remaining':['Android SDK/NDKs','Google Chrome/Edge/Firefox and drivers','additional Java 8/11/25 and exact upstream Java patch versions','Ruby/PyPy toolcache','Rust toolchain','Swift','Julia','Kotlin','Haskell','dotnet SDK matrix','PowerShell/Azure modules','AWS/Azure/Google CLIs','Homebrew','CodeQL','Helm/Kubernetes tools','exact upstream third-party package versions']}
(config/'image-compatibility.json').write_text(json.dumps(report,indent=2)+'\n')
