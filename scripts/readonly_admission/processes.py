"""单次进程快照、查询树核验和私有身份留证；不终止进程、不连接柜台。"""
import ctypes
from datetime import datetime, timezone
import hashlib
import json
import ntpath
import os
from pathlib import Path
import sys
from .errors import Rejected


# 不把入口路径拼进查询文本，避免查询worker因携带该文本而自匹配。
SNAPSHOT_SCRIPT = (
    "[Console]::OutputEncoding=[System.Text.UTF8Encoding]::new($false);"
    "$ErrorActionPreference='Stop';$rows=@(Get-CimInstance Win32_Process | Where-Object {"
    "$_.Name -like 'python*' -or $_.Name -in @('py.exe','pyw.exe') -or "
    "$_.Name -like '*ctp-readonly*' -or $_.ProcessId -eq $PID} | ForEach-Object {"
    "@{pid=[long]$_.ProcessId;parent_pid=[long]$_.ParentProcessId;name=$_.Name;"
    "executable=$_.ExecutablePath;command_line=$_.CommandLine;"
    "created_at=$(if($null -eq $_.CreationDate){$null}else{$_.CreationDate.ToUniversalTime().ToString('o')})}});"
    "@{format=1;query_succeeded=$true;collector_pid=$PID;"
    "captured_at=[DateTime]::UtcNow.ToString('o');rows=$rows}|ConvertTo-Json -Depth 5 -Compress"
)
QUERY_ARGV = ['powershell', '-NoProfile', '-NonInteractive', '-Command', SNAPSHOT_SCRIPT]


def require(condition, reason):
    if not condition:
        raise Rejected(reason)


def windows_argv(command):
    """按Windows原生规则解码，不用POSIX切分；空命令禁止触发API的默认值。"""
    require(isinstance(command, str) and 0 < len(command) <= 32768 and '\0' not in command,
            'missing or oversized command line')
    require(os.name == 'nt', 'Windows command parser required')
    shell = ctypes.WinDLL('shell32', use_last_error=True)
    kernel = ctypes.WinDLL('kernel32', use_last_error=True)
    parse = shell.CommandLineToArgvW
    parse.argtypes = [ctypes.c_wchar_p, ctypes.POINTER(ctypes.c_int)]
    parse.restype = ctypes.POINTER(ctypes.c_wchar_p)
    release = kernel.LocalFree
    release.argtypes = [ctypes.c_void_p]
    release.restype = ctypes.c_void_p
    count = ctypes.c_int()
    pointer = parse(command, ctypes.byref(count))
    if not pointer:
        raise ctypes.WinError(ctypes.get_last_error())
    try:
        require(0 < count.value <= 4096, 'invalid argument count')
        return [pointer[i] for i in range(count.value)]
    finally:
        if release(ctypes.cast(pointer, ctypes.c_void_p)):
            raise ctypes.WinError(ctypes.get_last_error())


def path_key(value):
    require(isinstance(value, str) and value and '\0' not in value and ntpath.isabs(value),
            'unknown executable path')
    return ntpath.normcase(ntpath.normpath(value))


def current_image():
    """Python安装管理器可用shim作主映像；不能把sys.executable当当前OS映像。"""
    require(os.name == 'nt', 'Windows image query required')
    kernel = ctypes.WinDLL('kernel32', use_last_error=True)
    get_name = kernel.GetModuleFileNameW
    get_name.argtypes = [ctypes.c_void_p, ctypes.c_wchar_p, ctypes.c_uint32]
    get_name.restype = ctypes.c_uint32
    buffer = ctypes.create_unicode_buffer(32768)
    length = get_name(None, buffer, len(buffer))
    if not length or length >= len(buffer):
        raise ctypes.WinError(ctypes.get_last_error())
    return buffer.value


def timestamp(value):
    require(isinstance(value, str), 'unknown creation time')
    at = datetime.fromisoformat(value.replace('Z', '+00:00'))
    require(at.utcoffset() is not None, 'unknown creation timezone')
    return at


def python_name(name):
    return name.startswith('python') or name in {'py.exe', 'pyw.exe'}


def decode_snapshot(raw):
    """未知JSON重复键不得被后值覆盖成成功；原始字节已由runner私下保存。"""
    def unique(pairs):
        result = {}
        for key, value in pairs:
            require(key not in result, 'duplicate snapshot JSON key')
            result[key] = value
        return result
    return json.loads(raw, object_pairs_hook=unique)


def assess(snapshot, *, current_pid, worker_pid, executable, worker_path,
           operator_path, exe_path, powershell_path, current_executable=None, split=windows_argv):
    """只排除能与本次实际启动PID及固定参数对应的三节点查询树。"""
    require(isinstance(snapshot, dict) and set(snapshot) ==
            {'format', 'query_succeeded', 'collector_pid', 'captured_at', 'rows'}, 'invalid snapshot schema')
    require(type(snapshot['format']) is int and snapshot['format'] == 1 and
            snapshot['query_succeeded'] is True, 'process query not proven successful')
    captured = timestamp(snapshot['captured_at'])
    require(isinstance(snapshot['rows'], list) and 3 <= len(snapshot['rows']) <= 4096,
            'incomplete or oversized process snapshot')
    ids = [current_pid, worker_pid, snapshot['collector_pid']]
    require(all(type(pid) is int and 0 < pid < 2**32 for pid in ids) and len(set(ids)) == 3,
            'unknown or overlapping query identities')
    rows = {}
    for row in snapshot['rows']:
        require(isinstance(row, dict) and set(row) ==
                {'pid', 'parent_pid', 'name', 'executable', 'command_line', 'created_at'}, 'invalid process row')
        pid = row['pid']
        require(type(pid) is int and 0 < pid < 2**32 and pid not in rows, 'duplicate or invalid process pid')
        require(type(row['parent_pid']) is int and 0 <= row['parent_pid'] < 2**32, 'unknown parent pid')
        require(isinstance(row['name'], str) and row['name'] and
                ntpath.basename(row['name']) == row['name'], 'unknown process name')
        image = path_key(row['executable'])
        require(ntpath.basename(image) == row['name'].lower(), 'name and executable mismatch')
        created = timestamp(row['created_at'])
        require(created <= captured, 'process created after snapshot')
        require(isinstance(row['command_line'], str) and 0 < len(row['command_line']) <= 32768 and
                '\0' not in row['command_line'], 'unknown process command')
        argv = split(row['command_line'])
        require(isinstance(argv, list) and argv and all(isinstance(a, str) for a in argv), 'invalid parsed arguments')
        rows[pid] = dict(raw=row, image=image, created=created, argv=argv)
    require(all(pid in rows for pid in ids), 'query tree missing')
    own, worker, collector = [rows[pid] for pid in ids]
    require(own['image'] == path_key(current_executable or executable) and python_name(own['raw']['name'].lower()),
            'current process image changed')
    require(worker['raw']['parent_pid'] == current_pid and worker['image'] == path_key(executable) and
            worker['created'] >= own['created'], 'query worker identity mismatch')
    require(len(worker['argv']) == 2 + len(QUERY_ARGV) and
            path_key(worker['argv'][0]) == path_key(executable) and
            path_key(worker['argv'][1]) == path_key(worker_path) and
            worker['argv'][2:] == QUERY_ARGV, 'query worker arguments mismatch')
    require(collector['raw']['parent_pid'] == worker_pid and
            collector['image'] == path_key(powershell_path) and collector['created'] >= worker['created'],
            'collector identity mismatch')
    require(len(collector['argv']) == len(QUERY_ARGV) and
            ntpath.basename(collector['argv'][0]).lower() in {'powershell', 'powershell.exe'} and
            collector['argv'][1:] == QUERY_ARGV[1:], 'collector arguments mismatch')
    targets = {path_key(operator_path), path_key(exe_path)}
    report = []
    blockers = 0
    for pid, row in rows.items():
        name = row['raw']['name'].lower()
        if pid in ids:
            role = ['current', 'query-worker', 'collector'][ids.index(pid)]
        elif 'ctp-readonly' in name or row['image'] == path_key(exe_path):
            role = 'blocker'
        else:
            require(python_name(name), 'unexpected candidate process')
            # 其他入口（包括相对路径）与包含相关执行文本的-c/-m保持保守阻断。
            arguments = row['argv'][1:]
            related = any(ntpath.basename(a).lower() in
                          {ntpath.basename(operator_path).lower(), 'run_registered.py', 'run_readonly.py'}
                          or (ntpath.isabs(a) and ntpath.normcase(ntpath.normpath(a)) in targets)
                          for a in arguments)
            mention = any(path_key(operator_path) in a.replace('/', '\\').lower()
                          or 'ctp-readonly' in a.lower() for a in arguments)
            role = 'blocker' if related or mention else 'unrelated-python'
        blockers += role == 'blocker'
        report.append(dict(pid=pid, parent_pid=row['raw']['parent_pid'], created_at=row['raw']['created_at'],
                           name=name, role=role, executable_sha256=hashlib.sha256(row['image'].encode()).hexdigest(),
                           command_sha256=hashlib.sha256(row['raw']['command_line'].encode()).hexdigest()))
    return dict(matching_count=blockers, query_succeeded=True), report


def inspect(ctx, cfg):
    """原始快照与身份判断均先落盘，再返回有限计数；任何失败不进入.env。"""
    require(os.name == 'nt', 'registered target requires Windows')
    raw = ctx.command(QUERY_ARGV, cfg['root'])
    binding = hashlib.sha256(raw.encode('utf-8')).hexdigest()
    report = dict(status='UNKNOWN', snapshot_text_sha256=binding)
    try:
        state, identities = assess(decode_snapshot(raw), current_pid=os.getpid(),
            worker_pid=ctx.last_command_pid, executable=sys.executable, current_executable=current_image(),
            worker_path=str(Path(__file__).resolve().parents[1]/'process_worker.py'),
            operator_path=cfg['operator_path'], exe_path=cfg['exe'],
            powershell_path=str(Path(os.environ.get('SystemRoot', 'C:/Windows')) /
                                'System32/WindowsPowerShell/v1.0/powershell.exe'))
        report.update(status='ASSESSED', state=state, identities=identities)
    except Exception:
        ctx.journal.event('private-process-identities', report)
        raise
    ctx.journal.event('private-process-identities', report)
    return state
