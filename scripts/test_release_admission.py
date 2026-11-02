"""发布联网与执行离线分期的业务反例；只用合成仓库与本地假CLI。"""
from datetime import datetime
import hashlib
import json
from pathlib import Path
import unittest
from unittest.mock import patch
import test_admission_registered as fixtures
from readonly_admission import release
from readonly_admission.checks import Checks
from readonly_admission.errors import CommandFailure
from readonly_admission.runner import Attempt, run_once


class ReleaseAdmissionTests(unittest.TestCase):
    def setUp(self):
        self.f=fixtures.RegisteredTests(methodName='test_full_registered_path_and_unique_execution')
        self.f.setUp();self.addCleanup(self.f.doCleanups)
        self.cfg=self.f.cfg;self.path=Path(self.cfg['release'])
        self.record=json.loads(self.path.read_bytes())
        self.at=datetime.fromisoformat('2026-11-02T09:05:00+08:00')

    def write(self, record=None, raw=None):
        self.path.write_bytes(raw if raw is not None else json.dumps(record or self.record).encode())
        self.cfg['release_sha256']=hashlib.sha256(self.path.read_bytes()).hexdigest()

    def verify(self):
        with patch.object(release,'now',lambda:self.at):
            return run_once(self.f.f.root/'verify',{},[('release',lambda ctx:release.verify(ctx,self.cfg))],lambda:True,lambda ctx:self.f.env_reads.append('would-read'))

    def refused(self, message):
        result=self.verify()
        self.assertEqual((result['status'],result['stage'],result['action_attempted']),('FAILED','release',False),message)
        self.assertFalse(self.f.env_reads,message)

    def test_execution_path_never_contacts_remote(self):
        original=self.f.command
        network=[]
        def command(ctx,argv,cwd,**kw):
            if 'ls-remote' in argv:
                network.append(argv)
                raise CommandFailure('exit',128)
            return original(ctx,argv,cwd,**kw)
        self.f.command=command
        result=self.f.run_registered()
        self.assertEqual(result['status'],'COMPLETED','执行期Git不可达仍阻断已封存只读')
        self.assertEqual(network,[],'执行期仍联网Git')
        self.assertEqual(self.f.env_reads,['synthetic-only'])
        self.assertEqual(len(self.f.processes),1)

    def test_both_stage_sets_are_explicit(self):
        checks=Checks(self.cfg)
        names=[name for name,_ in checks.execution_steps()]
        published=[name for name,_ in checks.steps()]
        self.assertIn('remote-git',published,'发布阶段丢失真实远端核验')
        self.assertNotIn('remote-git',names,'执行阶段未隔离网络')
        self.assertEqual(names,['release' if n=='remote-git' else n for n in published],'执行阶段遗漏本地守卫')
        self.assertEqual(names.count('release'),1,'发布凭据未接线')

    def test_valid_receipt_records_bound_digest(self):
        result=self.verify()
        self.assertEqual(result['status'],'COMPLETED','有效短期发布凭据被拒绝')
        rows=list((self.f.f.root/'verify').glob('*-release-binding.json'))
        self.assertEqual(len(rows),1,'未记录本轮发布凭据绑定')
        self.assertEqual(json.loads(rows[0].read_bytes())['sha256'],self.cfg['release_sha256'])

    def test_missing_receipt_has_no_live_fallback(self):
        self.path.unlink();self.refused('缺失发布凭据仍继续')

    def test_changed_bytes_are_not_accepted(self):
        self.path.write_bytes(self.path.read_bytes()+b' ')
        self.refused('发布凭据哈希漂移未拒绝')

    def test_wrong_digest_format_is_rejected(self):
        self.cfg['release_sha256']='x'*64;self.refused('非法凭据摘要未拒绝')

    def test_source_binding(self):
        self.record['source']='c'*40;self.write();self.refused('发布源码绑定未拒绝')

    def test_main_binding(self):
        self.record['main']='d'*40;self.write();self.refused('发布主线绑定未拒绝')

    def test_plan_binding(self):
        self.record['plan_sha256']='e'*64;self.write();self.refused('发布计划绑定未拒绝')

    def test_origin_binding(self):
        self.record['origin']='https://unregistered.invalid/repo.git';self.write();self.refused('发布仓库绑定未拒绝')

    def test_refs_binding(self):
        self.record['refs']['dev']='f'*40;self.write();self.refused('发布refs绑定未拒绝')

    def test_scope_cannot_be_trade(self):
        self.record['scope']='trade';self.write();self.refused('发布用途扩大未拒绝')

    def test_window_binding(self):
        self.record['window_end_bjt']='2026-11-02T09:11:00+08:00';self.write();self.refused('发布窗口绑定未拒绝')

    def test_schema_unknown_field(self):
        self.record['fallback']=True;self.write();self.refused('发布凭据未知字段未拒绝')

    def test_schema_missing_field(self):
        del self.record['scope'];self.write();self.refused('发布凭据缺字段未拒绝')

    def test_boolean_version_is_not_integer(self):
        self.record['format']=True;self.write();self.refused('布尔凭据版本未拒绝')

    def test_duplicate_key_cannot_override_refusal(self):
        raw=json.dumps(self.record).replace('"format": 1','"format": 0, "format": 1').encode()
        self.write(raw=raw);self.refused('发布凭据重复键未拒绝')

    def test_expiry_boundary_is_exclusive(self):
        self.at=datetime.fromisoformat(self.record['window_end_bjt']);self.refused('过期发布凭据未拒绝')

    def test_future_receipt_is_rejected(self):
        self.at=datetime.fromisoformat('2026-11-02T08:59:59+08:00');self.refused('未来发布凭据未拒绝')

    def test_old_receipt_is_rejected(self):
        self.record['verified_at_bjt']='2026-11-02T08:39:59+08:00';self.write();self.refused('过旧发布凭据未拒绝')

    def test_capture_after_window_start_is_rejected(self):
        self.record['verified_at_bjt']='2026-11-02T09:05:01+08:00';self.write();self.refused('迟发发布凭据未拒绝')

    def test_unqualified_timezone_is_rejected(self):
        self.record['verified_at_bjt']='2026-11-02T01:00:00+00:00';self.write();self.refused('发布时区未限定')

    def test_window_cannot_expand(self):
        self.cfg['release_window_end_bjt']='2026-11-02T09:11:00+08:00'
        self.record['window_end_bjt']=self.cfg['release_window_end_bjt'];self.write();self.refused('发布窗口扩长未拒绝')

    def test_invalid_source_config_is_rejected(self):
        self.cfg['source']='not-sha';self.record['source']='not-sha';self.write();self.refused('发布源码格式未拒绝')

    def test_invalid_plan_config_is_rejected(self):
        self.cfg['plan_sha256']='bad';self.record['plan_sha256']='bad';self.write();self.refused('发布计划格式未拒绝')

    def test_release_binding_write_failure_blocks(self):
        from readonly_admission.journal import Journal
        original=Journal.event
        def event(journal,kind,value):
            if kind=='release-binding':raise PermissionError('synthetic evidence failure')
            return original(journal,kind,value)
        with patch.object(Journal,'event',event):self.refused('发布绑定留证失败仍继续')

    def capture(self, output=None, error=None, origin=release.ORIGIN):
        calls=[]
        expected='\n'.join(self.cfg['main']+'\trefs/heads/'+name for name in ['main','dev'])
        def command(ctx,argv,cwd,**kw):
            calls.append(argv)
            if 'get-url' in argv:return origin
            if error:raise error
            return expected if output is None else output
        with patch.object(Attempt,'command',command),patch.object(release,'now',lambda:datetime.fromisoformat('2026-11-02T09:00:00+08:00')):
            result=run_once(self.f.f.root/'capture',{},[('remote-git',lambda ctx:release.capture(ctx,self.cfg))],lambda:True)
        return result,calls

    def test_capture_live_refs_once_then_exclusive_persistence(self):
        result,calls=self.capture()
        self.assertEqual(result['status'],'READY','真实发布refs对照被拒绝')
        self.assertEqual(sum('ls-remote' in a for a in calls),1,'发布远端隐式重试')
        record=json.loads((self.f.f.root/'capture/release-snapshot.json').read_bytes())
        self.assertEqual(record['refs'],self.record['refs'],'发布凭据没有绑定真实refs')

    def test_capture_existing_snapshot_is_not_overwritten(self):
        from readonly_admission.journal import Journal
        from readonly_admission.runner import Attempt
        directory=self.f.f.root/'existing-release'
        journal=Journal(directory,{})
        journal.raw('release-snapshot.json',b'previous evidence')
        ctx=Attempt(journal,lambda:True)
        expected='\n'.join(self.cfg['main']+'\trefs/heads/'+name for name in ['main','dev'])
        with patch.object(Attempt,'command',lambda ctx,argv,*a,**kw:release.ORIGIN if 'get-url' in argv else expected),patch.object(release,'now',lambda:datetime.fromisoformat('2026-11-02T09:00:00+08:00')):
            with self.assertRaises(FileExistsError,msg='发布凭据被覆盖'):release.capture(ctx,self.cfg)
        self.assertEqual((directory/'release-snapshot.json').read_bytes(),b'previous evidence')

    def test_exact_thirty_minute_lifetime_is_valid(self):
        self.record['verified_at_bjt']='2026-11-02T08:40:00+08:00';self.write()
        self.assertEqual(self.verify()['status'],'COMPLETED','三十分钟边界有效凭据误拒绝')

    def test_capture_network_failure_keeps_raw_failure_no_receipt(self):
        result,calls=self.capture(error=CommandFailure('exit',128))
        self.assertEqual((result['status'],result['exit_code']),('FAILED',128),'发布联网失败被洗成成功')
        self.assertEqual(sum('ls-remote' in a for a in calls),1,'发布失败重复远端请求')
        self.assertFalse((self.f.f.root/'capture/release-snapshot.json').exists(),'发布失败仍生成凭据')

    def test_capture_changed_refs_are_not_sealed(self):
        result,_=self.capture(output='f'*40+'\trefs/heads/main')
        self.assertEqual(result['status'],'FAILED','远端漂移仍封存发布')
        self.assertFalse((self.f.f.root/'capture/release-snapshot.json').exists())

    def test_capture_unknown_origin_never_contacts_remote(self):
        result,calls=self.capture(origin='https://unregistered.invalid/repo.git')
        self.assertEqual(result['status'],'FAILED','非登记远端未拒绝')
        self.assertFalse(any('ls-remote' in a for a in calls),'非登记远端仍发请求')

    def test_capture_persistence_failure_is_failure(self):
        from readonly_admission.journal import Journal
        original=Journal.raw
        def raw(journal,name,data):
            if name=='release-snapshot.json':raise OSError('synthetic disk failure')
            return original(journal,name,data)
        with patch.object(Journal,'raw',raw):result,_=self.capture()
        self.assertEqual(result['status'],'FAILED','发布落盘失败仍成功')

    def test_missing_receipt_stops_actual_registered_before_env(self):
        self.path.unlink();result=self.f.run_registered()
        self.assertEqual((result['status'],result['stage']),('FAILED','release'),'生产接线绕过发布凭据')
        self.assertFalse(self.f.env_reads,'生产发布失败仍读凭据')
        self.assertFalse(self.f.processes,'生产发布失败仍运行CLI')


if __name__=='__main__':unittest.main()
