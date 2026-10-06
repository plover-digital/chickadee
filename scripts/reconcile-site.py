#!/usr/bin/env python3
"""Operator-owned beta reconciler. No public admin HTTP endpoint or database.

Run as root on the runner host. Site transport is pinned SSH + private Unix
socket. Policy admits trusted user IDs and explicitly approved queues only.
"""
import argparse, copy, fcntl, importlib.util, json, os, pathlib, subprocess, tempfile, time

spec=importlib.util.spec_from_file_location('admit',pathlib.Path(__file__).with_name('admit-installation.py'))
admit=importlib.util.module_from_spec(spec);spec.loader.exec_module(admit)


def approved_request(entry, policy):
    approval=policy.get('approved_users',{}).get(str(entry['user']['id']))
    if approval is None:return None
    desired=entry.get('desired_state') or 'active'
    if desired not in ('active','paused','disconnected'):raise ValueError('invalid desired state')
    requested=entry.get('queues') or ['chickadee']
    allowed=approval.get('queues',['chickadee'])
    queues=['chickadee']+list(dict.fromkeys(q for q in requested if q!='chickadee' and q in allowed))
    return dict(entry,queues=queues,max_vms=approval.get('max_vms',1))


def scope_name(entry):
    return ('org-'+str(entry['account']['id']) if entry['account']['type']=='Organization' else 'repo-'+str(entry['repository']['id']))


def install_config(candidate,path,quarantine):
    encoded=json.dumps(candidate,indent=2)+'\n'
    with tempfile.TemporaryDirectory(prefix='chickadee-config-') as tmp:
        stage=pathlib.Path(tmp)/'config.json';stage.write_text(encoded);stage.chmod(0o600)
        subprocess.run(['/usr/local/bin/chickadee','-config',str(stage),'-check'],check=True)
        # Drain main process only. Never terminate a job for onboarding changes.
        state=subprocess.check_output(['systemctl','show','chickadee','-p','ActiveState','--value'],text=True).strip()
        if state=='activating':raise RuntimeError('controller still starting; retry later')
        active=state=='active'
        if active:
            subprocess.run(['systemctl','kill','--kill-whom=main','--signal=SIGUSR1','chickadee'],check=True)
            deadline=time.monotonic()+candidate['job_timeout_seconds']+120
            while subprocess.run(['systemctl','is-active','--quiet','chickadee']).returncode==0:
                if time.monotonic()>deadline:raise RuntimeError('drain timed out; config not installed')
                time.sleep(2)
        # Journal recovery for a revoked installation is impossible until access
        # returns. Preserve those intents privately rather than losing ownership.
        runtime=pathlib.Path(candidate['state_dir'])
        if list((runtime/'vms').iterdir()):raise RuntimeError('VMs remain after drain')
        if quarantine:
            target=runtime/'revoked-records';target.mkdir(mode=0o700,exist_ok=True)
            for file in (runtime/'records').glob('*.json'):
                record=json.loads(file.read_text())
                if record['github_url'].lower().rstrip('/') in quarantine:
                    destination=target/file.name
                    if destination.exists():raise RuntimeError('quarantine record already exists')
                    os.rename(file,destination)
        backup=path.with_name(path.name+'.before-managed-update');backup.write_bytes(path.read_bytes());backup.chmod(0o600)
        old=path.stat();stage.chown(old.st_uid,old.st_gid);stage.chmod(old.st_mode&0o777)
        # stage is on /tmp: copy to the target filesystem before atomic replace.
        fd,name=tempfile.mkstemp(prefix='.config-',dir=path.parent)
        with os.fdopen(fd,'w') as f:f.write(encoded);f.flush();os.fsync(f.fileno())
        os.chown(name,old.st_uid,old.st_gid);os.chmod(name,old.st_mode&0o777);os.replace(name,path)
        subprocess.run(['systemctl','reset-failed','chickadee'],check=True)
        start=time.time()
        subprocess.run(['systemctl','start','chickadee'],check=True)
        deadline=time.monotonic()+300
        while True:
            try:
                ready=json.loads((runtime/'status.json').read_text())
                import datetime
                updated=datetime.datetime.fromisoformat(ready['updated_at'].replace('Z','+00:00')).timestamp()
                if updated>=start and not ready['draining']:break
            except (OSError,ValueError,KeyError):pass
            if time.monotonic()>deadline:raise RuntimeError('new config has not reached ready; no activation acknowledged')
            time.sleep(2)


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config',default='/etc/chickadee/config.json')
    parser.add_argument('--policy',required=True)
    parser.add_argument('--site-host',required=True)
    parser.add_argument('--ssh-key',required=True)
    parser.add_argument('--known-hosts',required=True)
    parser.add_argument('--state-dir',default='/var/lib/chickadee-managed')
    args=parser.parse_args()
    if os.geteuid()!=0:raise ValueError('run as operator root; not the controller UID')
    runtime_lock=pathlib.Path('/run/chickadee-managed');runtime_lock.mkdir(mode=0o700,exist_ok=True)
    lock=open(runtime_lock/'lock','w');fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
    policy_path=pathlib.Path(args.policy)
    if not policy_path.is_file() or policy_path.stat().st_mode&0o077:raise ValueError('policy must be private')
    policy=json.loads(policy_path.read_text());path=pathlib.Path(args.config);original=json.loads(path.read_text());candidate=copy.deepcopy(original)
    ssh=['ssh','-i',args.ssh_key,'-o','BatchMode=yes','-o','StrictHostKeyChecking=yes','-o','UserKnownHostsFile='+args.known_hosts,'-o','ConnectTimeout=10','root@'+args.site_host]
    def admin(endpoint,body=None):
        command='sudo -u chickadee-web curl --fail --silent --show-error --max-time 15 --unix-socket /var/lib/chickadee-web/admin.sock '
        if body is not None:command+='-H "Content-Type: application/json" --data-binary @- '
        command+='http://localhost/'+endpoint
        result=subprocess.run([*ssh,command],input=None if body is None else json.dumps(body).encode(),stdout=subprocess.PIPE,stderr=subprocess.DEVNULL,timeout=30,check=True)
        if len(result.stdout)>2<<20:raise ValueError('site response too large')
        return json.loads(result.stdout) if result.stdout else None
    state=pathlib.Path(args.state_dir);state.mkdir(mode=0o700,exist_ok=True)
    if state.is_symlink() or state.stat().st_uid!=0 or state.stat().st_mode&0o077:raise ValueError('managed state must be root owned and private')
    site_available=True
    try:
        entries=admin('enrollments') or []
        fd,cache=tempfile.mkstemp(prefix='.requests-',dir=state)
        with os.fdopen(fd,'w') as file:json.dump(entries,file);file.flush();os.fsync(file.fileno())
        os.replace(cache,state/'requests.json')
    except (subprocess.SubprocessError,OSError):
        site_available=False
        entries=json.loads((state/'requests.json').read_text()) if (state/'requests.json').exists() else []
    if not isinstance(entries,list) or len(entries)>500:raise ValueError('invalid site request list')
    updates=[];quarantine=set()
    # Managed scope seeds allow revocation checks before a user has signed in.
    requests_by_scope={scope_name(e):e for e in policy.get('managed_requests',[])}
    for entry in entries:
        if approved_request(entry,policy) is None:continue
        name=scope_name(entry);previous=requests_by_scope.get(name)
        if previous is not None and (previous['user']['id']!=entry['user']['id'] or previous['installation_id']!=entry['installation_id']):raise ValueError('conflicting scope owners require operator review')
        requests_by_scope[name]=entry
    requests=list(requests_by_scope.values())
    jwt=admit.setup.app_jwt(original['app_client_id'],pathlib.Path(original['app_key_file']))
    verified={}
    for entry in requests:
        request=approved_request(entry,policy)
        if request is None:continue
        name=scope_name(entry)
        # Never let an enrollment mutate the operator's primary scope.
        url='https://github.com/'+(entry['account']['login'] if entry['account']['type']=='Organization' else entry['repository']['full_name'])
        primary=candidate.get('scopes',{}).get('primary',{}).get('github_url',candidate.get('github_url',''))
        if url.lower().rstrip('/')==primary.lower().rstrip('/'):
            if entry.get('id'):updates.append({'id':entry['id'],'status':'active','enabled_queues':['chickadee'],'message':'Operator-owned scope is active; additional queues require explicit approval.'})
            continue
        if name in verified:raise ValueError('multiple requests target one scope; operator reconciliation required')
        verified[name]=True
        desired=entry.get('desired_state') or 'active';status='pending';enabled=[];message=''
        revoked=False
        try:
            lookup_installation=True
            installation=admit.setup.api('/app/installations/'+str(entry['installation_id']),token=jwt)
            lookup_installation=False
            required='organization_self_hosted_runners' if entry['account']['type']=='Organization' else 'administration'
            revoked=bool(installation.get('suspended_at')) or installation['permissions'].get(required)!='write'
            if not revoked:
                token=admit.setup.api('/app/installations/'+str(entry['installation_id'])+'/access_tokens','POST',jwt,{'permissions':{required:'write','metadata':'read'}})['token']
                found=False
                for page in range(1,11):
                    listing=admit.setup.api('/installation/repositories?per_page=100&page='+str(page),token=token)
                    found=any(r['id']==entry['repository']['id'] and r['full_name']==entry['repository']['full_name'] for r in listing['repositories'])
                    if found or len(listing['repositories'])<100:break
                if not found and len(listing['repositories'])==100:raise RuntimeError('repository verification exceeded page limit')
                revoked=not found
        except RuntimeError as err:
            # A 404 from App-authenticated installation lookup confirms removal.
            if lookup_installation and str(err)=='GitHub setup API returned HTTP 404':revoked=True
            else:raise
        if revoked or desired!='active':
            old=candidate.get('scopes',{}).get(name)
            if revoked and old:
                candidate['scopes'].pop(name);quarantine.add(old['github_url'].lower().rstrip('/'))
            elif old:old['disabled']=True
            status='permission-required' if revoked else desired
            message='GitHub access removed, suspended, or awaiting permission approval.' if revoked else 'New assignments stopped; running jobs finished before applying this state.'
        else:
            with tempfile.TemporaryDirectory(prefix='chickadee-admission-') as tmp:
                config=pathlib.Path(tmp)/'config.json';config.write_text(json.dumps(candidate));config.chmod(0o600)
                req=pathlib.Path(tmp)/'request.json';req.write_text(json.dumps(request));req.chmod(0o600)
                output=pathlib.Path(tmp)/'output.json'
                subprocess.run(['python3',str(pathlib.Path(__file__).with_name('admit-installation.py')),'--config',str(config),'--request',str(req),'--output',str(output),'--trusted-workflows','--workflow',policy.get('workflow','.github/workflows/ci.yaml')],check=True,stdout=subprocess.DEVNULL)
                candidate=json.loads(output.read_text())
            scope=candidate['scopes'][name]
            # Policy defines exact enabled queues, not a permanent union.
            scope['profiles']={q:p for q,p in scope['profiles'].items() if q in request['queues']}
            scope['max_vms']=request['max_vms']
            scope.pop('disabled',None)
            enabled=list(scope['profiles']);status='active'
            if set(entry.get('queues') or ['chickadee'])-set(enabled):message='Additional queue requests are awaiting operator approval.'
        if entry.get('id'):updates.append({'id':entry['id'],'status':status,'enabled_queues':enabled,'message':message})
    if candidate!=original:install_config(candidate,path,quarantine)
    if site_available:
        for update in updates:
            entry=next(e for e in entries if e.get('id')==update['id'])
            url='https://github.com/'+(entry['account']['login'] if entry['account']['type']=='Organization' else entry['repository']['full_name'])
            usage_path=pathlib.Path(candidate['state_dir'])/'usage.json'
            records=json.loads(usage_path.read_text()) if usage_path.exists() else []
            import datetime
            today=datetime.datetime.now(datetime.timezone.utc).replace(hour=0,minute=0,second=0,microsecond=0)
            days=[]
            for offset in range(-6,1):
                start=today+datetime.timedelta(days=offset);end=start+datetime.timedelta(days=1)
                point={'date':start.date().isoformat(),'vm_seconds':0,'vms':0}
                for record in records:
                    if record['github_url'].lower().rstrip('/')!=url.lower().rstrip('/'):continue
                    reserved=datetime.datetime.fromisoformat(record['reserved_at'].replace('Z','+00:00'));completed=datetime.datetime.fromisoformat(record['completed_at'].replace('Z','+00:00'))
                    point['vm_seconds']+=max(0,(min(completed,end)-max(reserved,start)).total_seconds())
                    if start<=completed<end:point['vms']+=1
                days.append(point)
            update['usage']=days
            admin('status',update)
    print('managed reconciliation completed; requests='+str(len(updates)))

if __name__=='__main__':
    try:main()
    except Exception:
        # Upstream bodies, SSH output and request metadata stay out of journals.
        print('managed reconciliation failed; reviewed config remains or update requires operator recovery',file=__import__('sys').stderr)
        raise SystemExit(1)
