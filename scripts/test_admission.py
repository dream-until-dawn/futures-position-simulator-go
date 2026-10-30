"""真实本地子进程与合成故障验证；不读取真实凭据、不联网柜台。"""
import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import patch
from readonly_admission.errors import Rejected
from readonly_admission.journal import Journal
from readonly_admission.runner import run_once


class AdmissionTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.directory = self.root / 'round'
        self.calls = []

    def run_attempt(self, steps=(), allowed=lambda: True, action=None):
        if action is None:
            action = lambda ctx: self.calls.append('action')
        return run_once(self.directory, {'source': 'a'*40}, steps, allowed, action)

    def command_step(self, code, **options):
        def step(ctx):
            return ctx.command([sys.executable, '-c', code], self.root, **options)
        return step

    def test_success_is_durable_and_exactly_once(self):
        result = self.run_attempt([('ready', lambda c: c.require(True, 'unused'))])
        self.assertEqual(result['status'], 'COMPLETED')
        self.assertEqual(self.calls, ['action'])
        events = sorted(self.directory.glob('*-outcome.json'))
        self.assertEqual(json.loads(events[0].read_bytes()), result)
        again = self.run_attempt()
        self.assertEqual(again['kind'], 'existing-attempt', '重复准入未被拒绝')
        self.assertEqual(self.calls, ['action'], '重复准入触发执行')

    def test_unknown_existing_empty_directory_is_not_reused(self):
        self.directory.mkdir()
        result = self.run_attempt()
        self.assertEqual(result['kind'], 'existing-attempt', '未知旧目录被重用')
        self.assertEqual(list(self.directory.iterdir()), [])
        self.assertFalse(self.calls)

    def test_first_record_precedes_every_check(self):
        def inspect(ctx):
            self.assertTrue((self.directory/'001-start.json').exists(), '检查前未记录独占启动')
        self.assertEqual(self.run_attempt([('ready', inspect)])['status'], 'COMPLETED', '检查前未记录独占启动')

    def test_two_distinct_failures_keep_stage_and_original_cause(self):
        for phase in ['remote-git', 'binary']:
            with self.subTest(phase=phase):
                directory = self.root / phase
                def fail(ctx):
                    raise PermissionError(13, 'SYNTHETIC_PRIVATE_SECRET\n\x1b[31m')
                result = run_once(directory, {}, [(phase, fail)], lambda: True, lambda c: self.calls.append('bad'))
                self.assertEqual(result['stage'], phase, '准入异常丢失失败阶段')
                self.assertEqual(result['kind'], 'permission', '准入异常丢失底层分类')
                self.assertEqual(result['errno'], 13, '准入异常丢失系统错误码')
                raw = (directory/result['private_error']['file']).read_bytes()
                self.assertIn(b'SYNTHETIC_PRIVATE_SECRET', raw, '原始异常链未留存')
                self.assertEqual(hashlib.sha256(raw).hexdigest(), result['private_error']['sha256'])
                self.assertNotIn('SYNTHETIC_PRIVATE_SECRET', json.dumps(result), '公开结果泄漏异常消息')
        self.assertFalse(self.calls, '失败准入仍调用执行器')

    def test_native_nonzero_keeps_output_and_no_retry(self):
        code = "import sys;sys.stdout.buffer.write(b'OUT');sys.stdout.flush();sys.stderr.buffer.write(b'ERR_PRIVATE');sys.exit(7)"
        result = self.run_attempt([('remote-git', self.command_step(code))])
        self.assertEqual((result['stage'], result['kind'], result['exit_code']), ('remote-git', 'exit', 7), '非零返回码被洗掉')
        raw = (self.directory/'command-001-private-combined.bin').read_bytes()
        self.assertEqual(raw, b'OUTERR_PRIVATE', '原始合并输出丢失或被改写')
        self.assertEqual(len(list(self.directory.glob('command-*-private-combined.bin'))), 1, '失败命令被自动重试')
        self.assertFalse(self.calls, '非零命令后仍执行')

    def test_command_success_and_environment_are_bounded(self):
        code = "import os,sys;print(os.environ['GIT_TERMINAL_PROMPT']+'|'+os.environ['GCM_INTERACTIVE']);sys.stderr.write('中文')"
        value = []
        def step(ctx):
            value.append(self.command_step(code)(ctx))
        result = self.run_attempt([('remote-git', step)])
        self.assertEqual(result['status'], 'COMPLETED')
        self.assertIn('0|Never', value[0], '元数据子进程仍可交互')
        self.assertIn('中文', value[0])

    def test_invalid_utf8_is_preserved_then_rejected(self):
        result = self.run_attempt([('binary', self.command_step("import sys;sys.stdout.buffer.write(b'\\xff')"))])
        self.assertEqual(result['kind'], 'decode', '非法编码被替换成成功文本')
        self.assertEqual((self.directory/'command-001-private-combined.bin').read_bytes(), b'\xff', '非法编码原始字节丢失')
        self.assertFalse(self.calls)

    def test_binary_mode_never_strips_registry_bytes(self):
        values = []
        def step(ctx):
            values.append(self.command_step("import sys;sys.stdout.buffer.write(b' \\n\\xff \\n')", text=False)(ctx))
        self.assertEqual(self.run_attempt([('evidence', step)])['status'], 'COMPLETED')
        self.assertEqual(values, [b' \n\xff \n'], '源码原始字节被解码或裁剪')

    def test_spawn_failure_is_preserved_and_not_retried(self):
        with patch('readonly_admission.runner.bounded_process', side_effect=OSError(5, 'synthetic-spawn-secret')) as start:
            result = self.run_attempt([('local-git', self.command_step('pass'))])
        self.assertEqual(start.call_count, 1, '创建失败被重试')
        self.assertEqual(result['stage'], 'local-git')
        self.assertEqual(result['errno'], 5, '创建进程错误码未留存')
        self.assertIn(b'synthetic-spawn-secret', (self.directory/result['private_error']['file']).read_bytes())
        self.assertFalse(self.calls)

    def test_timeout_reaps_descendant_and_keeps_original_chain(self):
        marker = self.root/'escaped'
        child = 'import time,pathlib;time.sleep(2);pathlib.Path('+repr(str(marker))+').write_text("bad")'
        parent = 'import subprocess,sys,time;print("before-timeout",flush=True);subprocess.Popen([sys.executable,"-c",'+repr(child)+']);time.sleep(30)'
        started = time.monotonic()
        result = self.run_attempt([('remote-git', self.command_step(parent, timeout=.5))])
        self.assertLess(time.monotonic()-started, 8)
        self.assertEqual(result['kind'], 'timeout', '超时丢失分类')
        self.assertIn(b'before-timeout', (self.directory/'command-001-private-combined.bin').read_bytes(), '超时前输出丢失')
        self.assertIn('命令超时', (self.directory/result['private_error']['file']).read_text(), '超时原异常链丢失')
        time.sleep(2)
        self.assertFalse(marker.exists(), '超时子孙进程逃逸')
        self.assertFalse(self.calls)

    def test_output_limit_stops_command_and_action(self):
        result = self.run_attempt([('local-git', self.command_step("print('x'*1000)", output_limit=32))])
        self.assertEqual(result['kind'], 'output-limit', '过量输出未停止')
        self.assertEqual(len((self.directory/'command-001-private-combined.bin').read_bytes()), 33)
        self.assertFalse(self.calls)

    def test_outside_window_never_creates_or_checks(self):
        result = self.run_attempt([('ready', lambda c: self.fail('outside check ran'))], allowed=lambda: False)
        self.assertFalse(self.directory.exists(), '越窗仍建立准入轮')
        self.assertEqual(result['kind'], 'outside-window')
        self.assertFalse(self.calls)

    def test_window_crossing_after_check_never_executes(self):
        allowed = [True]
        result = self.run_attempt([('evidence', lambda c: allowed.__setitem__(0, False))], allowed=lambda: allowed[0])
        self.assertEqual(result['status'], 'FAILED', '准入越窗未停止')
        self.assertFalse(self.calls, '准入越窗仍执行')

    def test_window_crossing_at_action_boundary_never_executes(self):
        readings = iter([True, True, True, False])
        result = self.run_attempt([('ready', lambda c: None)], allowed=lambda: next(readings, False))
        self.assertEqual(result['stage'], 'execution')
        self.assertFalse(self.calls, '执行前越窗仍执行')

    def test_failure_write_does_not_replace_primary_error(self):
        original = Journal.event
        def event(journal, kind, value):
            if kind == 'failed':raise OSError('synthetic-disk-error')
            return original(journal, kind, value)
        def fail(ctx):raise subprocess.CalledProcessError(17, ['synthetic-secret'])
        with patch.object(Journal, 'event', event):
            result = self.run_attempt([('remote-git', fail)])
        self.assertEqual((result['status'], result['stage'], result['exit_code']), ('UNKNOWN', 'remote-git', 17), '落盘错误覆盖原失败')
        self.assertTrue(result['persistence_failed'])
        self.assertFalse(self.calls)

    def test_private_error_persistence_failure_never_executes(self):
        with patch.object(Journal, 'cause', side_effect=PermissionError(13, 'disk')):
            result = self.run_attempt([('binary', lambda c: c.require(False, 'synthetic-secret'))])
        self.assertEqual(result['status'], 'UNKNOWN', '异常留证失败仍声称完整失败证据')
        self.assertEqual(result['stage'], 'binary')
        self.assertFalse(self.calls)

    def test_start_record_failure_prevents_checks(self):
        with patch.object(Journal, 'raw', side_effect=OSError(5, 'disk')):
            result = self.run_attempt([('ready', lambda c: self.calls.append('bad'))])
        self.assertEqual((result['status'], result['stage']), ('UNKNOWN', 'journal'), '启动记录失败未阻止准入')
        self.assertFalse(self.calls)

    def test_final_record_failure_cannot_claim_success(self):
        original = Journal.event
        def event(journal, kind, value):
            if kind == 'outcome':raise OSError('disk')
            return original(journal, kind, value)
        with patch.object(Journal, 'event', event):
            result = run_once(self.directory, {}, [], lambda: True)
        self.assertEqual(result['status'], 'UNKNOWN', '终态落盘失败仍声称成功')
        self.assertTrue(result['persistence_failed'])

    def test_nested_failure_retains_inner_stage(self):
        def action(ctx):
            with ctx.stage('env'):
                raise Rejected('synthetic-env-refused')
        result = self.run_attempt(action=action)
        self.assertEqual(result['stage'], 'env', '外层阶段覆盖内层失败')

    def test_exclusive_json_never_overwrites_evidence(self):
        journal = Journal(self.directory, {})
        first = (self.directory/'001-start.json').read_bytes()
        with self.assertRaises(FileExistsError, msg='原始证据被覆盖'):
            journal.raw('001-start.json', b'bad')
        self.assertEqual((self.directory/'001-start.json').read_bytes(), first)

    def test_chained_causes_are_preserved(self):
        def fail(ctx):
            try:raise OSError(5,'SYNTHETIC_INNER_CAUSE')
            except OSError as error:raise Rejected('SYNTHETIC_OUTER_FAILURE') from error
        result=self.run_attempt([('binary',fail)])
        raw=(self.directory/result['private_error']['file']).read_bytes()
        self.assertIn(b'SYNTHETIC_INNER_CAUSE',raw,'底层异常链被截断')
        self.assertIn(b'SYNTHETIC_OUTER_FAILURE',raw)
        self.assertNotIn('SYNTHETIC_',json.dumps(result))

    def test_fsync_failure_prevents_every_check(self):
        with patch('readonly_admission.journal.os.fsync',side_effect=OSError(5,'disk')):
            result=self.run_attempt([('ready',lambda c:self.calls.append('bad'))])
        self.assertEqual(result['status'],'UNKNOWN','刷盘失败仍执行')
        self.assertFalse(self.calls,'刷盘失败后继续准入')

    def test_readback_mismatch_prevents_every_check(self):
        original=Path.read_bytes
        def changed(path):
            return b'corrupted' if path.name=='001-start.json' else original(path)
        with patch.object(Path,'read_bytes',changed):
            result=self.run_attempt([('ready',lambda c:self.calls.append('bad'))])
        self.assertEqual(result['status'],'UNKNOWN','读取回核不一致仍推进')
        self.assertFalse(self.calls)

    def test_bad_stage_never_executes(self):
        result = self.run_attempt([('SECRET_INVALID_STAGE', lambda c: self.calls.append('bad'))])
        self.assertEqual(result['status'], 'FAILED')
        self.assertNotIn('SECRET_INVALID_STAGE', json.dumps(result), '非法阶段泄漏')
        self.assertFalse(self.calls)


if __name__ == '__main__':unittest.main()
