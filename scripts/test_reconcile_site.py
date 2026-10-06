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
