#!/usr/bin/env python3
"""Install checked Go/Node archives in a native Rocky image at build time."""
import hashlib,json,pathlib,shutil,subprocess,tarfile

run=lambda *args:subprocess.run(args,check=True)
run('bash','-c','set -e; . /etc/os-release; test "$ID:$VERSION_ID" = rocky:10.2')
lock=json.load(open('/tmp/rocky-tool-inputs.json'))
cache=pathlib.Path('/opt/hostedtoolcache');cache.mkdir(parents=True,exist_ok=True)
staging=pathlib.Path('/tmp/rocky-tools');staging.mkdir(exist_ok=True)
with tarfile.open('/tmp/rocky-tools.tar') as archive:archive.extractall(staging,filter='data')
for tool in lock['tools']:
    assert tool['name'] in ['go','node'], 'Only portable Go/Node archives belong in Rocky'
    archive=staging/tool['filename']
    with archive.open('rb') as data:assert hashlib.file_digest(data,'sha256').hexdigest()==tool['sha256']
    target=cache/tool['name']/tool['version']/'x64';target.mkdir(parents=True)
    with tarfile.open(archive) as data:data.extractall(target,filter='data')
    (target/'setup.sh').unlink(missing_ok=True)
    executable='go' if tool['name']=='go' else 'node'
    run(str(target/'bin'/executable),'version' if executable=='go' else '--version')
    (target.parent/'x64.complete').touch()
run('chown','-R','runner:runner',str(cache))
for command,src in [('node','node/22.23.3/x64/bin/node'),('npm','node/22.23.3/x64/bin/npm'),('npx','node/22.23.3/x64/bin/npx'),('go','go/1.24.13/x64/bin/go')]:
    destination=pathlib.Path('/usr/local/bin')/command
    destination.unlink(missing_ok=True);destination.symlink_to(cache/src)
config=pathlib.Path('/etc/chickadee');config.mkdir(exist_ok=True)
(config/'runner.env').write_text('RUNNER_TOOL_CACHE=/opt/hostedtoolcache\nAGENT_TOOLSDIRECTORY=/opt/hostedtoolcache\nJAVA_HOME=/usr/lib/jvm/java-21-openjdk\n')
sudoers=pathlib.Path('/etc/sudoers.d/runner');sudoers.write_text('runner ALL=(ALL) NOPASSWD:ALL\n');sudoers.chmod(0o440)
report={'status':'native developer baseline; not GitHub hosted image parity','upstream_commit':lock['upstream_commit'],'installed_toolcache':lock['tools'],'remaining':['Docker daemon and compose/buildx','Ubuntu Python toolcache is incompatible; native Rocky Python is installed','Additional Java versions (Rocky 10 provides Java 21)','Full browser, Android, cloud CLI and language SDK inventory']}
(config/'image-compatibility.json').write_text(json.dumps(report,indent=2)+'\n')
shutil.rmtree(staging)
for name in ['rocky-tools.tar','rocky-tool-inputs.json']: (pathlib.Path('/tmp')/name).unlink()
