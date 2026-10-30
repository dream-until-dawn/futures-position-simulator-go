"""读取已登记哈希的私有输入，报告仅含白名单派生值和源码/内容指纹。"""
import hashlib
import json
from pathlib import Path
import re
from .parsing import EvidenceError, parse_log
from .analysis import analyze, compare
from .entries import parse_entries


def build_report(manifest):
    if not isinstance(manifest,list) or not manifest:
        raise EvidenceError('invalid-manifest')
    cases = []
    ids = set()
    for item in manifest:
        fields = {'id','source_commit','path','sha256','deadline_ns','process_exit','send_code_origin'}
        if not isinstance(item,dict) or set(item) != fields:
            raise EvidenceError('invalid-case-contract')
        if not isinstance(item['id'],str) or not re.fullmatch(r'[a-z0-9_-]{1,64}',item['id']) or item['id'] in ids:
            raise EvidenceError('invalid-or-duplicate-case-id')
        ids.add(item['id'])
        if not isinstance(item['source_commit'],str) or not re.fullmatch(r'[0-9a-f]{40}',item['source_commit']):
            raise EvidenceError('invalid-source-commit')
        if not isinstance(item['sha256'],str) or not re.fullmatch(r'[0-9a-f]{64}',item['sha256']):
            raise EvidenceError('invalid-content-fingerprint')
        if item['send_code_origin'] not in ['wrapper','native']:
            raise EvidenceError('unknown-send-code-origin')
        try:
            with Path(item['path']).open('rb') as stream:
                raw = stream.read(1048577)
        except (OSError,TypeError):
            raise EvidenceError('input-unreadable') from None
        if len(raw) > 1048576:
            raise EvidenceError('input-too-large')
        if hashlib.sha256(raw).hexdigest() != item['sha256']:
            raise EvidenceError('input-fingerprint-mismatch')
        result = analyze(parse_log(raw),item['deadline_ns'],item['process_exit'])
        cases.append(dict(id=item['id'],source_commit=item['source_commit'],sha256=item['sha256'],
            send_code_origin=item['send_code_origin'],analysis=result,entry_journal=parse_entries(raw)))
    return dict(format=2,cases=cases,comparison=compare(cases),
        limitations=['No new broker connection or credential access.','Input identity and native-vs-wrapper classification are declared and must be independently audited.',
            'No recorded callback does not prove no server or SDK callback occurred.','A successful sample is not a response-time upper bound.',
            'Entry counts are committed Go-entry observations at separate per-stage snapshots; zero is not absence of an SDK/server event.',
            'Wrapped non-null payload does not prove an original SDK payload; the fixed C++ bridge substitutes zero-valued rows for null.',
            'Heterogeneous samples do not estimate reliability or establish network/server root cause.'])


def write_report(path, report):
    """先完整编码，再排他写入；已有报告不得覆盖，落盘失败不得返回成功。"""
    raw = (json.dumps(report,ensure_ascii=False,indent=2)+'\n').encode('utf-8')
    with Path(path).open('xb') as stream:
        stream.write(raw)
        stream.flush()
        import os
        os.fsync(stream.fileno())
