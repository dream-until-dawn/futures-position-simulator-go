"""固定只读用途的短期发布凭据；发布联网，执行仅核验冻结字节。"""
from datetime import datetime, timedelta, timezone
import hashlib
import json
from pathlib import Path
import re
from .errors import Rejected

ORIGIN = 'https://github.com/dream-until-dawn/futures-position-simulator-go.git'
SCOPE = 'simnow-params-readonly'
BJT = timezone(timedelta(hours=8))
FIELDS = {'format', 'scope', 'source', 'main', 'plan_sha256', 'origin', 'refs',
          'window_start_bjt', 'window_end_bjt', 'verified_at_bjt'}


def now():
    """真实时刻仅在测试中注入；生产调用者没有时钟覆盖参数。"""
    return datetime.now(BJT)


def require(condition, reason):
    if not condition:
        raise Rejected(reason)


def timestamp(value):
    require(type(value) is str, 'invalid release timestamp')
    at = datetime.fromisoformat(value)
    require(at.utcoffset() == timedelta(hours=8), 'unqualified release timezone')
    return at


def bindings(cfg):
    """凭据只能用于准确源码和已封存计划所登记的五分钟只读窗口。"""
    require(all(type(cfg[key]) is str and re.fullmatch(r'[0-9a-f]{40}', cfg[key])
                for key in ['source', 'main']), 'invalid release source')
    require(type(cfg['plan_sha256']) is str and
            re.fullmatch(r'[0-9a-f]{64}', cfg['plan_sha256']), 'invalid release plan')
    start = timestamp(cfg['release_window_start_bjt'])
    end = timestamp(cfg['release_window_end_bjt'])
    require(timedelta(0) < end-start <= timedelta(minutes=5), 'invalid release window')
    return dict(format=1, scope=SCOPE, source=cfg['source'], main=cfg['main'],
                plan_sha256=cfg['plan_sha256'], origin=ORIGIN,
                refs={name: cfg['main'] for name in ['main', 'dev']},
                window_start_bjt=cfg['release_window_start_bjt'],
                window_end_bjt=cfg['release_window_end_bjt'])


def validate(record, cfg):
    expected = bindings(cfg)
    require(type(record) is dict and set(record) == FIELDS, 'invalid release schema')
    require(type(record['format']) is int and record['format'] == 1, 'invalid release version')
    require(all(record[key] == value for key, value in expected.items()), 'release binding changed')
    issued = timestamp(record['verified_at_bjt'])
    start = timestamp(record['window_start_bjt'])
    end = timestamp(record['window_end_bjt'])
    require(issued <= start and timedelta(0) < end-issued <= timedelta(minutes=30),
            'release lifetime exceeded')
    current = now()
    require(issued <= current < end, 'release future or expired')


def unique_object(pairs):
    record = {}
    for key, value in pairs:
        require(key not in record, 'duplicate release key')
        record[key] = value
    return record


def capture(ctx, cfg):
    """发布阶段单次核真实远端；失败不写凭据，原始命令仍由Attempt留证。"""
    record = bindings(cfg)
    root = Path(cfg['root'])
    origin = ctx.command(['git', '-c', 'http.version=HTTP/1.1',
                          'remote', 'get-url', 'origin'], root)
    require(origin == ORIGIN, 'unexpected release origin')
    lines = ctx.command(['git', '-c', 'http.version=HTTP/1.1', 'ls-remote',
                         'origin', 'refs/heads/main', 'refs/heads/dev'], root).splitlines()
    expected = {cfg['main']+'\trefs/heads/'+name for name in ['main', 'dev']}
    require(len(lines) == 2 and set(lines) == expected, 'unknown or changed release refs')
    record['verified_at_bjt'] = now().isoformat()
    validate(record, cfg)
    raw = (json.dumps(record, ensure_ascii=True, allow_nan=False, indent=2)+'\n').encode()
    # Journal强制排他写、fsync和读回；不覆盖凭据或吞掉持久化异常。
    return ctx.journal.raw('release-snapshot.json', raw)


def verify(ctx, cfg):
    """执行阶段没有联网、捕获重试或实时检查回退；必须绑定已封存字节。"""
    expected = cfg['release_sha256']
    require(type(expected) is str and re.fullmatch(r'[0-9a-f]{64}', expected),
            'invalid release digest')
    raw = Path(cfg['release']).read_bytes()
    require(hashlib.sha256(raw).hexdigest() == expected, 'release content changed')
    record = json.loads(raw, object_pairs_hook=unique_object)
    validate(record, cfg)
    ctx.journal.event('release-binding', dict(sha256=expected, scope=SCOPE,
                                            verified_at_bjt=record['verified_at_bjt']))
