"""严格白名单解析：异常只返回固定原因码，不复制原始行或自由文本。"""
from dataclasses import dataclass
from decimal import Decimal, localcontext
from typing import Optional
import re

STAGES = {'setup', 'front', 'authenticate', 'login', 'settlement-confirm'}
EVENTS = {'begin', 'send-return', 'response', 'finished', 'timeout', 'closed', 'disconnected'} | {
    'ignored-'+reason for reason in ['stopped','inactive','duplicate','stage','request','late','partial','unknown']}
UNITS = {'h':3600000000000,'m':60000000000,'s':1000000000,'ms':1000000,'us':1000,'µs':1000,'μs':1000,'ns':1}


class EvidenceError(ValueError):
    """固定原因码，CLI不能把包含身份的输入或系统异常原文回显。"""


@dataclass(frozen=True)
class Event:
    stage: str
    request: int
    event: str
    code: int
    elapsed_ns: int
    received_stage: Optional[str] = None
    received_request: Optional[int] = None
    last: Optional[bool] = None
    has_payload: Optional[bool] = None


def duration_ns(value):
    """精确解析Go日志duration；不使用会丢失纳秒边界的float。"""
    if len(value) > 128:
        raise EvidenceError('invalid-duration')
    chunks = list(re.finditer(r'([0-9]+(?:\.[0-9]+)?)(ns|us|µs|μs|ms|s|m|h)', value))
    if not chunks or ''.join(chunk.group() for chunk in chunks) != value:
        raise EvidenceError('invalid-duration')
    with localcontext() as context:
        context.prec = 64
        total = sum(Decimal(chunk[1])*UNITS[chunk[2]] for chunk in chunks)
        if total != total.to_integral_value() or total > 9223372036854775807:
            raise EvidenceError('invalid-duration')
        return int(total)


def integer(value, minimum, maximum):
    """先约束十进制形状和范围，拒绝自由文本及过大整数。"""
    if not re.fullmatch(r'-?[0-9]{1,12}', value):
        raise EvidenceError('invalid-integer')
    result = int(value)
    if not minimum <= result <= maximum:
        raise EvidenceError('invalid-integer')
    return result


def boolean(value):
    """false必须保持False，不能用bool(非空字符串)误判。"""
    if value not in ['true','false']:
        raise EvidenceError('invalid-boolean')
    return value == 'true'


def parse_log(raw):
    """同时接受旧五字段和新九字段格式；缺失received元数据保持None，不能变成false。"""
    try:
        lines = raw.decode('utf-8-sig').splitlines()
    except UnicodeDecodeError:
        raise EvidenceError('invalid-log-encoding') from None
    events = []
    for line in lines:
        marker = '[ctp.handshake] '
        if marker not in line:
            continue
        text = line.split(marker,1)[1]
        fields = re.findall(r'([a-z_]+)=([^\s]*)', text)
        if ' '.join(key+'='+value for key,value in fields) != text or len(dict(fields)) != len(fields):
            raise EvidenceError('malformed-handshake-record')
        row = dict(fields)
        base = {'stage','request','event','code','elapsed'}
        extra = {'received_stage','received_request','last','has_payload'}
        if set(row) not in [base,base|extra] or row['stage'] not in STAGES or row['event'] not in EVENTS:
            raise EvidenceError('unknown-handshake-field')
        received = None
        if extra <= row.keys():
            if row['received_stage'] not in STAGES|{''}:
                raise EvidenceError('unknown-received-stage')
            received = dict(received_stage=row['received_stage'],
                received_request=integer(row['received_request'],0,2147483647),
                last=boolean(row['last']),has_payload=boolean(row['has_payload']))
        events.append(Event(stage=row['stage'],request=integer(row['request'],0,2147483647),
            event=row['event'],code=integer(row['code'],-2147483648,2147483647),
            elapsed_ns=duration_ns(row['elapsed']),**(received or {})))
    if not events:
        raise EvidenceError('no-handshake-evidence')
    return events
