#!/usr/bin/env python3
"""Verify an operator-approved beta request and prepare shared-scope config.

No service is restarted here. Review/validate output, drain the controller, then
install it. Neither user tokens nor App tokens are written or printed.
"""
import argparse, copy, importlib.util, json, os, pathlib, re, urllib.request

spec=importlib.util.spec_from_file_location('setup_app',pathlib.Path(__file__).with_name('setup-app.py'))
setup=importlib.util.module_from_spec(spec);spec.loader.exec_module(setup)

def requested_profiles(c, request):
    template=c.get('profiles') or c['scopes']['primary']['profiles']
    queues=request.get('queues',['chickadee'])
    if not isinstance(queues,list) or len(queues)>len(template) or any(not isinstance(q,str) or q not in template for q in queues):
        raise ValueError('requested queue is not in the host profile catalog')
    selected=['chickadee']+list(dict.fromkeys(q for q in queues if q!='chickadee'))
    profiles={q:copy.deepcopy(template[q]) for q in selected}
    for profile in profiles.values():profile['warm_pool']=0
    return profiles

def proposed(c, request, installation, repo, group_id):
    profiles=requested_profiles(c,request)
    maximum=request.get('max_vms',1)
    if type(maximum)!=int or maximum<1 or maximum>c['limits']['max_vms']:raise ValueError('invalid scope concurrency limit')
    if installation['account']['id']!=request['account']['id'] or installation['account']['type']!=request['account']['type']:
        raise ValueError('installation account mismatch')
    if repo['id']!=request['repository']['id'] or repo['full_name']!=request['repository']['full_name']:
        raise ValueError('repository identity mismatch')
    result=copy.deepcopy(c)
    if not result.get('scopes'):
        primary_profiles=result.pop('profiles')
        result['scopes']={'primary':{'github_url':result.pop('github_url'),'app_installation_id':result.pop('app_installation_id'),'runner_group_id':result.pop('runner_group_id'),'profiles':primary_profiles}}
    url='https://github.com/'+(installation['account']['login'] if installation['account']['type']=='Organization' else repo['full_name'])
    for scope in result['scopes'].values():
        if scope['github_url'].lower().rstrip('/')==url.lower():
            if scope['app_installation_id']!=installation['id'] or scope['runner_group_id']!=group_id:raise ValueError('existing scope identity changed')
            for label,profile in profiles.items():scope['profiles'].setdefault(label,profile)
            return result
    result['scopes'][('org-' if installation['account']['type']=='Organization' else 'repo-')+str(installation['account']['id'] if installation['account']['type']=='Organization' else repo['id'])]={'github_url':url,'app_installation_id':installation['id'],'runner_group_id':group_id,'profiles':profiles,'max_vms':request.get('max_vms',1)}
    return result

def validate_repository_only_selection(selected, repository_id):
    if selected['total_count']!=1 or {r['id'] for r in selected['repositories']}!={repository_id}:
        raise ValueError('repository-only approval cannot broaden access for other selected repositories')

def runner_group_plan(name, repo, ref, repository_only=False):
    if repository_only and not repo['private']:
        raise ValueError('repository-only access requires the explicitly approved private repository')
    return {'name':name,'visibility':'selected','selected_repository_ids':[repo['id']],
            'allows_public_repositories':False if repository_only else not repo['private'],
            'restricted_to_workflows':not repository_only,
            'selected_workflows':[] if repository_only else [ref]}

def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--config',required=True);p.add_argument('--request',required=True);p.add_argument('--output',required=True)
    p.add_argument('--trusted-workflows',action='store_true',help='operator has reviewed beta trust and workflow policy')
    p.add_argument('--repository-only',action='store_true',help='explicit operator approval for every workflow in the one selected private organization repository')
    p.add_argument('--workflow',default='.github/workflows/chickadee.yml',help='main-branch workflow allowed for org groups')
    p.add_argument('--queue',action='append',help='explicit additional queue; repeat as needed (default: chickadee only)')
    args=p.parse_args()
    if not args.trusted_workflows:raise ValueError('operator admission and trusted-workflow review required')
    if not re.fullmatch(r'\.github/workflows/[A-Za-z0-9_-]+\.ya?ml',args.workflow):raise ValueError('use one main-branch workflow file')
    c=json.load(open(args.config));request=json.load(open(args.request))
    if args.queue is not None:request['queues']=args.queue
    if not c.get('profiles') and not c.get('scopes'):raise ValueError('first migrate flat config to the image/profile catalog')
    requested_profiles(c,request) # Reject unknown queues before any GitHub mutation.
    iid=request['installation_id'];rid=request['repository']['id']
    if type(iid)!=int or iid<=0 or type(rid)!=int or rid<=0:raise ValueError('invalid installation/repository ID')
    jwt=setup.app_jwt(c['app_client_id'],pathlib.Path(c['app_key_file']))
    installation=setup.api('/app/installations/'+str(iid),token=jwt)
    if installation.get('suspended_at'):raise ValueError('installation suspended')
    account=installation['account']
    if account['type'] not in ('User','Organization'):raise ValueError('unsupported account type')
    needed='organization_self_hosted_runners' if account['type']=='Organization' else 'administration'
    if installation['permissions'].get(needed)!='write':raise ValueError('runner-management permission not approved')
    token=setup.api('/app/installations/'+str(iid)+'/access_tokens','POST',jwt,{'permissions':{needed:'write','metadata':'read'}})['token']
    repo=None
    for page in range(1,11):
        listing=setup.api('/installation/repositories?per_page=100&page='+str(page),token=token)
        for item in listing['repositories']:
            if item['id']==rid:repo=item
        if repo is not None or len(listing['repositories'])<100:break
    if repo is None:raise ValueError('repository not selected for this App installation')
    # Validate identities before any runner-group mutation.
    if account['id']!=request['account']['id'] or account['type']!=request['account']['type'] or repo['full_name']!=request['repository']['full_name']:
        raise ValueError('request identity no longer matches GitHub')
    if account['type']=='User' and account['id']!=request['user']['id']:
        raise ValueError('personal beta admission requires the repository owner')
    if args.repository_only and (account['type']!='Organization' or not repo['private']):
        raise ValueError('repository-only access requires an approved private organization repository')
    group_id=1
    if account['type']=='Organization':
        org=account['login'];base='/orgs/'+org+'/actions/runner-groups'
        scopes=c.get('scopes',{})
        if not scopes:scopes={'primary':{'github_url':c['github_url'],'runner_group_id':c['runner_group_id']}}
        own=next((scope for scope in scopes.values() if scope['github_url'].lower().rstrip('/')==('https://github.com/'+org).lower()),None)
        ref=repo['full_name']+'/'+args.workflow+'@refs/heads/main'
        if own is None:
            # Do not adopt/mutate an unrelated group by its name on retry.
            name='chickadee-'+str(installation['app_id'])+'-'+str(iid)
            groups=setup.api(base+'?per_page=100',token=token)
            if any(g['name']==name for g in groups['runner_groups']):raise ValueError('group already exists; recover its ownership/config before retrying')
            group=setup.api(base,'POST',token,runner_group_plan(name,repo,ref,args.repository_only))
        else:
            group=setup.api(base+'/'+str(own['runner_group_id']),token=token)
            if group['visibility']!='selected':raise ValueError('existing group must restrict repositories')
            if args.repository_only:
                selected=setup.api(base+'/'+str(group['id'])+'/repositories?per_page=100',token=token)
                validate_repository_only_selection(selected,rid)
                group=setup.api(base+'/'+str(group['id']),'PATCH',token,{'restricted_to_workflows':False,'selected_workflows':[],'allows_public_repositories':False})
            else:
                if not group.get('restricted_to_workflows'):raise ValueError('existing repository-only group requires explicit repository-only policy')
                refs=set(group['selected_workflows']);refs.add(ref)
                setup.api(base+'/'+str(group['id'])+'/repositories/'+str(rid),'PUT',token)
                group=setup.api(base+'/'+str(group['id']),'PATCH',token,{'restricted_to_workflows':True,'selected_workflows':sorted(refs),'allows_public_repositories':group['allows_public_repositories'] or not repo['private']})
        group_id=group['id']
    output=proposed(c,request,installation,repo,group_id)
    path=pathlib.Path(args.output)
    fd=os.open(path,os.O_CREAT|os.O_EXCL|os.O_WRONLY,0o600)
    with os.fdopen(fd,'w') as f:json.dump(output,f,indent=2);f.write('\n');f.flush();os.fsync(f.fileno())
    print('verified candidate written; run chickadee -check, review, drain, and install')

if __name__=='__main__':
    try:main()
    except Exception as e:
        # Never include token-bearing upstream objects or HTTP response bodies.
        print('admission failed: '+str(e)[:250],file=__import__('sys').stderr)
        raise SystemExit(1)
