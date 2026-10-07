import importlib.util
import os
from pathlib import Path
import subprocess
import unittest
from unittest.mock import patch

spec=importlib.util.spec_from_file_location('acceptance',Path(__file__).with_name('network-namespace-acceptance.py'))
m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)
class PolicyAcceptanceTests(unittest.TestCase):
    def test_counter_annotations_preserve_policy(self):
        original='iifname "ck*" ip saddr != 10.203.1.2 drop\niifname "ck*" oifname "eth0" accept'
        counted=m.counter_rules(original)
        self.assertEqual(counted.replace('counter drop','drop'),original)
        self.assertEqual(counted.count('counter'),1)
    def test_refuses_shared_network_or_mount_namespace(self):
        class Stat:
            def __init__(self,inode):self.st_ino=inode
        for shared in ('net','mnt'):
            def stat(path):
                kind=Path(path).name
                return Stat(1 if kind==shared or '/self/' in str(path) else 2)
            with patch.object(m.os,'geteuid',return_value=0),patch.object(m.os,'stat',side_effect=stat):
                with self.assertRaises(ValueError):m.require_private_namespaces()
    @unittest.skipUnless(os.environ.get('CHICKADEE_NETWORK_NS_TEST')=='1','operator opt-in isolated namespace kernel test')
    def test_isolated_kernel_policy(self):
        result=subprocess.run(['sudo','-n','unshare','--net','--mount','--propagation','private','python3',str(Path(m.__file__).resolve())],capture_output=True,text=True,timeout=65)
        self.assertEqual(result.returncode,0,result.stdout)
        self.assertIn('"policy_reproduction": "passed"',result.stdout)
        self.assertIn('"live_host_acceptance": "not_tested"',result.stdout)
if __name__=='__main__':unittest.main()
