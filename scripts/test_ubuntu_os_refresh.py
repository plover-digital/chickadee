import importlib.util
import hashlib
import json
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
        good = 'Inst openssl [1] (2 local-deb [amd64])\nInst kernel (3 local-deb [amd64])\nConf kernel (3 local-deb [amd64])\n'
        self.assertEqual(install.validate_plan_output(good, plan), {'openssl': '2', 'kernel': '3'})
        for bad in [good.replace('(2 ', '(1 '), good + 'Remv old-kernel [1]\n', good + 'Inst extra (4 local-deb [amd64])\n', good.replace('Inst kernel (3 local-deb [amd64])\n', ''), good + 'Inst broken\n']:
            with self.assertRaises(ValueError):
                install.validate_plan_output(bad, plan)

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
