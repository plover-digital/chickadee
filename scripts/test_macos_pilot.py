import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('macos_pilot', Path(__file__).parent/'macos/pilot.py')
pilot = importlib.util.module_from_spec(spec)
spec.loader.exec_module(pilot)


class MacPilotTests(unittest.TestCase):
    def test_failed_credential_write_still_spends_vm(self):
        with tempfile.TemporaryDirectory() as d:
            state=Path(d)
            with patch.object(pilot.os,'pwrite',side_effect=OSError('injected disk failure')):
                with self.assertRaises(OSError):
                    pilot.deliver(-1,state,'a'*32,'YWJj','pilot-test')
            self.assertTrue(json.loads((state/'credential-intent.json').read_text())['spent'])
            with patch.object(pilot.os,'pwrite') as write:
                with self.assertRaises(FileExistsError):
                    pilot.deliver(-1,state,'a'*32,'YWJj','pilot-test')
                write.assert_not_called()

    def test_invalid_guest_status_rejected(self):
        good=dict(v=1,nonce='a'*32,type='READY',sequence=0,code=0,log_size=0,log_sha256='')
        cases=[dict(good,nonce='b'*32),dict(good,sequence=3),dict(good,type='UNKNOWN'),dict(good,log_size=pilot.MAX_LOGS+1),dict(good,code=256),dict(good,unexpected='value')]
        with tempfile.TemporaryFile() as f:
            f.truncate(pilot.DISK)
            for message in cases:
                os.pwrite(f.fileno(),json.dumps(message).encode().ljust(pilot.PAGE,b'\0'),pilot.PAGE)
                self.assertIsNone(pilot.status(f.fileno(),'a'*32))
            os.pwrite(f.fileno(),json.dumps(good).encode().ljust(pilot.PAGE,b'\0'),pilot.PAGE)
            self.assertEqual(pilot.status(f.fileno(),'a'*32)['type'],'READY')
