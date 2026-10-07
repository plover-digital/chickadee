import importlib.util,pathlib,unittest
spec=importlib.util.spec_from_file_location('settings',pathlib.Path(__file__).with_name('profile-settings.py'))
m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)
class ProfileBudgets(unittest.TestCase):
 def test_catalog_limits_do_not_sum_per_profile_maxima(self):
  c={'profiles':{'default':{'image':'rocky','resources':'medium','max_vms':1},'explicit':{'image':'rocky','resources':'medium','max_vms':1}},'images':{'rocky':{'path':'/images/rocky','disk_gib':16}},'resource_classes':{'medium':{'cpus':4,'memory_mib':8192}},'limits':{'max_vms':2,'max_vcpus':6,'max_memory_mib':12288}}
  profiles,count,cpu,ram=m.settings(c)
  self.assertEqual((count,cpu,ram),(2,6,12288));self.assertEqual(profiles[0]['memory_mib'],8192)
 def test_legacy_limits_include_every_vm(self):
  c={'max_vms':2,'cpus':2,'memory_mib':2048,'image_dir':'/images'}
  _,count,cpu,ram=m.settings(c)
  self.assertEqual((count,cpu,ram),(2,4,4096))
 def test_resources_include_explicit_cgroup_overhead_and_task_budget(self):
  import contextlib,io,json,tempfile
  from unittest.mock import patch
  c={'max_vms':2,'cpus':2,'memory_mib':2048,'image_dir':'/images','cgroup':{'memory_overhead_mib':768,'pids_max':200}}
  with tempfile.TemporaryDirectory() as tmp:
   path=pathlib.Path(tmp)/'config.json';path.write_text(json.dumps(c));out=io.StringIO()
   with patch.object(m.sys,'argv',['settings','resources',str(path)]),contextlib.redirect_stdout(out):m.main()
  self.assertIn('MemoryMax=6144M',out.getvalue());self.assertIn('TasksMax=464',out.getvalue());self.assertIn('CPUQuota=400%',out.getvalue())
 def test_empty_cgroup_object_enables_default_limits(self):
  import contextlib,io,json,tempfile
  from unittest.mock import patch
  c={'max_vms':2,'cpus':2,'memory_mib':2048,'image_dir':'/images','cgroup':{}}
  with tempfile.TemporaryDirectory() as tmp:
   path=pathlib.Path(tmp)/'config.json';path.write_text(json.dumps(c));out=io.StringIO()
   with patch.object(m.sys,'argv',['settings','resources',str(path)]),contextlib.redirect_stdout(out):m.main()
  self.assertIn('MemoryMax=5632M',out.getvalue());self.assertIn('TasksMax=320',out.getvalue())
