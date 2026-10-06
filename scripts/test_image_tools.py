#!/usr/bin/env python3
import importlib.util,json,pathlib,tempfile,unittest
spec=importlib.util.spec_from_file_location('fetch',pathlib.Path(__file__).with_name('fetch-image-tools.py'));fetch=importlib.util.module_from_spec(spec);spec.loader.exec_module(fetch)
spec2=importlib.util.spec_from_file_location('audit',pathlib.Path(__file__).with_name('audit-image-compatibility.py'));audit=importlib.util.module_from_spec(spec2);spec2.loader.exec_module(audit)
class Inputs(unittest.TestCase):
    def test_tampered_cached_download_is_rejected(self):
        with tempfile.TemporaryDirectory() as d:
            root=pathlib.Path(d);(root/'archive.tar.gz').write_bytes(b'corrupt')
            lock=root/'lock.json';lock.write_text(json.dumps({'tools':[{'filename':'archive.tar.gz','url':'https://github.com/actions/node-versions/releases/download/pinned/archive.tar.gz','sha256':'0'*64}]}))
            with self.assertRaisesRegex(ValueError,'checksum mismatch'):fetch.fetch_tools(lock,root)
    def test_traversal_input_is_rejected_before_network(self):
        with tempfile.TemporaryDirectory() as d:
            lock=pathlib.Path(d)/'lock.json';lock.write_text(json.dumps({'tools':[{'filename':'../archive.tar.gz','url':'https://github.com/actions/x','sha256':'0'*64}]}))
            with self.assertRaisesRegex(ValueError,'invalid locked'):fetch.fetch_tools(lock,d)
    def test_version_audit_distinguishes_missing_and_mismatched(self):
        report=audit.compare({'git':'2','unzip':'6'},{'git':'1'})
        self.assertEqual(report['missing_packages'],['unzip']);self.assertEqual(report['different_versions'],[{'name':'git','expected':'2','actual':'1'}])
if __name__=='__main__':unittest.main()
