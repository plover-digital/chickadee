import importlib.util
import pathlib
import unittest

spec = importlib.util.spec_from_file_location('provenance', pathlib.Path(__file__).resolve().parents[1] / 'guest/package-provenance.py')
provenance = importlib.util.module_from_spec(spec)
spec.loader.exec_module(provenance)

class PackageProvenance(unittest.TestCase):
    def test_dpkg_filters_removed_packages_and_preserves_vendor_revision(self):
        result = provenance.parse_packages('ii \tbubblewrap\t0.9.0-1ubuntu0.3\tamd64\tbubblewrap\t0.9.0-1ubuntu0.3\nrc \told\t1\tamd64\told\t1\n', 'deb')
        self.assertEqual(len(result), 1)
        self.assertEqual(result[0]['version'], '0.9.0-1ubuntu0.3')
        self.assertEqual(result[0]['source_version'], '0.9.0-1ubuntu0.3')

    def test_held_installed_packages_are_not_omitted(self):
        result = provenance.parse_packages('hi \theld\t1\tamd64\theld\t1\n', 'deb')
        self.assertEqual(result[0]['name'], 'held')

    def test_rpm_preserves_epoch_architecture_and_source_rpm(self):
        result = provenance.parse_packages('package\t1:2.3-4.el10_2\tx86_64\tpackage-2.3-4.el10_2.src.rpm\n', 'rpm')
        self.assertEqual(result[0]['version'], '1:2.3-4.el10_2')
        self.assertEqual(result[0]['architecture'], 'x86_64')
        self.assertEqual(result[0]['source'], 'package-2.3-4.el10_2.src.rpm')

    def test_malformed_inventory_fails(self):
        with self.assertRaises(ValueError):
            provenance.parse_packages('incomplete', 'deb')
