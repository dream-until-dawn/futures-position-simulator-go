"""公开有限错误分类；原始异常仅供私有证据使用。"""
import subprocess

STAGES = frozenset({'journal', 'process', 'window', 'ready', 'receipt', 'source', 'local-git',
                    'remote-git', 'binary', 'certificate', 'plan', 'dll',
                    'evidence', 'env', 'execution', 'diagnosis', 'final'})


class Rejected(Exception):
    """明确的准入条件不成立，原因文本仍只能进入私有证据。"""


class CommandFailure(Exception):
    def __init__(self, kind, code=None):
        if kind not in {'exit', 'timeout', 'output-limit'}:
            raise ValueError('invalid command classification')
        super().__init__(kind)
        self.kind = kind
        self.code = code


class Stopped(Exception):
    """已记录的第一处失败，跨越外层阶段时不得被覆盖。"""
    def __init__(self, report):
        self.report = report
        super().__init__('admission-stopped')


def classify(error):
    """只生成固定分类与数值，不公开异常消息、类名、路径或命令。"""
    code = None
    if isinstance(error, CommandFailure):
        kind, code = error.kind, error.code
    elif isinstance(error, subprocess.CalledProcessError):
        kind, code = 'exit', error.returncode
    elif isinstance(error, subprocess.TimeoutExpired):
        kind = 'timeout'
    elif isinstance(error, UnicodeError):
        kind = 'decode'
    elif isinstance(error, Rejected):
        kind = 'check'
    elif isinstance(error, FileNotFoundError):
        kind = 'missing'
    elif isinstance(error, PermissionError):
        kind = 'permission'
    elif isinstance(error, OSError):
        kind = 'io'
    else:
        kind = 'unexpected'
    return dict(kind=kind, exit_code=code if type(code) is int else None,
                errno=getattr(error, 'errno', None) if type(getattr(error, 'errno', None)) is int else None,
                winerror=getattr(error, 'winerror', None) if type(getattr(error, 'winerror', None)) is int else None)
