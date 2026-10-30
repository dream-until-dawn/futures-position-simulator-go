"""有界元数据命令及一次性准入编排，不实现交易和重连。"""
from contextlib import contextmanager
import os
import time
from validation_runtime import bounded_process
from .errors import STAGES, CommandFailure, Rejected, Stopped, classify
from .journal import Journal


class Attempt:
    def __init__(self, journal, allowed):
        self.journal = journal
        self.allowed = allowed
        self.current_stage = 'window'
        self.command_count = 0
        self.primary = None

    def check_window(self):
        if not self.allowed():
            raise Rejected('outside registered window')

    @contextmanager
    def stage(self, name):
        if name not in STAGES:
            raise ValueError('invalid stage')
        previous = self.current_stage
        self.current_stage = name
        started = time.monotonic_ns()
        try:
            self.check_window()
            self.journal.event('begin', dict(stage=name))
            yield
            self.check_window()
            self.journal.event('passed', dict(stage=name, elapsed_ns=time.monotonic_ns()-started))
        except Stopped:
            raise
        except Exception as error:
            report = dict(status='FAILED', stage=name, **classify(error),
                          elapsed_ns=time.monotonic_ns()-started)
            self.primary = report
            try:
                report['private_error'] = self.journal.cause(error)
                self.journal.event('failed', report)
            except Exception:
                report.update(status='UNKNOWN', persistence_failed=True)
            raise Stopped(report) from error
        finally:
            self.current_stage = previous

    def require(self, condition, private_reason):
        if not condition:
            raise Rejected(private_reason)

    def command(self, argv, cwd, timeout=10, output_limit=1048576, *, text=True):
        """保存合并原始字节后才严格解码；不把非零/超时补试成成功。"""
        if not 0 < timeout <= 40 or output_limit <= 0:
            raise ValueError('invalid bounded command budget')
        self.check_window()
        self.command_count += 1
        index = self.command_count
        env = dict(os.environ, GIT_TERMINAL_PROMPT='0', GCM_INTERACTIVE='Never')
        raw = bytearray()
        code = None
        failure = None
        try:
            with bounded_process(argv, cwd, timeout, env=env) as process:
                self.journal.event('process', dict(stage=self.current_stage, index=index, pid=process.pid))
                stream = getattr(process.stdout, 'buffer', process.stdout)
                while True:
                    block = stream.read(min(65536, output_limit+1-len(raw)))
                    if not block:
                        break
                    raw.extend(block)
                    if len(raw) > output_limit:
                        raise CommandFailure('output-limit')
                code = process.wait()
        except Exception as error:
            # bounded_process只在看门狗触发时产生这个固定超时前缀。
            failure = CommandFailure('timeout') if isinstance(error, RuntimeError) and str(error).startswith('命令超时（') else error
            if failure is not error:
                failure.__cause__ = error
        output = self.journal.raw(f'command-{index:03d}-private-combined.bin', bytes(raw))
        self.journal.event('command', dict(stage=self.current_stage, index=index,
            observed_exit_code=code, exit_code_origin='supervised-process', output=output,
            truncated=len(raw)>output_limit))
        if failure is not None:
            raise failure
        if code != 0:
            raise CommandFailure('exit', code)
        return bytes(raw).decode('utf-8-sig', errors='strict').strip() if text else bytes(raw)


def run_once(directory, identity, steps, allowed, action=None):
    """登记目录一旦存在就拒绝；失败阶段之后不调用action。依赖注入只供内部固定接线及测试。"""
    result = dict(status='REFUSED', stage='window', action_attempted=False)
    if not allowed():
        return dict(result, kind='outside-window')
    try:
        journal = Journal(directory, identity)
    except FileExistsError:
        return dict(result, stage='journal', kind='existing-attempt')
    except Exception as error:
        return dict(result, status='UNKNOWN', stage='journal', **classify(error), persistence_failed=True)
    attempt = Attempt(journal, allowed)
    try:
        for name, check in steps:
            with attempt.stage(name):
                check(attempt)
        with attempt.stage('execution' if action is not None else 'final'):
            if action is not None:
                attempt.check_window()
                result['action_attempted'] = True
                action(attempt)
        result.update(status='COMPLETED' if action is not None else 'READY', stage='final', kind=None)
    except Stopped as stop:
        result.update(stop.report)
    except Exception as error:
        # 包括未知阶段或编排自身错误；同样保留异常，不继续action。
        result.update(status='FAILED', stage=attempt.current_stage, **classify(error))
        try:
            result['private_error'] = journal.cause(error)
        except Exception:
            result.update(status='UNKNOWN', persistence_failed=True)
    try:
        journal.event('outcome', result)
    except Exception as error:
        result.update(status='UNKNOWN', persistence_failed=True, persistence_error=classify(error))
    return result
