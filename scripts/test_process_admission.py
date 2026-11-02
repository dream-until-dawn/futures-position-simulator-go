"""进程身份纯合成反例与真实无网络CIM探针；所有路径/命令均不含凭据。"""
from copy import deepcopy
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch
from readonly_admission import processes as p, registered
from readonly_admission.errors import Rejected
from readonly_admission.runner import Attempt, run_once


def sample():
    executable=r'C:\Python Space\python.exe'
    worker=r'C:\项目\scripts\process_worker.py'
    operator=r'C:\项目\ops\run_registered.py'
    exe=r'C:\Temp\ctp-readonly-synthetic.exe'
    powershell=r'C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe'
    def row(pid,parent,image,args,second):
        return dict(pid=pid,parent_pid=parent,name=Path(image.replace('\\','/')).name,
                    executable=image,command_line=json.dumps(args),created_at=f'2026-11-02T01:00:0{second}+00:00')
    rows=[row(100,99,executable,[executable,operator],0),
          row(101,100,executable,[executable,worker,*p.QUERY_ARGV],1),
          row(102,101,powershell,p.QUERY_ARGV,2)]
    snapshot=dict(format=1,query_succeeded=True,collector_pid=102,captured_at='2026-11-02T01:00:03+00:00',rows=rows)
    options=dict(current_pid=100,worker_pid=101,executable=executable,worker_path=worker,
                 operator_path=operator,exe_path=exe,powershell_path=powershell,split=json.loads)
    return snapshot,options


class ProcessIdentityTests(unittest.TestCase):
    def setUp(self):
        self.snapshot,self.options=sample()

    def assess(self):return p.assess(self.snapshot,**self.options)

    def test_exact_query_tree_has_zero_blockers_and_three_identities(self):
        state,rows=self.assess()
        self.assertEqual(state,dict(matching_count=0,query_succeeded=True),'本轮查询树被误报')
        self.assertEqual([x['role'] for x in rows],['current','query-worker','collector'],'本轮身份未逐项记录')
        self.assertEqual([x['pid'] for x in rows],[100,101,102])
        self.assertNotIn('command_line',json.dumps(rows),'身份摘要泄漏原始命令')

    def test_fixed_query_text_contains_no_operator_path(self):
        self.assertNotIn('run_registered.py',p.SNAPSHOT_SCRIPT,'查询文本再次嵌入入口路径')

    def test_duplicate_json_keys_cannot_replace_failed_query(self):
        with self.assertRaises(Rejected,msg='重复JSON键覆盖失败标记'):
            p.decode_snapshot('{"query_succeeded":false,"query_succeeded":true}')

    def test_old_readonly_operator_is_not_unrelated_python(self):
        row=deepcopy(self.snapshot['rows'][0]);row.update(pid=200,parent_pid=90,command_line=json.dumps([self.options['executable'],'C:/old/run_readonly.py']))
        self.snapshot['rows'].append(row)
        self.assertEqual(self.assess()[0]['matching_count'],1,'旧只读入口未阻断')

    def test_other_operator_and_readonly_executable_are_blockers(self):
        for kind in ['operator','relative','executor','mention']:
            with self.subTest(kind=kind):
                s,o=sample();row=deepcopy(s['rows'][0]);row.update(pid=200,parent_pid=90)
                if kind=='executor':row.update(name='ctp-readonly-synthetic.exe',executable=o['exe_path'],command_line=json.dumps([o['exe_path']]))
                else:row['command_line']=json.dumps([o['executable'],o['operator_path'] if kind=='operator' else 'run_registered.py' if kind=='relative' else '-c',o['operator_path']])
                s['rows'].append(row)
                state,ids=p.assess(s,**o)
                self.assertEqual(state['matching_count'],1,'其他执行器或入口未阻断')
                self.assertEqual(ids[-1]['role'],'blocker','阻断身份未留证')

    def test_unrelated_python_is_not_implicitly_a_second_round(self):
        row=deepcopy(self.snapshot['rows'][0]);row.update(pid=200,parent_pid=90,command_line=json.dumps([self.options['executable'],'C:/other/worker.py']))
        self.snapshot['rows'].append(row)
        state,rows=self.assess()
        self.assertEqual(state['matching_count'],0)
        self.assertEqual(rows[-1]['role'],'unrelated-python')

    def refused(self, message='不完整或伪装身份被放行'):
        with self.assertRaises(Rejected,msg=message):self.assess()

    def test_worker_parent_is_not_a_blanket_descendant_exemption(self):
        self.snapshot['rows'][1]['parent_pid']=99;self.refused('错误worker父PID被放行')

    def test_worker_creation_time_must_follow_current(self):
        self.snapshot['rows'][1]['created_at']='2026-11-02T00:59:59+00:00';self.refused('PID复用时间矛盾未拒绝')

    def test_worker_image_must_be_current_python(self):
        self.snapshot['rows'][1]['executable']='C:/evil/python.exe';self.refused('伪装worker映像被放行')

    def test_worker_arguments_must_be_exact(self):
        self.snapshot['rows'][1]['command_line']=json.dumps([self.options['executable'],self.options['worker_path'],'-c','unrelated'])
        self.refused('伪装worker参数被放行')

    def test_worker_pid_must_be_actual_supervised_pid(self):
        self.options['worker_pid']=500;self.refused('未核实际监管PID')

    def test_collector_parent_image_and_command_are_verified(self):
        for key,value in [('parent_pid',100),('executable','C:/evil/powershell.exe'),('command_line',json.dumps(['powershell','-Command','different']))]:
            with self.subTest(key=key):
                self.snapshot,self.options=sample();self.snapshot['rows'][2][key]=value
                self.refused('采集器身份被伪装')

    def test_current_image_must_match_actual_runtime(self):
        self.options['current_executable']='C:/other/python.exe';self.refused('本轮主进程映像未核')

    def test_launcher_image_and_runtime_image_are_separately_bound(self):
        self.snapshot['rows'][0]['executable']='C:/shim/python.exe'
        self.options['current_executable']='C:/shim/python.exe'
        self.assertEqual(self.assess()[0]['matching_count'],0,'合法Python启动shim被误报')
        self.snapshot['rows'][1]['executable']='C:/shim/python.exe'
        self.refused('运行时worker映像被shim假冒')

    def test_query_failure_or_nonboolean_success_is_not_zero(self):
        for value in [False,0,1,None]:
            self.snapshot['query_succeeded']=value;self.refused('查询未知被当成功')

    def test_duplicate_pid_is_not_overwritten(self):
        self.snapshot['rows'].append(deepcopy(self.snapshot['rows'][0]));self.refused('重复PID被覆盖')

    def test_unknown_snapshot_fields_do_not_silently_disappear(self):
        self.snapshot['ignored_field']='SYNTHETIC_PRIVATE'
        self.refused('未知快照字段被忽略')

    def test_missing_identity_fields_are_not_defaults(self):
        for field in ['executable','command_line','created_at','parent_pid','name']:
            with self.subTest(field=field):
                self.snapshot,self.options=sample();self.snapshot['rows'][1][field]=None
                self.refused('身份缺失被当完整')

    def test_snapshot_cannot_be_empty_or_missing_own_tree(self):
        self.snapshot['rows']=[];self.refused('空快照被当0进程')

    def test_future_or_naive_time_is_not_accepted(self):
        for row,second in zip(self.snapshot['rows'],[4,5,6]):
            row['created_at']=f'2026-11-02T01:00:0{second}+00:00'
        self.refused('创建时间晚于快照未拒绝')
        self.snapshot,self.options=sample();self.snapshot['rows'][1]['created_at']='2026-11-02T01:00:01'
        self.refused('创建时间未知未拒绝')

    def test_candidate_name_must_match_image_basename(self):
        row=deepcopy(self.snapshot['rows'][0]);row.update(pid=200,parent_pid=90,executable='C:/other/python3.exe',command_line=json.dumps(['python3.exe','other.py']))
        self.snapshot['rows'].append(row)
        self.refused('候选名称与映像不符仍放行')

    def test_noncurrent_descendant_is_not_ignored(self):
        row=deepcopy(self.snapshot['rows'][0]);row.update(pid=200,parent_pid=101)
        self.snapshot['rows'].append(row)
        self.assertEqual(self.assess()[0]['matching_count'],1,'其他子孙入口被整体忽略')

    def test_windows_parser_quotes_and_backslashes_or_platform_refusal(self):
        if os.name!='nt':
            with self.assertRaises(Rejected):p.windows_argv('python script.py')
            return
        argv=[r'C:\目录 空格\python.exe',r'C:\路径\run_registered.py','a"b',r'C:\trailing'+chr(92),'',p.SNAPSHOT_SCRIPT]
        self.assertEqual(p.windows_argv(subprocess.list2cmdline(argv)),argv,'Windows参数往返损坏')
        with self.assertRaises(Rejected):p.windows_argv('')

    def test_registered_uses_production_identity_guard(self):
        self.assertIs(registered.inspect,p.inspect,'生产入口未使用身份判别')
        class Context:
            def require(self,yes,why):
                if not yes:raise Rejected(why)
        with patch.object(registered,'inspect',return_value=dict(matching_count=1,query_succeeded=True)):
            with patch.object(registered,'os',type('NT',(),{'name':'nt'})):
                with self.assertRaises(Rejected,msg='生产计数守卫绕过'):registered.inspect_processes(Context(),{})


class ProcessEvidenceTests(unittest.TestCase):
    def run_fake(self,broken=False,bad=False):
        temporary=tempfile.TemporaryDirectory();self.addCleanup(temporary.cleanup)
        root=Path(temporary.name);snapshot,options=sample();calls=[]
        # 内部合成数据只替换采集结果和Windows解码，真实Journal/runner/guard仍执行。
        def command(ctx,*args,**kwargs):
            ctx.last_command_pid=101
            return '{bad-json' if bad else json.dumps(snapshot)
        original=p.assess
        def classify(raw,**kw):return original(raw,**options)
        cfg=dict(root=str(root),operator_path=options['operator_path'],exe=options['exe_path'])
        def inspect(ctx):
            if broken:
                saved=ctx.journal.event
                def event(kind,value):
                    if kind=='private-process-identities':raise OSError('SYNTHETIC_IDENTITY_WRITE')
                    return saved(kind,value)
                ctx.journal.event=event
            state=p.inspect(ctx,cfg)
            ctx.require(state==dict(matching_count=0,query_succeeded=True),'blocked')
        with patch.object(p,'os',type('NT',(),{'name':'nt','getpid':staticmethod(lambda:100),'environ':{}})),patch.object(p,'current_image',return_value=options['executable']),patch.object(Attempt,'command',command),patch.object(p,'assess',classify):
            result=run_once(root/'round',{},[('process',inspect)],lambda:True,lambda ctx:calls.append('synthetic-only'))
        return result,root/'round',calls

    def test_identity_is_persisted_before_action(self):
        result,directory,calls=self.run_fake()
        self.assertEqual(result['status'],'COMPLETED',result)
        files=list(directory.glob('*-private-process-identities.json'))
        self.assertEqual(len(files),1,'身份判断未持久化')
        body=json.loads(files[0].read_bytes());self.assertEqual(len(body['identities']),3,'身份树证据不完整')
        self.assertEqual(calls,['synthetic-only'])

    def test_identity_write_failure_blocks_action(self):
        result,directory,calls=self.run_fake(broken=True)
        self.assertEqual((result['status'],result['stage']),('FAILED','process'),'身份留证失败仍推进')
        self.assertFalse(calls,'身份留证失败仍执行')

    def test_invalid_snapshot_keeps_unknown_identity_and_no_action(self):
        result,directory,calls=self.run_fake(bad=True)
        self.assertEqual(result['status'],'FAILED');self.assertFalse(calls)
        body=json.loads(next(directory.glob('*-private-process-identities.json')).read_bytes())
        self.assertEqual(body['status'],'UNKNOWN','畸形身份未留未知证据')

    def test_real_windows_query_has_actual_identity_and_no_counterparty(self):
        if os.name!='nt':
            with self.assertRaises(Rejected):p.inspect(None,{})
            return
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp);cfg=dict(root=str(root),operator_path=str(root/'synthetic-operator.py'),exe=str(root/'synthetic-readonly.exe'))
            def check(ctx):
                state=p.inspect(ctx,cfg)
                ctx.require(state==dict(matching_count=0,query_succeeded=True),'real query has blockers')
            result=run_once(root/'round',{'kind':'real-cim-only'},[('process',check)],lambda:True)
            self.assertEqual(result['status'],'READY','真实查询树身份守卫误拒绝:'+json.dumps(result))
            body=json.loads(next((root/'round').glob('*-private-process-identities.json')).read_bytes())
            roles=[row['role'] for row in body['identities']]
            self.assertEqual(roles.count('query-worker'),1,'实际查询worker未留证')
            self.assertEqual(roles.count('collector'),1,'实际采集器未留证')
            self.assertFalse(result['action_attempted'])


if __name__=='__main__':unittest.main()
