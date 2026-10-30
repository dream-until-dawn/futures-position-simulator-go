"""真实登记编排+本地假CLI贯穿测试；不启动CTP或访问任何真实凭据。"""
from contextlib import contextmanager
import json
import os
from pathlib import Path
import sys
import types
import unittest
from unittest.mock import patch
import test_admission_checks as fixtures
from test_handshake_diagnostics import fixture
from readonly_admission import registered
from readonly_admission.errors import Rejected
from readonly_admission.runner import Attempt, run_once


def entries():
    rows=[]
    for stage,request,at in [('front',0,99000000),('authenticate',1,600000000),('login',2,1275779100),('settlement-confirm',3,4225065800),('general-error',0,0),('disconnected',0,0)]:
        present=at>0
        rows.append(f'[ctp.handshake.entry] stage={stage} count={int(present)} first_ns={at} last_ns={at} at_ns=10100000000 request={request} code=0 last={str(present).lower()} wrapped_payload={str(present).lower()}')
    return ('\n'.join(rows)+'\n').encode()


def successful_log():
    return fixture()+entries()+'[ctp] 握手完整成功，交易日=20261102，最大报单引用=0\n    MarginPriceType "4" 开仓价\n    Algorithm "1" 浮盈浮亏都计算\n    AvailIncludeCloseProfit "0" 包含\n'.encode()


class RegisteredTests(unittest.TestCase):
    def setUp(self):
        self.f=fixtures.AdmissionChecksTests(methodName='test_all_production_checks_are_wired')
        self.f.setUp();self.addCleanup(self.f.doCleanups)
        self.cfg=self.f.cfg
        self.cfg.update(operator_path=str(self.f.root/'operator.py'),trading_day='20261102')
        self.directory=self.f.root/'registered'
        self.log=successful_log();self.exit=0;self.env_reads=[];self.processes=[]
        self.locked=False;self.process_state=dict(matching_count=0,query_succeeded=True)
        self.env_error=None;self.allowed=True;self.query_error=None;self.expire_at_evidence=False
        self.original_command=Attempt.command

    @contextmanager
    def synthetic_env(self,path):
        self.assertEqual(path,self.f.root/'.env')
        self.env_reads.append('synthetic-only')
        if self.env_error:raise self.env_error
        self.locked=True
        try:yield '0'*64
        finally:self.locked=False

    def command(self,ctx,argv,cwd,**kwargs):
        if argv[0]=='powershell':
            self.assertTrue((self.directory/'001-start.json').exists(),'登记前查询进程')
            if self.query_error:raise self.query_error
            return json.dumps(self.process_state)
        if argv==self.cfg['argv']:
            self.assertTrue(self.locked,'实际CLI未处于凭据共享锁内')
            self.assertTrue(self.allowed,'越窗仍启动实际CLI')
            self.processes.append(list(argv))
            code='import sys;sys.stdout.buffer.write('+repr(self.log)+');sys.exit('+str(self.exit)+')'
            return self.original_command(ctx,[sys.executable,'-c',code],cwd,**kwargs)
        result=self.f.fake_command(ctx,argv,cwd,**kwargs)
        if self.expire_at_evidence and argv[-1]=='HEAD' and Path(cwd)==self.f.proof:self.allowed=False
        return result

    def run_registered(self):
        with patch.object(Attempt,'command',lambda ctx,*a,**kw:self.command(ctx,*a,**kw)),patch.object(registered,'locked_env',self.synthetic_env),patch.object(registered,'os',types.SimpleNamespace(name='nt',getpid=os.getpid)),patch.dict(os.environ,{'CTP_DLL_DIR':str(self.f.dll)}):
            return registered.run_registered(self.cfg,self.directory,lambda:self.allowed)

    def test_full_registered_path_and_unique_execution(self):
        result=self.run_registered()
        self.assertEqual(result['status'],'COMPLETED',result)
        self.assertEqual(self.env_reads,['synthetic-only'])
        self.assertEqual(self.processes,[self.cfg['argv']],'固定只读CLI接线不唯一')
        self.assertFalse(self.locked)
        self.assertTrue((self.directory/'handshake-diagnosis.json').exists(),'真实登记未生成握手诊断')
        again=self.run_registered()
        self.assertEqual(again['kind'],'existing-attempt','登记被再次执行')
        self.assertEqual(len(self.processes),1,'登记隐式重试')

    def test_process_query_failure_never_reads_env(self):
        self.query_error=PermissionError(13,'SYNTHETIC_PROCESS_PERMISSION')
        result=self.run_registered()
        self.assertEqual((result['stage'],result['kind']),('process','permission'),'进程查询未知被当0')
        self.assertFalse(self.env_reads);self.assertFalse(self.processes)

    def test_existing_process_never_reads_env(self):
        self.process_state['matching_count']=1
        result=self.run_registered()
        self.assertEqual(result['stage'],'process','已有未知进程未拒绝')
        self.assertFalse(self.env_reads);self.assertFalse(self.processes)

    def test_wrong_ready_never_reads_env(self):
        Path(self.cfg['ready']).write_bytes(b'{}')
        self.assertEqual(self.run_registered()['stage'],'ready','真实登记绕过准入')
        self.assertFalse(self.env_reads);self.assertFalse(self.processes)

    def test_trade_argv_is_rejected_before_env(self):
        self.cfg['argv']=[str(self.f.exe),'ctp-resting-price','-execute']
        self.assertEqual(self.run_registered()['stage'],'source','交易argv混入只读登记')
        self.assertFalse(self.env_reads);self.assertFalse(self.processes)

    def test_env_rejection_keeps_stage_and_cause(self):
        self.env_error=Rejected('SYNTHETIC_ENV_MISMATCH')
        result=self.run_registered()
        self.assertEqual(result['stage'],'env','凭据失败丢失阶段')
        self.assertFalse(self.processes)
        self.assertIn(b'SYNTHETIC_ENV_MISMATCH',(self.directory/result['private_error']['file']).read_bytes())
        self.assertNotIn('SYNTHETIC_ENV_MISMATCH',json.dumps(result))

    def test_wrong_day_cannot_complete(self):
        self.log=self.log.replace(b'20261102',b'20261030')
        result=self.run_registered()
        self.assertEqual((result['status'],result['stage']),('FAILED','diagnosis'),'错误交易日仍宣称完成')

    def test_missing_entry_evidence_cannot_complete(self):
        self.log=self.log.replace(entries(),b'')
        self.assertEqual(self.run_registered()['stage'],'diagnosis','缺入口段仍宣称完成')

    def test_unknown_parameter_cannot_complete(self):
        self.log=self.log.replace(b'MarginPriceType "4"',b'MarginPriceType "9"')
        self.assertEqual(self.run_registered()['stage'],'diagnosis','未知参数被当查询成功')

    def test_duplicate_parameter_cannot_complete(self):
        self.log+=b'    Algorithm "1" duplicate\n'
        self.assertEqual(self.run_registered()['stage'],'diagnosis','重复参数被任意选择')

    def test_nonzero_execution_stops_and_keeps_raw(self):
        self.exit=7
        result=self.run_registered()
        self.assertEqual((result['status'],result['stage'],result['exit_code']),('FAILED','execution',7),'执行失败被洗成成功')
        self.assertEqual(len(self.processes),1)
        self.assertEqual(next(self.directory.glob('command-*-private-combined.bin')).read_bytes(),self.log)

    def test_window_expiry_in_evidence_never_reads_env(self):
        self.expire_at_evidence=True
        result=self.run_registered()
        self.assertEqual(result['status'],'FAILED','证据核验越窗仍执行')
        self.assertFalse(self.env_reads);self.assertFalse(self.processes)

    def test_before_window_never_inspects_or_reads(self):
        self.allowed=False
        result=self.run_registered()
        self.assertEqual(result['kind'],'outside-window')
        self.assertFalse(self.directory.exists());self.assertFalse(self.env_reads);self.assertFalse(self.processes)

    def test_offline_diagnosis_of_failed_execution_does_not_reconnect(self):
        raw=fixture(False)+entries().replace(b'stage=settlement-confirm count=1 first_ns=4225065800 last_ns=4225065800',b'stage=settlement-confirm count=0 first_ns=0 last_ns=0').replace(b'request=3 code=0 last=true wrapped_payload=true',b'request=0 code=0 last=false wrapped_payload=false')
        def fail(ctx):
            ctx.command([sys.executable,'-c','import sys;sys.stdout.buffer.write('+repr(raw)+');sys.exit(1)'],self.f.root)
        result=run_once(self.directory,{},[('execution',fail)],lambda:True)
        self.assertEqual(result['status'],'FAILED')
        report=registered.diagnose(self.directory,self.f.source,'20261102')
        self.assertEqual(report['status'],'timeout','失败执行未保留可离线分析样本')
        body=json.loads((self.directory/'handshake-diagnosis.json').read_bytes())
        self.assertFalse(body['cases'][0]['analysis']['confirm_callback_absence_proven'])
        self.assertEqual(len(list(self.directory.glob('*-process.json'))),1,'离线诊断触发新连接')


if __name__=='__main__':unittest.main()
