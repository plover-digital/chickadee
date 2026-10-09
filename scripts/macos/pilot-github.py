#!/usr/bin/env python3
"""Drive one trusted repository workflow through the native Mac pilot over SSH.

Requires operator-approved guest networking and gh admin access to the test repo.
Management credentials stay on this host; only runner JIT crosses SSH stdin.
"""
import argparse
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import queue
import secrets
import shlex
import subprocess
import threading
import time


def api(method, path, data=None):
    command=['gh','api','--method',method,path]
    if data is not None:
        command+=['--input','-']
    result=subprocess.run(command,input=None if data is None else json.dumps(data),capture_output=True,text=True,timeout=65)
    if result.returncode:
        raise RuntimeError('GitHub request failed')
    return json.loads(result.stdout) if result.stdout.strip() else None


def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--ssh',required=True)
    p.add_argument('--repo',required=True)
    p.add_argument('--remote-python',required=True)
    p.add_argument('--remote-pilot',required=True)
    p.add_argument('--base',required=True)
    p.add_argument('--runtime',required=True)
    p.add_argument('--native',required=True)
    p.add_argument('--netproxy',required=True)
    p.add_argument('--deny',required=True)
    p.add_argument('--record',type=Path,required=True)
    a=p.parse_args()
    key=secrets.token_hex(8)
    name='chickadee-macos-pilot-'+key
    label='chickadee-macos-native-smoke'
    started=time.monotonic()
    process=None;runner_id=None;run_id=None
    result={'run_key':key,'runner_name':name,'repository':a.repo,'started_at':datetime.now(timezone.utc).isoformat(),'events':[]}
    command=[a.remote_python,a.remote_pilot,'--base',a.base,'--runtime',a.runtime,'--native',a.native,'--netproxy',a.netproxy,'--deny',a.deny]
    a.record.parent.mkdir(mode=0o700,parents=True,exist_ok=True)
    os.chmod(a.record.parent,0o700)
    try:
        process=subprocess.Popen(['ssh',a.ssh,shlex.join(command)],stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.DEVNULL,text=True,bufsize=1)
        events=queue.Queue()
        def collect():
            for line in process.stdout:
                if len(line)>4096:continue
                try:events.put(json.loads(line))
                except ValueError:continue
        threading.Thread(target=collect,daemon=True).start()
        dispatched=False
        deadline=time.monotonic()+800
        while time.monotonic()<deadline:
            try:event=events.get(timeout=1)
            except queue.Empty:
                if process.poll() is not None:break
                continue
            event['controller_seconds']=round(time.monotonic()-started,3)
            result['events'].append(event)
            print(json.dumps(event),flush=True)
            if event.get('event')=='READY' and not dispatched:
                before=time.monotonic()
                jit=api('POST',f'repos/{a.repo}/actions/runners/generate-jitconfig',{'name':name,'runner_group_id':1,'labels':['self-hosted','macOS','ARM64',label],'work_folder':'_work'})
                runner_id=jit['runner']['id'];result['runner_id']=runner_id;result['jit_api_seconds']=round(time.monotonic()-before,3)
                process.stdin.write(json.dumps({'jit':jit['encoded_jit_config'],'runner_name':name})+'\n');process.stdin.flush();jit.clear()
                api('POST',f'repos/{a.repo}/actions/workflows/native-macos-smoke.yml/dispatches',{'ref':'main','inputs':{'run_key':key,'runner_label':label}})
                dispatched=True
            if event.get('event') in ['PILOT_FAILED','EXIT_UNCONFIRMED_STATE_RETAINED']:
                raise RuntimeError('Mac pilot failed')
        process.wait(timeout=35)
        if process.returncode!=0 or not dispatched:
            raise RuntimeError('Runner pilot did not complete successfully')
        deadline=time.monotonic()+120
        while time.monotonic()<deadline:
            runs=api('GET',f'repos/{a.repo}/actions/workflows/native-macos-smoke.yml/runs?event=workflow_dispatch&per_page=50')['workflow_runs']
            matching=[r for r in runs if r.get('display_title')=='Native macOS acceptance '+key]
            if len(matching)==1:
                run_id=matching[0]['id'];result['run_id']=run_id;result['url']=matching[0]['html_url'];result['conclusion']=matching[0]['conclusion']
                if matching[0]['status']=='completed':break
            time.sleep(2)
        if result.get('conclusion')!='success':
            raise RuntimeError('Workflow has not succeeded')
        jobs=api('GET',f'repos/{a.repo}/actions/runs/{run_id}/jobs')['jobs']
        if len(jobs)!=1 or jobs[0].get('runner_name')!=name or jobs[0].get('conclusion')!='success':
            raise RuntimeError('Workflow runner/job evidence mismatch')
        result['job_id']=jobs[0]['id'];result['job_started_at']=jobs[0]['started_at'];result['job_completed_at']=jobs[0]['completed_at'];result['steps']=jobs[0]['steps']
        print(json.dumps({'event':'WORKFLOW_VERIFIED','url':result['url'],'job_id':result['job_id'],'runner_name':name}),flush=True)
        return 0
    except Exception:
        result['error']='Acceptance failed; inspect private lifecycle records'
        print(json.dumps({'event':'ACCEPTANCE_FAILED','run_key':key}),flush=True)
        return 1
    finally:
        if process is not None:
            if process.stdin:
                try:process.stdin.close()
                except BrokenPipeError:pass
            if process.poll() is None:
                # EOF tells the remote pilot to retire its VM. Do not kill SSH first.
                try:process.wait(timeout=45)
                except subprocess.TimeoutExpired:result['remote_exit_uncertain']=True
        if result.get('error'):
            try:
                runs=api('GET',f'repos/{a.repo}/actions/workflows/native-macos-smoke.yml/runs?event=workflow_dispatch&per_page=50')['workflow_runs']
                matching=[r for r in runs if r.get('display_title')=='Native macOS acceptance '+key]
                if len(matching)==1:
                    result['run_id']=matching[0]['id'];result['url']=matching[0]['html_url']
                    if matching[0]['status']!='completed':
                        api('POST',f'repos/{a.repo}/actions/runs/{matching[0]["id"]}/cancel')
            except Exception:
                result['workflow_cancel_uncertain']=True
        try:
            if runner_id is None:
                registrations=api('GET',f'repos/{a.repo}/actions/runners?per_page=100')['runners']
                matching=[r for r in registrations if r['name']==name]
                if len(matching)==1:runner_id=matching[0]['id']
            if runner_id is not None:
                registrations=api('GET',f'repos/{a.repo}/actions/runners?per_page=100')['runners']
                if any(r['id']==runner_id and r['name']==name for r in registrations):
                    api('DELETE',f'repos/{a.repo}/actions/runners/{runner_id}')
                result['runner_registration_cleaned']=True
        except Exception:
            result['runner_registration_cleanup_uncertain']=True
        fd=os.open(a.record,os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,0o600)
        with os.fdopen(fd,'w') as f:json.dump(result,f,indent=2);f.flush();os.fsync(f.fileno())


if __name__=='__main__':raise SystemExit(main())
