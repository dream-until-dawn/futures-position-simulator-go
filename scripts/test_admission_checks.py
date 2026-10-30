"""完整生产准入接线的合成证据；Git远端与构建输出注入，不访问网络或真实.env。"""
import hashlib
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
from readonly_admission.checks import Checks
from readonly_admission.runner import Attempt, run_once


def sha(raw):return hashlib.sha256(raw).hexdigest()


class AdmissionChecksTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.proof = self.root/'proof';self.proof.mkdir()
        (self.proof/'input.txt').write_bytes(b'original source')
        self.source='a'*40;self.main='b'*40;self.tree='c'*40
        self.index=b'100644 abc input.txt\0'
        self.state=dict(status='',head=self.source,tracked_sha256=sha(b'input.txt\0'+hashlib.sha256(b'original source').digest()),index_sha256=sha(self.index))
        self.registry=[dict(name='1 synthetic',pkg='.',test='TestSynthetic',expect='red',want='synthetic assertion')]
        self.registry_raw=(json.dumps(self.registry)+'\n').encode()
        self.report=self.root/'mutations.json';self.report.write_text(json.dumps([dict(self.registry[0],verdict='红对了',detail='synthetic assertion')]),encoding='utf-8')
        self.summary=self.root/'summary.json'
        self.summary.write_text(json.dumps(dict(suite='mutation',status='passed',failure=None,before=self.state,after=self.state,completed=[dict(command=['go','run','./tools/breakcheck','-out',str(self.report)],exit_code=0)])),encoding='utf-8')
        self.python=self.root/'python.log';self.python.write_bytes(b'Ran 1 tests\n\nOK\n')
        self.certificate=self.root/'certificate.json'
        self.cert=dict(format=1,source_commit=self.source,source_tree=self.tree,
            registry_sha256=sha(self.registry_raw),report_sha256=sha(self.report.read_bytes()),summary_sha256=sha(self.summary.read_bytes()),
            summary=self.artifact(self.summary),report=self.artifact(self.report),python_tests=self.artifact(self.python),suites={},
            ci=dict(sha=self.source,conclusion='success',jobs=[dict(name='offline ('+osname+')',status='completed',conclusion='success') for osname in ['windows-2022','ubuntu-24.04']]))
        for suite,count in [('regression',3),('race',3),('fuzz',5)]:
            self.cert['suites'][suite]=dict(summary=dict(status='passed',failure=None,before=self.state,after=self.state,completed=[dict(exit_code=0,passed_tests=1,skipped=[],command=['go','test','-fuzztime=20s']) for _ in range(count)]))
        self.exe=self.root/'fixed.exe';self.exe.write_bytes(b'synthetic-only-not-executable')
        self.plan=self.root/'plan.md';self.plan.write_bytes(b'fixed plan')
        self.dll=self.root/'win';self.dll.mkdir()
        dlls=[]
        for name in ['ctp_trade.dll','thosttraderapi_se.dll']:
            path=self.dll/name;path.write_bytes(b'synthetic dll');dlls.append(self.artifact(path))
        self.cfg=dict(root=str(self.root),source=self.source,main=self.main,exe=str(self.exe),go='SYNTHETIC-GO',binary_sha256=sha(self.exe.read_bytes()),
            ready=str(self.root/'ready.json'),receipt=str(self.root/'receipt.json'),certificate=str(self.certificate),plan=str(self.plan),plan_sha256=sha(self.plan.read_bytes()),dlls=dlls,
            ready_deadline_bjt='2026-11-02T08:55:00+08:00',proof_root=str(self.proof),mutation_total=1,minimum_evidence_files=3,python_tests=1)
        self.cfg['argv']=[str(self.exe),'ctp-params','-timeout','10s','-env',str(self.root/'.env')]
        self.ready=dict(source_commit=self.source,published_main=self.main,argv=self.cfg['argv'],verified_at_bjt='2026-10-30T20:00:00+08:00',
            binary=self.artifact(self.exe),plan=self.artifact(self.plan),dlls=dlls)
        self.recorded='2026-10-30T20:00:01+08:00'
        self.refresh_certificate()
        self.calls=[];self.overrides={};self.serial=0

    def artifact(self,path):return dict(path=str(path),sha256=sha(path.read_bytes()))

    def refresh_ready(self):
        ready=Path(self.cfg['ready']);ready.write_text(json.dumps(self.ready),encoding='utf-8')
        self.cfg['ready_sha256']=sha(ready.read_bytes())
        receipt=Path(self.cfg['receipt']);receipt.write_text(json.dumps(dict(ready_sha256=self.cfg['ready_sha256'],verified_at_bjt=self.ready['verified_at_bjt'],recorded_at_bjt=self.recorded)),encoding='utf-8')
        self.cfg['receipt_sha256']=sha(receipt.read_bytes())

    def refresh_certificate(self):
        self.certificate.write_text(json.dumps(self.cert),encoding='utf-8')
        self.cfg['certificate_sha256']=sha(self.certificate.read_bytes())
        self.ready['certificate']=self.artifact(self.certificate)
        self.refresh_ready()

    def fake_command(self,ctx,argv,cwd,timeout=10,output_limit=1048576,*,text=True):
        key=tuple(argv)
        if key in self.overrides:return self.overrides[key]
        if argv[0]=='SYNTHETIC-GO':return 'vcs.revision='+self.source+'\nvcs.modified=false\nGOEXPERIMENT=nojsonv2'
        args=argv[3:]
        if args[:1]==['status']:return ''
        if args[:1]==['rev-parse']:
            if args[-1].endswith('^{tree}'):return self.tree
            return self.source if Path(cwd)==self.proof else self.main
        if args[:1]==['merge-base']:return ''
        if args[:1]==['diff']:return 'docs/synthetic.md'
        if args[:1]==['ls-remote']:return '\n'.join(self.main+'\trefs/heads/'+b for b in ['main','dev'])
        if args[:1]==['show']:
            self.assertFalse(text,'清单原始字节未请求二进制模式')
            return self.registry_raw
        if args[:1]==['ls-files']:return self.index if '--stage' in args else b'input.txt\0'
        self.fail('unexpected synthetic command')

    def run_checks(self,dll=None):
        self.serial+=1
        with patch.object(Attempt,'command',lambda ctx,*a,**kw:self.fake_command(ctx,*a,**kw)),patch.dict(os.environ,{'CTP_DLL_DIR':str(dll or self.dll)}):
            return run_once(self.root/f'round-{self.serial}',{},Checks(self.cfg).steps(),lambda:True,lambda ctx:self.calls.append('action'))

    def assert_refused(self,stage):
        result=self.run_checks()
        self.assertEqual((result['status'],result['stage']),('FAILED',stage),'准入约束失效:'+stage)
        self.assertFalse(self.calls,'准入失败仍执行')

    def test_all_production_checks_are_wired(self):
        result=self.run_checks()
        self.assertEqual(result['status'],'COMPLETED',result)
        self.assertEqual(self.calls,['action'])
        stages=[json.loads(p.read_bytes())['stage'] for p in sorted((self.root/'round-1').glob('*-passed.json'))]
        self.assertEqual(stages,['ready','receipt','source','local-git','remote-git','binary','certificate','plan','dll','evidence','execution'],'生产准入阶段缺失')

    def test_ready_hash(self):
        Path(self.cfg['ready']).write_bytes(b'{}');self.assert_refused('ready')

    def test_late_receipt_and_timezone(self):
        for stamp in ['2026-11-02T08:55:00+08:00','2026-10-30T20:00:01+00:00','2026-10-30T19:59:59+08:00']:
            self.recorded=stamp;self.refresh_ready();self.assert_refused('receipt')

    def test_source_and_fixed_argv(self):
        for args in [dict(source_commit='d'*40),dict(argv=[str(self.exe),'ctp-resting-price','-execute'])]:
            old=dict(self.ready);self.ready.update(args);self.refresh_ready();self.assert_refused('source');self.ready=old

    def test_dirty_source_ref_and_runtime_drift(self):
        prefix=['git','-c','http.version=HTTP/1.1']
        for args,value in [(['status','--porcelain'],' M source.go'),(['rev-parse','dev'],'d'*40),(['diff','--name-only',self.source,self.main],'scripts/runtime.py')]:
            self.overrides={tuple(prefix+args):value};self.assert_refused('local-git')

    def test_remote_unknown_duplicate_and_changed(self):
        key=tuple(['git','-c','http.version=HTTP/1.1','ls-remote','origin','refs/heads/main','refs/heads/dev'])
        for value in ['',self.main+'\trefs/heads/main\n'+self.main+'\trefs/heads/main','d'*40+'\trefs/heads/main\n'+self.main+'\trefs/heads/dev']:
            self.overrides={key:value};self.assert_refused('remote-git')

    def test_binary_hash_and_build(self):
        self.exe.write_bytes(b'changed');self.assert_refused('binary')
        self.exe.write_bytes(b'synthetic-only-not-executable')
        self.overrides={tuple(['SYNTHETIC-GO','version','-m',str(self.exe)]):'vcs.revision='+self.source+'\nvcs.modified=true\nGOEXPERIMENT=nojsonv2'}
        self.assert_refused('binary')

    def test_certificate_hash_and_tree(self):
        original=self.certificate.read_bytes();self.certificate.write_bytes(b'{}');self.assert_refused('certificate');self.certificate.write_bytes(original)
        self.overrides={tuple(['git','-c','http.version=HTTP/1.1','rev-parse',self.source+'^{tree}']):'d'*40};self.assert_refused('certificate')

    def test_plan_hash(self):
        self.plan.write_bytes(b'changed');self.assert_refused('plan')

    def test_dll_actual_path_not_just_equal_bytes(self):
        other=self.root/'other';other.mkdir()
        for item in self.cfg['dlls']:(other/Path(item['path']).name).write_bytes(b'synthetic dll')
        result=self.run_checks(other)
        self.assertEqual(result['stage'],'dll','DLL路径漂移未拒绝')
        self.assertFalse(self.calls)

    def test_actual_proof_source_is_rehashed(self):
        (self.proof/'input.txt').write_bytes(b'changed');self.assert_refused('evidence')

    def test_incomplete_mutation_and_wrong_assertion(self):
        observed=json.loads(self.report.read_bytes());observed[0]['detail']='other assertion'
        self.report.write_text(json.dumps(observed),encoding='utf-8')
        self.cert['report_sha256']=sha(self.report.read_bytes());self.cert['report']=self.artifact(self.report)
        self.refresh_certificate();self.assert_refused('evidence')

    def test_skips_and_fuzz_duration_are_not_passes(self):
        for suite in ['regression','race','fuzz']:
            original=json.loads(json.dumps(self.cert['suites'][suite]))
            item=self.cert['suites'][suite]['summary']['completed'][0]
            if suite=='fuzz':item['command']=['go','test','-fuzztime=1s']
            else:item['skipped']=['TestHidden']
            self.refresh_certificate();self.assert_refused('evidence');self.cert['suites'][suite]=original

    def test_python_and_ci_claims_are_checked(self):
        self.cert['ci']['jobs'][0]['conclusion']='failure';self.refresh_certificate();self.assert_refused('evidence')
        self.cert['ci']['jobs'][0]['conclusion']='success'
        self.python.write_bytes(b'Ran 0 tests\nOK\n');self.cert['python_tests']=self.artifact(self.python);self.refresh_certificate();self.assert_refused('evidence')


if __name__=='__main__':unittest.main()
