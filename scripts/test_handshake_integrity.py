"""合成证据的时间一致性正反例，不接触真实凭据或柜台。"""
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

from handshake_diagnostics.analysis import analyze
from handshake_diagnostics.parsing import EvidenceError, parse_log
from test_handshake_diagnostics import case, fixture, line


class HandshakeIntegrityTests(unittest.TestCase):
    def reject(self, raw, message):
        with self.assertRaises(EvidenceError, msg=message):
            analyze(parse_log(raw), 10000000000, 1)

    def test_response_deadline_is_exclusive(self):
        for at in [b'10s', b'10.000000001s']:
            self.reject(fixture().replace(b'4.2250658s', at), '截止后接受响应未拒绝')
        good = fixture().replace(b'4.2250658s', b'9.999999999s')
        self.assertEqual(analyze(parse_log(good), 10000000000, 0)['status'], 'handshake-complete')

    def test_response_cannot_precede_begin(self):
        raw = fixture().replace(b'event=response code=0 elapsed=4.2250658s', b'event=response code=0 elapsed=1s')
        self.reject(raw, '响应早于阶段开始未拒绝')

    def test_send_cannot_precede_begin(self):
        raw = fixture().replace(b'event=send-return code=0 elapsed=1.2757791s', b'event=send-return code=0 elapsed=1s')
        self.reject(raw, '发送返回早于阶段开始未拒绝')

    def test_finished_cannot_precede_response(self):
        raw = fixture().replace(b'event=finished code=0 elapsed=4.2250658s', b'event=finished code=0 elapsed=2s')
        self.reject(raw, '完成早于响应未拒绝')

    def test_finished_requires_response(self):
        rows = [row for row in fixture().decode().splitlines() if not ('stage=settlement-confirm' in row and 'event=response ' in row)]
        self.reject(('\n'.join(rows)+'\n').encode(), '缺失响应的完成未拒绝')

    def test_timeout_not_before_deadline(self):
        self.reject(fixture(False).replace(b'10.0005823s', b'9.999999999s'), '提前超时未拒绝')
        good = fixture(False).replace(b'10.0005823s', b'10s')
        self.assertEqual(analyze(parse_log(good), 10000000000, 1)['status'], 'timeout')

    def test_begin_must_precede_deadline(self):
        rows = fixture(False).decode().splitlines()[:-3]
        rows += [line('settlement-confirm', 3, 'begin', '10s')]
        self.reject(('\n'.join(rows)+'\n').encode(), '截止后阶段开始未拒绝')

    def test_finished_must_precede_deadline(self):
        raw = fixture().replace(b'event=finished code=0 elapsed=4.2250658s', b'event=finished code=0 elapsed=10s')
        self.reject(raw, '截止后阶段完成未拒绝')

    def test_sync_callback_and_interleaved_print_remain_valid(self):
        raw = fixture().replace(b'event=send-return code=0 elapsed=1.2757791s', b'event=send-return code=0 elapsed=5s')
        rows = raw.decode().splitlines()
        rows[1], rows[4] = rows[4], rows[1]
        result = analyze(parse_log(('\n'.join(rows)+'\n').encode()), 10000000000, 0)
        self.assertEqual(result['status'], 'handshake-complete')
        self.assertTrue(result['confirm_response_before_send_return'])
        self.assertTrue(result['record_order_differs_from_event_time'])

    def test_cli_conflict_does_not_publish_report(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            item = case(root/'input.log', fixture().replace(b'4.2250658s', b'10s'))
            source = root/'manifest.json'
            source.write_text(json.dumps([item]), encoding='utf-8')
            out = root/'out.json'
            command = [sys.executable, str(Path(__file__).with_name('diagnose_handshake.py')), '--manifest', str(source), '--out', str(out)]
            result = subprocess.run(command, capture_output=True)
            self.assertNotEqual(result.returncode, 0, '矛盾时间线CLI仍成功')
            self.assertFalse(out.exists(), '矛盾时间线仍发布报告')
            self.assertNotIn(str(root).encode(), result.stderr)


if __name__ == '__main__':
    unittest.main()
