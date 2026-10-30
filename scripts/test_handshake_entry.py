"""入口快照只使用合成日志；旧日志缺项不能自动补零。"""
from pathlib import Path
import tempfile
import unittest

from handshake_diagnostics.parsing import EvidenceError
from handshake_diagnostics.reporting import build_report
from test_handshake_diagnostics import case, fixture


def entries():
    return ('\n'.join(f'[ctp.handshake.entry] stage={stage} count=0 first_ns=0 last_ns=0 at_ns=10000000000 request=0 code=0 last=false wrapped_payload=false'
        for stage in ['front','authenticate','login','settlement-confirm','general-error','disconnected'])+'\n').encode()


class EntryTests(unittest.TestCase):
    def report(self,raw):
        with tempfile.TemporaryDirectory() as directory:
            return build_report([case(Path(directory)/'log',raw)])

    def test_truncated_entry_rejected(self):
        with self.assertRaisesRegex(EvidenceError,'^incomplete-entry-journal$',msg='残缺入口快照被当成完整证据'):
            self.report(fixture()+entries().splitlines(keepends=True)[0])

    def test_missing_is_unknown(self):
        report=self.report(fixture())
        self.assertIsNone(report['cases'][0]['entry_journal'],'旧日志缺失入口段被补零')
        self.assertEqual(report['format'],2)

    def test_complete_zero_and_metadata(self):
        raw=fixture()+entries().replace(b'stage=settlement-confirm count=0 first_ns=0 last_ns=0',b'stage=settlement-confirm count=2 first_ns=3000000000 last_ns=4000000000').replace(b'at_ns=10000000000 request=0 code=0 last=false wrapped_payload=false\n[ctp.handshake.entry] stage=general-error',b'at_ns=10000000000 request=3 code=-3 last=true wrapped_payload=true\n[ctp.handshake.entry] stage=general-error')
        journal=self.report(raw)['cases'][0]['entry_journal']
        self.assertEqual(journal['settlement-confirm']['count'],2)
        self.assertEqual(journal['settlement-confirm']['request'],3)
        self.assertEqual(journal['settlement-confirm']['code'],-3)
        self.assertIsNone(journal['front']['request'],'零入口却输出不存在的回调元数据')

    def test_invalid_entries_rejected(self):
        good=entries()
        variants=[
            good+good,
            good.replace(b'stage=front',b'stage=unknown',1),
            good.replace(b'count=0',b'count=-1',1),
            good.replace(b'count=0',b'count=18446744073709551616',1),
            good.replace(b'first_ns=0',b'first_ns=1',1),
            good.replace(b'request=0',b'request=1',1),
            good.replace(b'last=false',b'last=yes',1),
            good.replace(b'at_ns=10000000000',b'at_ns=1',1).replace(b'count=0 first_ns=0 last_ns=0',b'count=1 first_ns=2 last_ns=2',1),
            good.replace(b'count=0 first_ns=0 last_ns=0',b'count=1 first_ns=2 last_ns=3',1),
            good.replace(b'count=0 first_ns=0 last_ns=0',b'count=2 first_ns=3 last_ns=2',1),
            good.replace(b'count=0',b'count=0 count=0',1),
            good.replace(b'[ctp.handshake.entry]',b'[ctp.handshake.entry',1),
        ]
        for raw in variants:
            with self.subTest(raw=variants.index(raw)), self.assertRaises(EvidenceError,msg='非法入口快照未拒绝'):
                self.report(fixture()+raw)


if __name__=='__main__': unittest.main()
