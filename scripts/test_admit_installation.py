import copy,importlib.util,pathlib,unittest
spec=importlib.util.spec_from_file_location('admit',pathlib.Path(__file__).with_name('admit-installation.py'))
m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)
class Admission(unittest.TestCase):
 def setUp(self):
  self.config={'github_url':'https://github.com/EXAMPLE-ORG','app_installation_id':1,'runner_group_id':2,'profiles':{'chickadee':{'image':'rocky-102','resources':'medium','warm_pool':1,'max_vms':1}},'limits':{'max_vms':2,'max_vcpus':6,'max_memory_mib':12288}}
  self.repo={'id':7,'full_name':'example-user/example-repo'}
  self.install={'id':3,'account':{'id':4,'login':'example-user','type':'User'}}
  self.request={'account':self.install['account'],'repository':self.repo}
 def test_personal_scope_keeps_global_limits_and_existing_warm(self):
  c=m.proposed(self.config,self.request,self.install,self.repo,1)
  self.assertEqual(c['limits'],self.config['limits']);self.assertNotIn('github_url',c)
  self.assertEqual(c['scopes']['primary']['profiles']['chickadee']['warm_pool'],1)
  scope=c['scopes']['repo-7'];self.assertEqual(scope['github_url'],'https://github.com/example-user/example-repo');self.assertEqual(scope['profiles']['chickadee']['warm_pool'],0)
  self.assertEqual(m.proposed(c,self.request,self.install,self.repo,1),c)
 def test_identity_mismatch_rejected(self):
  bad=copy.deepcopy(self.request);bad['repository']['id']=8
  with self.assertRaises(ValueError):m.proposed(self.config,bad,self.install,self.repo,1)
 def test_org_scope_is_not_personal_repo_scope(self):
  self.install['account']['type']='Organization'
  c=m.proposed(self.config,self.request,self.install,self.repo,9)
  self.assertEqual(c['scopes']['org-4']['github_url'],'https://github.com/example-user')
  self.assertEqual(c['scopes']['org-4']['runner_group_id'],9)
