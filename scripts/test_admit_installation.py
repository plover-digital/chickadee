import copy,importlib.util,pathlib,unittest
spec=importlib.util.spec_from_file_location('admit',pathlib.Path(__file__).with_name('admit-installation.py'))
m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)
class Admission(unittest.TestCase):
 def setUp(self):
  self.config={'github_url':'https://github.com/EXAMPLE-ORG','app_installation_id':1,'runner_group_id':2,'profiles':{'chickadee':{'image':'rocky-102','resources':'medium','warm_pool':1,'max_vms':1}},'limits':{'max_vms':2,'max_vcpus':6,'max_memory_mib':12288}}
  self.config['profiles']['chickadee-medium-ubuntu-2404']={'image':'ubuntu-2404','resources':'medium','warm_pool':0,'max_vms':1}
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

 def test_default_only_and_explicit_additional_queue(self):
  c=m.proposed(self.config,self.request,self.install,self.repo,1)
  self.assertEqual(list(c['scopes']['repo-7']['profiles']),['chickadee'])
  self.request['queues']=['chickadee-medium-ubuntu-2404']
  c=m.proposed(c,self.request,self.install,self.repo,1)
  self.assertEqual(set(c['scopes']['repo-7']['profiles']),{'chickadee','chickadee-medium-ubuntu-2404'})
  self.request.pop('queues')
  self.assertEqual(m.proposed(c,self.request,self.install,self.repo,1),c)
 def test_unknown_queue_rejected(self):
  self.request['queues']=['chickadee-large-ubuntu-2404']
  with self.assertRaises(ValueError):m.proposed(self.config,self.request,self.install,self.repo,1)


class RunnerGroupPolicy(unittest.TestCase):
 def test_default_group_restricts_exact_selected_workflow(self):
  body=m.runner_group_plan('owned',{'id':7,'private':True},'example/repo/.github/workflows/ci.yml@refs/heads/main')
  self.assertTrue(body['restricted_to_workflows']);self.assertEqual(body['selected_repository_ids'],[7]);self.assertFalse(body['allows_public_repositories']);self.assertEqual(len(body['selected_workflows']),1)
 def test_explicit_repository_mode_is_private_and_selected(self):
  body=m.runner_group_plan('owned',{'id':7,'private':True},'unused',True)
  self.assertFalse(body['restricted_to_workflows']);self.assertEqual(body['selected_workflows'],[]);self.assertEqual(body['visibility'],'selected');self.assertEqual(body['selected_repository_ids'],[7]);self.assertFalse(body['allows_public_repositories'])
 def test_repository_mode_cannot_allow_public_repository(self):
  with self.assertRaises(ValueError):m.runner_group_plan('owned',{'id':7,'private':False},'unused',True)
 def test_repository_mode_cannot_broaden_another_existing_repository(self):
  m.validate_repository_only_selection({'total_count':1,'repositories':[{'id':7}]},7)
  for listing in [{'total_count':2,'repositories':[{'id':7},{'id':8}]},{'total_count':1,'repositories':[{'id':8}]},{'total_count':2,'repositories':[{'id':7}]}]:
   with self.assertRaises(ValueError):m.validate_repository_only_selection(listing,7)
