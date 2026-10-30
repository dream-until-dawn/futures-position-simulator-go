"""可选Go入口快照；缺失保持未知，零只指取样前没有已提交入口记录。"""
import re
from .parsing import EvidenceError, boolean, integer

STAGES = ('front','authenticate','login','settlement-confirm','general-error','disconnected')
FIELDS = {'stage','count','first_ns','last_ns','at_ns','request','code','last','wrapped_payload'}


def unsigned(text, maximum):
    if not re.fullmatch(r'[0-9]{1,20}',text) or int(text)>maximum:
        raise EvidenceError('invalid-entry-number')
    return int(text)


def parse_entries(raw):
    try:
        lines=raw.decode('utf-8-sig').splitlines()
    except UnicodeDecodeError:
        raise EvidenceError('invalid-log-encoding') from None
    journal={}
    for line in lines:
        marker='[ctp.handshake.entry] '
        if '[ctp.handshake.entry' not in line:
            continue
        if marker not in line:
            raise EvidenceError('malformed-entry-record')
        text=line.split(marker,1)[1]
        fields=re.findall(r'([a-z_]+)=([^\s]*)',text)
        row=dict(fields)
        if ' '.join(k+'='+v for k,v in fields)!=text or len(row)!=len(fields) or set(row)!=FIELDS:
            raise EvidenceError('malformed-entry-record')
        stage=row['stage']
        if stage not in STAGES or stage in journal:
            raise EvidenceError('unknown-or-duplicate-entry-stage')
        count=unsigned(row['count'],2**64-1)
        first,last,at=[unsigned(row[key],2**63-1) for key in ['first_ns','last_ns','at_ns']]
        request=integer(row['request'],0,2147483647)
        code=integer(row['code'],-2147483648,2147483647)
        final,payload=boolean(row['last']),boolean(row['wrapped_payload'])
        if count==0:
            if first or last or request or code or final or payload:
                raise EvidenceError('zero-entry-has-metadata')
            journal[stage]=dict(count=0,at_ns=at,first_ns=None,last_ns=None,request=None,code=None,last=None,wrapped_payload=None)
        else:
            if first>last or last>at or (count==1 and first!=last):
                raise EvidenceError('inconsistent-entry-time')
            journal[stage]=dict(count=count,at_ns=at,first_ns=first,last_ns=last,request=request,code=code,last=final,wrapped_payload=payload)
    if journal and set(journal)!=set(STAGES):
        raise EvidenceError('incomplete-entry-journal')
    return journal or None
