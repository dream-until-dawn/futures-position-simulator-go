"""只读操作的目标校验与Windows共享读锁；不构造柜台连接。"""
from contextlib import contextmanager
from pathlib import Path
import ctypes
from ctypes import wintypes
import hashlib
import os
from .errors import Rejected

BROKER='9999'
FRONT='tcp://182.254.243.31:30001'
GO_SPACE='\t\n\v\f\r \x85\xa0\u1680'+''.join(chr(n) for n in range(0x2000,0x200b))+'\u2028\u2029\u202f\u205f\u3000'

class GateError(Rejected):
    """只允许固定原因码，不能把凭据或操作系统自由文本带出。"""

def parse_target(raw):
    if len(raw)>1048576:raise GateError('env-too-large')
    try:text=raw.decode('utf-8')
    except UnicodeDecodeError:raise GateError('env-encoding') from None
    fields={}
    # 与Go Scanner/TrimPrefix(BOM)/TrimSpace/SplitN/Trim引号的顺序一致。
    for line in text.split('\n'):
        if len(line.encode('utf-8'))>=65536:raise GateError('env-line-too-large')
        line=line.removeprefix('\ufeff').strip(GO_SPACE)
        if not line or line.startswith('#') or '=' not in line:continue
        key,value=line.split('=',1)
        fields[key.strip(GO_SPACE)]=value.strip(GO_SPACE).strip('\"\'')
    if fields.get('CTP_BROKER_ID')!=BROKER or fields.get('CTP_TD_FRONT')!=FRONT:
        raise GateError('target-mismatch')
    for key in ['CTP_USER_ID','CTP_PASSWORD','CTP_APP_ID','CTP_AUTH_CODE','KQ_USER','KQ_PASSWORD']:
        if not fields.get(key):raise GateError('required-field-missing')
    return hashlib.sha256(raw).hexdigest()

@contextmanager
def locked_env(path):
    """FileShare.Read允许子程序读，阻止常规改写/删除；不能取锁就不执行。"""
    if os.name!='nt':raise GateError('platform-mismatch')
    import msvcrt
    kernel=ctypes.WinDLL('kernel32.dll',use_last_error=True)
    create=kernel.CreateFileW
    create.argtypes=[wintypes.LPCWSTR,wintypes.DWORD,wintypes.DWORD,ctypes.c_void_p,wintypes.DWORD,wintypes.DWORD,wintypes.HANDLE]
    create.restype=wintypes.HANDLE
    close=kernel.CloseHandle;close.argtypes=[wintypes.HANDLE];close.restype=wintypes.BOOL
    handle=create(str(Path(path).resolve()),0x80000000,1,None,3,0x80,None)
    if handle==ctypes.c_void_p(-1).value:
        raise GateError('env-lock-failed') from ctypes.WinError(ctypes.get_last_error())
    try:descriptor=msvcrt.open_osfhandle(int(handle),os.O_RDONLY|os.O_BINARY)
    except BaseException as error:
        close(handle);raise GateError('env-handle-failed') from error
    with os.fdopen(descriptor,'rb') as stream:
        raw=stream.read(1048577)
        digest=parse_target(raw)
        yield digest
        stream.seek(0)
        if hashlib.sha256(stream.read(1048577)).hexdigest()!=digest:raise GateError('env-changed')
