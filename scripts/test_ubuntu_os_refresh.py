import importlib.util
import hashlib
import json
import io
import pathlib
import subprocess
import tempfile
import unittest
from unittest import mock

ROOT = pathlib.Path(__file__).resolve().parents[1]


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, ROOT / path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


prepare = load('ubuntu_prepare', 'scripts/prepare-ubuntu-os-inputs.py')
install = load('ubuntu_install', 'guest/refresh-ubuntu-os.py')
fetch = load('ubuntu_fetch', 'scripts/prefetch-ubuntu-os-inputs.py')
metadata = load('ubuntu_metadata', 'scripts/refresh-ubuntu-os-metadata.py')


def package(name, version, **fields):
    return dict(Package=name, Version=version, Architecture='amd64', **fields)


def parent(*rows):
    return {'packages': [{'name': name, 'version': version, 'architecture': 'amd64'} for name, version in rows]}


class NativeRefresh(unittest.TestCase):
    def test_debian_version_epoch_tilde_numeric_and_revision(self):
        for low, high in [('7.0.0-34.34', '7.0.0-38.38'), ('6.8.0-142.142', '6.8.0-147.147'), ('1.0~rc1-1', '1.0-1'), ('1:1-9', '2:0-1'), ('3.5.5-1ubuntu3.6', '3.5.5-1ubuntu3.7'), ('1.0a', '1.0+'), ('1', '1.0')]:
            self.assertLess(prepare.version_compare(low, high), 0)
            self.assertGreater(prepare.version_compare(high, low), 0)
        self.assertEqual(prepare.version_compare('1.0001-0', '1.1'), 0)

    def test_complete_installed_update_cohort_and_new_dependency(self):
        catalog = {'openssl': package('openssl', '2', Depends='libssl (= 2)'), 'libssl': package('libssl', '2'), 'sudo': package('sudo', '2'), 'uninstalled': package('uninstalled', '9')}
        selected, _ = prepare.closure(parent(('openssl', '1'), ('sudo', '1'), ('unaffected', '9')), catalog)
        self.assertEqual(set(selected), {'openssl', 'libssl', 'sudo'})

    def test_exact_signed_provider_avoids_alternative_initramfs(self):
        catalog = {'kernel': package('kernel', '2', Depends='dracut | linux-initramfs-tool'), 'initramfs-tools': package('initramfs-tools', '1', Provides='linux-initramfs-tool'), 'dracut': package('dracut', '3')}
        selected, _ = prepare.closure(parent(('kernel', '1'), ('initramfs-tools', '1')), catalog)
        self.assertEqual(set(selected), {'kernel'})
        catalog['initramfs-tools']['Version'] = '0'
        selected, _ = prepare.closure(parent(('kernel', '1'), ('initramfs-tools', '1')), catalog)
        self.assertIn('dracut', selected)  # Old/mismatching provider metadata cannot clear dependency.

    def test_no_downgrade_or_unresolved_dependency(self):
        catalog = {'a': package('a', '2', Depends='b (= 4)'), 'b': package('b', '4')}
        with self.assertRaisesRegex(ValueError, 'unresolved'):
            prepare.closure(parent(('a', '1'), ('b', '5')), catalog)
        selected, _ = prepare.closure(parent(('b', '5')), catalog)
        self.assertNotIn('b', selected)

    def test_control_paragraph_empty_field_does_not_pollute_provides(self):
        row = next(prepare.paragraphs('Package: x\nProvides: virtual-x\nConffiles:\n /etc/x hash\nVersion: 1\n'))
        self.assertEqual(row['Provides'], 'virtual-x')
        with self.assertRaises(ValueError):
            prepare.relation('libfoo [arm64]')

    def test_native_exact_plan_old_new_extra_removal_and_malformed(self):
        plan = {'packages': [{'name': 'openssl', 'version': '2'}, {'name': 'kernel', 'version': '3'}]}
        good = 'Inst openssl [1] (2 local-deb [amd64])\nInst kernel (3 local-deb [amd64])\nConf openssl (2 local-deb [amd64])\nConf kernel (3 local-deb [amd64])\n'
        self.assertEqual(install.validate_plan_output(good, plan), {'openssl': '2', 'kernel': '3'})
        for bad in [good.replace('(2 ', '(1 '), good + 'Remv old-kernel [1]\n', good + 'Inst extra (4 local-deb [amd64])\n', good.replace('Inst kernel (3 local-deb [amd64])\n', ''), good + 'Inst broken\n', good + 'Conf outside (1 local-deb [amd64])\n', good.replace('Conf openssl (2 ', 'Conf openssl (1 '), good.replace('Conf kernel (3 local-deb [amd64])\n', ''), good + 'Conf broken\n', good + 'Purg residue [1]\n']:
            with self.assertRaises(ValueError):
                install.validate_plan_output(bad, plan)

    def test_documented_simulation_descriptions_and_annotations_synthetic(self):
        # Synthetic cases from APT Describe/ShortBreaks syntax, not a recovered
        # row from a failed appliance. Package/version action sets stay exact.
        plan={'packages':[{'name':'openssl','version':'2'},{'name':'kernel','version':'3'}]}
        output='Inst openssl [1] (2 ) []\nConf openssl (2 )\nInst kernel (3  [amd64]) [openssl:amd64 ]\nConf kernel (3 local-deb [amd64]) []\n'
        self.assertEqual(install.validate_plan_output(output,plan),{'openssl':'2','kernel':'3'})
        for bad in [output+'Conf extra (1 )\n',output.replace('Conf openssl (2 )\n',''),output.replace('Inst openssl [1] (2 )','Inst openssl [1] (1 )'),output+'Inst extra (1 ) []\n',output.replace('[]','[Conf extra (1 )]',1),output+'Conf openssl (2 )\n',output+'Remv old [1]\n']:
            with self.assertRaises(ValueError):install.validate_plan_output(bad,plan)

    def test_simulation_output_persisted_before_parser_failure(self):
        with tempfile.TemporaryDirectory() as directory:
            report=pathlib.Path(directory)/'report.json';stdout='Inst malformed-native-row\n';stderr='diagnostic'
            result=subprocess.CompletedProcess(['apt-get'],0,stdout,stderr)
            original=install.validate_plan_output
            def validate(output,plan):
                self.assertEqual(report.with_suffix('.apt-plan.txt').read_text(),stdout)
                saved=json.loads(report.with_suffix('.apt-simulation.json').read_text())
                self.assertEqual(saved['stdout'],stdout);self.assertEqual(saved['stderr'],stderr);self.assertFalse(saved['accepted'])
                return original(output,plan)
            with mock.patch.object(install.subprocess,'run',return_value=result) as run, mock.patch.object(install,'validate_plan_output',side_effect=validate), mock.patch.object(install.sys,'stderr',io.StringIO()) as journal:
                with self.assertRaisesRegex(ValueError,'malformed-native-row'):
                    install.simulate_and_validate(['apt-get','--simulate'],{'packages':[]},report,{})
                self.assertIn('malformed-native-row',journal.getvalue())
                run.assert_called_once()
            saved=json.loads(report.with_suffix('.apt-simulation.json').read_text());self.assertFalse(saved['accepted']);self.assertIn('error',saved)

    def test_failed_simulation_exit_retains_both_streams_without_parsing(self):
        with tempfile.TemporaryDirectory() as directory:
            report=pathlib.Path(directory)/'report.json';result=subprocess.CompletedProcess(['apt-get'],100,'partial plan','native error')
            with mock.patch.object(install.subprocess,'run',return_value=result), mock.patch.object(install,'validate_plan_output') as validate, mock.patch.object(install.sys,'stderr',io.StringIO()):
                with self.assertRaises(subprocess.CalledProcessError):install.simulate_and_validate(['apt-get','--simulate'],{},report,{})
                validate.assert_not_called()
            saved=json.loads(report.with_suffix('.apt-simulation.json').read_text());self.assertEqual(saved['exit'],100);self.assertEqual(saved['stdout'],'partial plan');self.assertEqual(saved['stderr'],'native error');self.assertFalse(saved['accepted'])

    def test_successful_simulation_keeps_exact_action_acceptance_proof(self):
        with tempfile.TemporaryDirectory() as directory:
            report=pathlib.Path(directory)/'report.json';output='Inst pkg [1] (2 )\nConf pkg (2 )\n';result=subprocess.CompletedProcess(['apt-get'],0,output,'')
            with mock.patch.object(install.subprocess,'run',return_value=result):install.simulate_and_validate(['apt-get','--simulate'],{'packages':[{'name':'pkg','version':'2'}]},report,{})
            saved=json.loads(report.with_suffix('.apt-simulation.json').read_text());self.assertTrue(saved['accepted']);self.assertEqual(saved['stdout'],output)

    def test_native_post_transaction_preserves_all_other_packages(self):
        before = {'openssl': ('1', 'amd64'), 'php': ('fixed', 'amd64')}
        plan = {'packages': [{'name': 'openssl', 'version': '2', 'architecture': 'amd64'}]}
        good = dict(before, openssl=('2', 'amd64'))
        install.verify_after(before, good, plan)
        for bad in [before, dict(good, php=('old', 'amd64')), {'openssl': ('2', 'amd64')}]:
            with self.assertRaises(ValueError):
                install.verify_after(before, bad, plan)

    def test_kernel_profile_config_old_fails_new_passes(self):
        for family, symbols in [('24.04', ['VIRTIO_MMIO', 'VIRTIO_BLK', 'VIRTIO_NET']), ('26.04', ['VIRTIO_PCI', 'SCSI_VIRTIO', 'VIRTIO_BLK', 'VIRTIO_NET'])]:
            text = '\n'.join('CONFIG_' + name + '=m' for name in symbols) + '\nCONFIG_VIRTIO_MMIO_CMDLINE_DEVICES=y\n'
            install.validate_kernel_config(text, family)
            with self.assertRaises(ValueError):
                install.validate_kernel_config(text.replace('CONFIG_VIRTIO_BLK=m', '# CONFIG_VIRTIO_BLK is not set'), family)

    def test_cache_invalid_or_empty_input_fails_before_network(self):
        plan = {'packages': [{'filename': '../bad.deb', 'size': 1, 'sha256': '0'*64, 'url': ''}], 'download_bytes': 1, 'snapshot': 'x'}
        with tempfile.TemporaryDirectory() as directory, mock.patch.object(fetch.urllib.request, 'urlopen', side_effect=AssertionError('network must not run')):
            with self.assertRaises(ValueError):
                fetch.prefetch(plan, pathlib.Path(directory))
            with self.assertRaises(ValueError):
                fetch.verify({'filename': 'empty.deb', 'size': 1, 'sha256': '0'*64}, pathlib.Path(directory)/'missing')

    def test_metadata_current_inventory_kernel_and_original_snapshot(self):
        with tempfile.TemporaryDirectory() as directory:
            src=pathlib.Path(directory)/'src';out=pathlib.Path(directory)/'out';src.mkdir();out.mkdir()
            original={'kernel':'old','snapshot':'original-root-snapshot','machine':'q35'}
            before=[{'name':'libssl:amd64','version':'1','architecture':'amd64'}, {'name':'php','version':'fixed','architecture':'amd64'}]
            packages=[{'name':'libssl','version':'2','architecture':'amd64'}]
            plan={'kernel':'7.0.0-38-generic','snapshot':'new-native-snapshot','signed_indexes':[],'packages':packages,'parent_packages':before}
            for name,value in [('manifest.json',original),('ca-trust.json',{'retained':'yes'})]:(src/name).write_text(json.dumps(value))
            (src/'SHA256SUMS').write_text('parent-seal')
            for name in ['vmlinuz-7.0.0-38-generic','initrd-7.0.0-38-generic']:(out/name).write_bytes(b'new-kernel-or-initrd')
            digest=hashlib.sha256(b'new-kernel-or-initrd').hexdigest()
            report={'kernel':plan['kernel'],'native_apt_exact_plan':True,'packages':packages,'kernel_sha256':digest,'initrd_sha256':digest,'bootstrap_sha256':'retained'}
            after=[dict(before[0],version='2'),before[1]]
            for name,value in [('os-package-plan.json',plan),('os-security.json',report),('os-java-trust.json',{'java_stores':[{'sha256':'current'}]}),('package-provenance.json',{'packages':after})]:(out/name).write_text(json.dumps(value))
            with mock.patch.object(metadata.subprocess,'check_output',return_value='source-commit\n'):
                metadata.refresh(src,out)
                actual=json.loads((out/'manifest.json').read_text())
                self.assertEqual(actual['snapshot'],'original-root-snapshot');self.assertEqual(actual['kernel'],plan['kernel']);self.assertEqual(actual['machine'],'q35')
                (out/'package-provenance.json').write_text(json.dumps({'packages':before}))
                with self.assertRaises(ValueError):metadata.refresh(src,out)
                (out/'package-provenance.json').write_text(json.dumps({'packages':after}))
                (out/'vmlinuz-7.0.0-38-generic').write_bytes(b'old-kernel')
                with self.assertRaisesRegex(ValueError,'kernel/initrd'):metadata.refresh(src,out)

    def test_full_builder_snapshot_and_all_component_scope(self):
        policy=json.loads((ROOT/'guest/images/ubuntu-os-security.json').read_text())
        for release in policy['releases'].values():
            self.assertEqual(len(release['indexes']),3)
            for index in release['indexes']:self.assertEqual(set(index['components']),{'main','restricted','universe','multiverse'})
        for name,key in [('ubuntu-2604.json','snapshot'),('github-ubuntu-2604.json','ubuntu_snapshot'),('github-ubuntu-2404.json','ubuntu_snapshot')]:
            self.assertEqual(json.loads((ROOT/'guest/images'/name).read_text())[key],policy['snapshot'])
        self.assertIn('snapshot='+policy['snapshot'],(ROOT/'scripts/build-image.sh').read_text())

    def exercise_install_preflight(self, text, *, clean=False, query_exit=0,
                                   audit_exit=0, audit_stdout='', audit_stderr=''):
        class CleanStateReached(Exception):
            pass
        with tempfile.TemporaryDirectory() as directory:
            directory=pathlib.Path(directory);report=directory/'report.json';calls=[]
            original_read_text=pathlib.Path.read_text
            original_write_text=pathlib.Path.write_text
            def write_text(path,*args,**kwargs):
                if str(path).startswith('/usr/sbin/policy-rc.d'):
                    self.fail('Service policy write must not run')
                return original_write_text(path,*args,**kwargs)
            def read_text(path,*args,**kwargs):
                if str(path)=='/etc/os-release':return 'ID=ubuntu\nVERSION_ID="26.04"\n'
                return original_read_text(path,*args,**kwargs)
            def run(argv,**kwargs):
                calls.append(argv)
                if argv==install.DPKG_QUERY:
                    return subprocess.CompletedProcess(argv,query_exit,text,'query failed' if query_exit else '')
                if argv==['dpkg','--audit']:
                    return subprocess.CompletedProcess(argv,audit_exit,audit_stdout,audit_stderr)
                self.fail('No simulation/install command may run in this test: '+str(argv))
            plan={'family':'26.04','parent_packages':[{'name':'base','version':'1','architecture':'amd64'}]}
            with mock.patch.object(install.os,'geteuid',return_value=0), \
                 mock.patch.object(pathlib.Path,'read_text',read_text), \
                 mock.patch.object(pathlib.Path,'write_text',write_text), \
                 mock.patch.object(install.subprocess,'run',side_effect=run), \
                 mock.patch.object(install.subprocess,'check_output',side_effect=AssertionError('APT simulation must not run')), \
                 mock.patch.object(install,'digest',side_effect=CleanStateReached) as digest, \
                 mock.patch.object(pathlib.Path,'rename',side_effect=AssertionError('Service policy must not mutate')) as rename, \
                 mock.patch.object(pathlib.Path,'chmod',side_effect=AssertionError('Service policy chmod must not run')), \
                 mock.patch.object(pathlib.Path,'unlink',side_effect=AssertionError('Service policy unlink must not run')), \
                 mock.patch.object(install.sys,'stderr',io.StringIO()):
                expected=CleanStateReached if clean else (ValueError,subprocess.CalledProcessError)
                with self.assertRaises(expected):install.install(plan,directory,report,'0'*64)
                self.assertEqual(digest.call_count,1 if clean else 0)
                rename.assert_not_called()
            evidence=json.loads(report.with_suffix('.dpkg-preflight.json').read_text())
            self.assertEqual(evidence['accepted'],clean)
            self.assertTrue(all(command[0] in ['dpkg-query','dpkg'] for command in calls))
            return evidence

    def test_install_entrypoint_rejects_every_incomplete_error_state_before_apt(self):
        for state in ['iU ','iF ','iH ','iW ','it ','rU ','pc ','iiR','rcR','hi ','i','bogus']:
            with self.subTest(state=state):
                self.exercise_install_preflight('ii \tbase\t1\tamd64\n'+state+'\tpending\t2\tamd64\n')

    def test_install_entrypoint_clean_and_residual_configuration_pass_preflight(self):
        for extra in ['', 'rc \tresidue\told\tamd64\n', 'un \tuninstalled\t\t\nrn \tremoved\t\t\npn \tpurged\t\t\n']:
            with self.subTest(extra=extra):
                evidence=self.exercise_install_preflight('ii \tbase\t1\tamd64\n'+extra,clean=True)
                self.assertEqual(len(evidence['commands']),2)
                self.assertEqual(evidence['commands'][1]['argv'],['dpkg','--audit'])

    def test_install_entrypoint_query_and_audit_errors_retained_before_apt(self):
        for kwargs in [{'query_exit':2},{'audit_exit':2,'audit_stderr':'audit failed'}, {'audit_stdout':'pending configuration'}, {'audit_stderr':'unexpected diagnostic'}]:
            with self.subTest(kwargs=kwargs):
                evidence=self.exercise_install_preflight('ii \tbase\t1\tamd64\n',**kwargs)
                self.assertIn('error',evidence)
                self.assertEqual(evidence['commands'][-1]['exit'],kwargs.get('audit_exit',kwargs.get('query_exit',0)))
        for malformed in ['', 'ii \tbase\t1\n', 'rc \t\t1\tamd64\n', 'rc \tresidue\t\tamd64\n']:
            with self.subTest(malformed=malformed):self.exercise_install_preflight(malformed)

    def test_real_bash_wrapper_parent_failure_precedes_prepare_copy_and_edit(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory);(root/'scripts').mkdir();(root/'bin').mkdir();(root/'parent').mkdir();(root/'cache').mkdir();(root/'plan').write_text('{}')
            wrapper = root/'scripts/refresh-ubuntu-os-bundle.sh';wrapper.write_bytes((ROOT/'scripts/refresh-ubuntu-os-bundle.sh').read_bytes())
            mark = root/'forbidden'
            stubs = {'git': '#!/bin/sh\nexit 0\n', 'sha256sum': '#!/bin/sh\nexit 0\n', 'python3': '#!/bin/sh\ncase "$1" in scripts/check-image-bundle.py) exit 0;; scripts/check-runtime-parent.py) exit 1;; *) echo reached > "'+str(mark)+'";exit 99;; esac\n'}
            for name in ['qemu-img', 'virt-customize', 'guestfish', 'virt-sparsify']:
                stubs[name] = '#!/bin/sh\necho reached > "'+str(mark)+'"\nexit 99\n'
            for name, text in stubs.items():
                file=root/'bin'/name;file.write_text(text);file.chmod(0o755)
            result = subprocess.run(['/bin/bash', wrapper, root/'parent', root/'output', '0'*64, root/'plan', root/'cache'], env={'PATH': str(root/'bin')+':/usr/bin:/bin'}, capture_output=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse(mark.exists())
            self.assertFalse((root/'output').exists())


if __name__ == '__main__':
    unittest.main()
