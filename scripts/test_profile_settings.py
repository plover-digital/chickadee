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
