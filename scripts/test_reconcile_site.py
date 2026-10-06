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


class OrganizationWorkflows(unittest.TestCase):
 def setUp(self):
  self.entry={'account':{'type':'Organization','login':'Example'},'installation_id':123,'workflow_path':'.github/workflows/build.yaml'}
 def test_explicit_org_path_overrides_legacy_global_guess(self):
  self.assertEqual(m.workflow_for_request(self.entry,{'workflow':'.github/workflows/ci.yaml'},{}),'.github/workflows/build.yaml')
 def test_new_org_missing_path_is_not_silently_admitted(self):
  self.entry.pop('workflow_path')
  with self.assertRaises(m.WorkflowPathRequired):m.workflow_for_request(self.entry,{'workflow':'.github/workflows/ci.yaml'}, {})
 def test_existing_same_installation_keeps_legacy_policy_path(self):
  self.entry.pop('workflow_path');c={'scopes':{'old':{'github_url':'https://github.com/Example','app_installation_id':123}}}
  self.assertEqual(m.workflow_for_request(self.entry,{'workflow':'.github/workflows/legacy.yml'},c),'.github/workflows/legacy.yml')
 def test_reinstalled_scope_cannot_inherit_legacy_approval(self):
  self.entry.pop('workflow_path');c={'scopes':{'old':{'github_url':'https://github.com/Example','app_installation_id':122}}}
  with self.assertRaises(m.WorkflowPathRequired):m.workflow_for_request(self.entry,{},c)
 def test_invalid_org_paths_are_rejected(self):
  for path in ['https://github.com/org/repo/file.yml','.github/workflows/../../bad.yml','.github/workflows/build.yml@refs/heads/main','build.yml',42]:
   self.entry['workflow_path']=path
   with self.assertRaises(m.WorkflowPathRequired):m.workflow_for_request(self.entry,{}, {})
 def test_personal_scope_has_no_workflow_group_requirement(self):
  self.entry['account']['type']='User';self.entry.pop('workflow_path')
  self.assertEqual(m.workflow_for_request(self.entry,{},{}),'.github/workflows/chickadee.yml')


class RepositoryWorkflowAccess(unittest.TestCase):
 def setUp(self):self.entry={'account':{'type':'Organization'},'repository':{'id':7,'private':True}}
 def test_unapproved_repository_retains_workflow_restriction(self):
  self.assertEqual(m.workflow_access(self.entry,{'repository_workflow_access':{'8':'repository'}}),'workflow')
 def test_only_exact_private_org_repository_can_get_repository_mode(self):
  policy={'repository_workflow_access':{'7':'repository'}}
  self.assertEqual(m.workflow_access(self.entry,policy),'repository')
  self.entry['repository']['private']=False
  with self.assertRaises(ValueError):m.workflow_access(self.entry,policy)
  self.entry['repository']['private']=True;self.entry['account']['type']='User'
  with self.assertRaises(ValueError):m.workflow_access(self.entry,policy)
 def test_invalid_operator_mode_is_rejected(self):
  with self.assertRaises(ValueError):m.workflow_access(self.entry,{'repository_workflow_access':{'7':'all-org-repos'}})


class AutomaticQueueSelection(unittest.TestCase):
 def setUp(self):
  self.entry={'user':{'id':7},'queues':['chickadee-medium-ubuntu-2604','chickadee-small-rocky-102','not-a-host-profile']}
  self.policy={'approved_users':{'7':{'auto_queues':True,'max_vms':1}}}
  self.catalog={'chickadee':{},'chickadee-medium-ubuntu-2604':{},'chickadee-small-rocky-102':{}}
 def test_approved_customer_requests_known_catalog_labels_without_per_label_review(self):
  request=m.approved_request(self.entry,self.policy,self.catalog)
  self.assertEqual(request['queues'],['chickadee','chickadee-medium-ubuntu-2604','chickadee-small-rocky-102']);self.assertEqual(request['max_vms'],1)
 def test_automatic_policy_does_not_turn_on_unrequested_profiles(self):
  self.entry['queues']=[]
  self.assertEqual(m.approved_request(self.entry,self.policy,self.catalog)['queues'],['chickadee'])
 def test_automatic_queues_do_not_admit_an_unknown_account(self):
  self.entry['user']['id']=8
  self.assertIsNone(m.approved_request(self.entry,self.policy,self.catalog))
 def test_automatic_queues_require_host_catalog(self):
  with self.assertRaises(ValueError):m.approved_request(self.entry,self.policy)

class LiveConfigTransaction(unittest.TestCase):
 def test_live_update_keeps_running_vm_and_requires_matching_fresh_ack(self):
  import tempfile,json,datetime,hashlib,subprocess
  from unittest.mock import patch
  with tempfile.TemporaryDirectory() as tmp:
   root=pathlib.Path(tmp);runtime=root/'state';runtime.mkdir();(runtime/'vms').mkdir();(runtime/'vms'/'running-job').mkdir()
   config=root/'config.json';original={'state_dir':str(runtime),'revision':'old'};config.write_text(json.dumps(original));config.chmod(0o640)
   candidate={'state_dir':str(runtime),'revision':'new','job_timeout_seconds':60};calls=[]
   def run(args,**kw):
    calls.append(args)
    if '--signal=SIGHUP' in args:
     digest=hashlib.sha256(config.read_bytes()).hexdigest()
     (runtime/'reload.json').write_text(json.dumps({'config_sha256':digest,'status':'applied','updated_at':datetime.datetime.now(datetime.timezone.utc).isoformat()}))
    return subprocess.CompletedProcess(args,0)
   with patch.object(m.subprocess,'check_output',return_value='active\n'),patch.object(m.subprocess,'run',side_effect=run):m.install_config(candidate,config,set(),live_reload=True)
   self.assertEqual(json.loads(config.read_text()),candidate);self.assertEqual(config.stat().st_mode&0o777,0o640)
   self.assertTrue((runtime/'vms'/'running-job').exists())
   self.assertTrue(any('--signal=SIGHUP' in args for args in calls));self.assertFalse(any('--signal=SIGUSR1' in args or args[:2]==['systemctl','start'] for args in calls))
 def test_rejected_update_restores_exact_old_bytes_without_stopping_vms(self):
  import tempfile,json,datetime,hashlib,subprocess
  from unittest.mock import patch
  with tempfile.TemporaryDirectory() as tmp:
   root=pathlib.Path(tmp);runtime=root/'state';runtime.mkdir();(runtime/'vms').mkdir();(runtime/'vms'/'running-job').mkdir()
   config=root/'config.json';before=(json.dumps({'state_dir':str(runtime),'revision':'old'})+'\n').encode();config.write_bytes(before);calls=[]
   def run(args,**kw):
    calls.append(args)
    if '--signal=SIGHUP' in args:
     (runtime/'reload.json').write_text(json.dumps({'config_sha256':hashlib.sha256(config.read_bytes()).hexdigest(),'status':'rejected','updated_at':datetime.datetime.now(datetime.timezone.utc).isoformat()}))
    return subprocess.CompletedProcess(args,0)
   with patch.object(m.subprocess,'check_output',return_value='active\n'),patch.object(m.subprocess,'run',side_effect=run):
    with self.assertRaisesRegex(RuntimeError,'rejected'):m.install_config({'state_dir':str(runtime),'revision':'new'},config,set(),live_reload=True)
   self.assertEqual(config.read_bytes(),before);self.assertTrue((runtime/'vms'/'running-job').exists());self.assertFalse(any('--signal=SIGUSR1' in args for args in calls))
 def test_quarantine_never_uses_live_reload_with_a_remaining_vm(self):
  import tempfile,json,subprocess
  from unittest.mock import patch
  with tempfile.TemporaryDirectory() as tmp:
   root=pathlib.Path(tmp);runtime=root/'state';runtime.mkdir();(runtime/'vms').mkdir();(runtime/'vms'/'running-job').mkdir()
   config=root/'config.json';config.write_text(json.dumps({'state_dir':str(runtime)}));calls=[]
   def run(args,**kw):calls.append(args);return subprocess.CompletedProcess(args,1 if args[:2]==['systemctl','is-active'] else 0)
   with patch.object(m.subprocess,'check_output',return_value='active\n'),patch.object(m.subprocess,'run',side_effect=run):
    with self.assertRaisesRegex(RuntimeError,'VMs remain'):m.install_config({'state_dir':str(runtime),'job_timeout_seconds':60},config,{'https://github.com/revoked'},live_reload=True)
   self.assertTrue(any('--signal=SIGUSR1' in args for args in calls));self.assertFalse(any('--signal=SIGHUP' in args for args in calls));self.assertTrue((runtime/'vms'/'running-job').exists())

class LostLiveAcknowledgement(unittest.TestCase):
 def test_lost_ack_restores_and_confirms_old_runtime_without_killing_vms(self):
  import tempfile,json,hashlib,subprocess
  from unittest.mock import patch
  with tempfile.TemporaryDirectory() as tmp:
   root=pathlib.Path(tmp);runtime=root/'state';runtime.mkdir();config=root/'config.json'
   before=(json.dumps({'state_dir':str(runtime),'revision':'old'})+'\n').encode();config.write_bytes(before)
   with patch.object(m.subprocess,'run',return_value=subprocess.CompletedProcess([],0)) as run,patch.object(m,'wait_reload_ack',side_effect=[m.LiveReloadUncertain('lost ack'),None]) as acknowledgement:
    with self.assertRaisesRegex(m.LiveReloadUncertain,'previous runtime restored'):m.install_live_config({'state_dir':str(runtime),'revision':'new'},config)
   self.assertEqual(config.read_bytes(),before);self.assertEqual(run.call_count,2)
   self.assertTrue(all('--signal=SIGHUP' in call.args[0] for call in run.call_args_list))
   self.assertEqual(acknowledgement.call_args_list[1].args[1],hashlib.sha256(before).hexdigest());self.assertFalse((runtime/'admission-uncertain.json').exists())
 def test_failed_rollback_stops_further_admission(self):
  import tempfile,json,subprocess
  from unittest.mock import patch
  with tempfile.TemporaryDirectory() as tmp:
   root=pathlib.Path(tmp);runtime=root/'state';runtime.mkdir();config=root/'config.json';before=json.dumps({'state_dir':str(runtime)}).encode();config.write_bytes(before)
   with patch.object(m.subprocess,'run',return_value=subprocess.CompletedProcess([],0)),patch.object(m,'wait_reload_ack',side_effect=m.LiveReloadUncertain('lost ack')):
    with self.assertRaisesRegex(m.LiveReloadUncertain,'further admission stopped'):m.install_live_config({'state_dir':str(runtime),'revision':'new'},config)
   self.assertEqual(config.read_bytes(),before);self.assertTrue((runtime/'admission-uncertain.json').exists());self.assertEqual((runtime/'admission-uncertain.json').stat().st_mode&0o777,0o600)
 def test_stale_or_wrong_digest_ack_is_not_activation(self):
  import tempfile,json,datetime,time
  from unittest.mock import patch
  with tempfile.TemporaryDirectory() as tmp:
   root=pathlib.Path(tmp)
   for digest,timestamp in [('wrong',datetime.datetime.now(datetime.timezone.utc).isoformat()),('expected','1970-01-01T00:00:00Z')]:
    (root/'reload.json').write_text(json.dumps({'config_sha256':digest,'updated_at':timestamp,'status':'applied'}))
    with patch.object(m.time,'monotonic',side_effect=[0,151]):
     with self.assertRaises(m.LiveReloadUncertain):m.wait_reload_ack(root,'expected',time.time())
