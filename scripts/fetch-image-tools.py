#!/usr/bin/env python3
"""Fetch exact image tool inputs; cached and new files must match the lock."""
import concurrent.futures,hashlib,json,pathlib,sys,time,urllib.request

def fetch_tools(lock_path, destination):
    lock=json.loads(pathlib.Path(lock_path).read_text());root=pathlib.Path(destination);root.mkdir(parents=True,exist_ok=True)
    def fetch(e):
        name=e['filename']
        if pathlib.Path(name).name!=name or not e['url'].startswith('https://github.com/actions/') or len(e['sha256'])!=64:
            raise ValueError('invalid locked download')
        p=root/name
        if not p.exists():
            partial=p.with_suffix(p.suffix+'.part')
            for attempt in range(3):
                try:
                    with urllib.request.urlopen(e['url'],timeout=120) as response,partial.open('wb') as output:
                        while chunk:=response.read(1024*1024):output.write(chunk)
                    partial.rename(p);break
                except Exception:
                    partial.unlink(missing_ok=True)
                    if attempt==2:raise
                    time.sleep(attempt+1)
        with p.open('rb') as data:digest=hashlib.file_digest(data,'sha256').hexdigest()
        if digest!=e['sha256']:raise ValueError('checksum mismatch: '+name)
        print('Verified '+name,flush=True)
    with concurrent.futures.ThreadPoolExecutor(max_workers=3) as executor:list(executor.map(fetch,lock['tools']))
if __name__=='__main__':fetch_tools(*sys.argv[1:])
