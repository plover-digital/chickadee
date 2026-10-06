import importlib.util,pathlib,unittest
spec=importlib.util.spec_from_file_location('reconcile',pathlib.Path(__file__).with_name('reconcile-site.py'))
m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)
class Reconciliation(unittest.TestCase):
 def setUp(self):
  self.entry={'user':{'id':7},'account':{'id':7,'type':'User'},'repository':{'id':99},'queues':['chickadee-medium-ubuntu-2404']}
 def test_unknown_user_not_admitted(self):self.assertIsNone(m.approved_request(self.entry,{}))
 def test_trusted_user_defaults_to_default_queue_only(self):
  r=m.approved_request(self.entry,{'approved_users':{'7':{}}});self.assertEqual(r['queues'],['chickadee']);self.assertEqual(r['max_vms'],1)
 def test_queue_requires_user_request_and_operator_approval(self):
  policy={'approved_users':{'7':{'queues':['chickadee-medium-ubuntu-2404','chickadee-small-rocky-102']}}}
  r=m.approved_request(self.entry,policy);self.assertEqual(r['queues'],['chickadee','chickadee-medium-ubuntu-2404'])
 def test_scope_ownership_is_repository_or_org(self):
  self.assertEqual(m.scope_name(self.entry),'repo-99');self.entry['account']['type']='Organization';self.assertEqual(m.scope_name(self.entry),'org-7')

class ConfigTransaction(unittest.TestCase):
 def test_validated_update_is_atomic_and_waits_for_fresh_status(self):
  import tempfile,json,datetime,subprocess
  from unittest.mock import patch
  with tempfile.TemporaryDirectory() as tmp:
   root=pathlib.Path(tmp);runtime=root/'state';runtime.mkdir();(runtime/'vms').mkdir()
   config=root/'config.json';original={'revision':'old'};config.write_text(json.dumps(original));config.chmod(0o640)
   candidate={'revision':'new','state_dir':str(runtime),'job_timeout_seconds':60}
   calls=[]
   def run(args,**kw):
    calls.append(args)
    if args==['systemctl','start','chickadee']:
     (runtime/'status.json').write_text(json.dumps({'updated_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'draining':False}))
    return subprocess.CompletedProcess(args,0)
   with patch.object(m.subprocess,'check_output',return_value='inactive\n'),patch.object(m.subprocess,'run',side_effect=run):m.install_config(candidate,config,set())
   self.assertEqual(json.loads(config.read_text()),candidate)
   self.assertEqual(json.loads(config.with_name('config.json.before-managed-update').read_text()),original)
   self.assertEqual(config.stat().st_mode&0o777,0o640)
   self.assertEqual(calls[0][-1],'-check')
   self.assertIn(['systemctl','start','chickadee'],calls)
 def test_remaining_vm_blocks_config_replacement(self):
  import tempfile,json,subprocess
  from unittest.mock import patch
  with tempfile.TemporaryDirectory() as tmp:
   root=pathlib.Path(tmp);runtime=root/'state';runtime.mkdir();(runtime/'vms').mkdir();(runtime/'vms'/'still-owned').mkdir()
   config=root/'config.json';config.write_text('{"revision":"old"}')
   with patch.object(m.subprocess,'check_output',return_value='inactive\n'),patch.object(m.subprocess,'run',return_value=subprocess.CompletedProcess([],0)):
    with self.assertRaisesRegex(RuntimeError,'VMs remain'):m.install_config({'state_dir':str(runtime),'job_timeout_seconds':60},config,set())
   self.assertEqual(json.loads(config.read_text()),{'revision':'old'})
