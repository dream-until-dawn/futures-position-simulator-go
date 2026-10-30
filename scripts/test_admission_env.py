"""全部使用临时合成凭据；永不读取主项目.env。"""
from pathlib import Path
import hashlib
import subprocess
import sys
import tempfile
import unittest
import os
from readonly_admission.envfile import GateError,locked_env,parse_target

RAW=b'CTP_BROKER_ID=9999\nCTP_TD_FRONT=tcp://182.254.243.31:30001\nCTP_USER_ID=FAKE_ONLY\nCTP_PASSWORD=FAKE_SECRET\nCTP_APP_ID=FAKE_APP\nCTP_AUTH_CODE=FAKE_AUTH\nKQ_USER=FAKE_KQ\nKQ_PASSWORD=FAKE_KQ_SECRET\n'

class TargetTests(unittest.TestCase):
    def test_valid_and_last_duplicate_match_go(self):
        for raw in [RAW,b'\xef\xbb\xbf'+RAW,RAW+b'CTP_BROKER_ID="9999"\r\n',b'CTP_BROKER_ID=wrong\n'+RAW]:
            self.assertEqual(parse_target(raw),hashlib.sha256(raw).hexdigest())

    def test_wrong_target_no_secret_echo(self):
        for raw in [RAW.replace(b'9999',b'FAKE_SECRET'),RAW.replace(b':30001',b':40001'),RAW+b'CTP_BROKER_ID=wrong\n',RAW.replace(b'CTP_BROKER_ID',b'export CTP_BROKER_ID')]:
            with self.assertRaisesRegex(GateError,'^target-mismatch$',msg='wrong target accepted') as caught:parse_target(raw)
            self.assertNotIn('FAKE_SECRET',str(caught.exception))

    def test_missing_required_refused(self):
        for key in ['CTP_USER_ID','CTP_PASSWORD','CTP_APP_ID','CTP_AUTH_CODE','KQ_USER','KQ_PASSWORD']:
            raw=RAW+b'\n'+key.encode()+b'=\n'
            with self.assertRaisesRegex(GateError,'^required-field-missing$',msg='missing field accepted'):parse_target(raw)

    def test_encoding_size_and_go_space(self):
        for raw in [b'\xff'+RAW,b'#'+b'a'*1048576,RAW+b'#'+b'b'*65536]:
            with self.assertRaises(GateError,msg='invalid input accepted'):parse_target(raw)
        # Python通用strip会多吞\x1c，Go TrimSpace不会；不能据此误判目标。
        with self.assertRaisesRegex(GateError,'^target-mismatch$'):parse_target(RAW.replace(b'9999',b'\x1c9999\x1c'))

    def child(self,mode,path):
        script="from pathlib import Path; import sys; p=Path(sys.argv[2]); mode=sys.argv[1]\ntry:\n if mode=='read': assert len(p.read_bytes())>0\n elif mode=='write': p.write_bytes(b'replacement')\n elif mode=='replace':\n  other=p.with_suffix('.new'); other.write_bytes(b'replacement'); other.replace(p)\n else: p.unlink()\nexcept PermissionError: sys.exit(7)\n"
        return subprocess.run([sys.executable,'-c',script,mode,str(path)],capture_output=True,timeout=10).returncode

    def test_shared_read_blocks_write_and_replace(self):
        if os.name != 'nt':
            with self.assertRaisesRegex(GateError, '^platform-mismatch$'):
                with locked_env('MUST-NOT-READ.env'):self.fail('unsupported platform yielded')
            return
        with tempfile.TemporaryDirectory() as directory:
            path=Path(directory)/'synthetic.env';path.write_bytes(RAW)
            with locked_env(path) as digest:
                self.assertEqual(digest,hashlib.sha256(RAW).hexdigest())
                self.assertEqual(self.child('read',path),0,'shared read blocked')
                self.assertEqual(self.child('write',path),7,'write remained possible')
                self.assertEqual(self.child('delete',path),7,'delete remained possible')
                self.assertEqual(self.child('replace',path),7,'replace remained possible')
            self.assertEqual(self.child('write',path),0,'positive write after unlock failed')

    def test_missing_file_never_yields(self):
        with tempfile.TemporaryDirectory() as directory:
            expected='^env-lock-failed$' if os.name == 'nt' else '^platform-mismatch$'
            with self.assertRaisesRegex(GateError,expected,msg='missing file yielded'):
                with locked_env(Path(directory)/'absent.env'):self.fail('missing file yielded')

if __name__=='__main__':unittest.main()
