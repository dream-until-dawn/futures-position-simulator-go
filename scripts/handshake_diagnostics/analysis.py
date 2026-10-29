"""只陈述已记录事实，阶段按编号聚合，打印先后不冒充协议顺序。"""
from .parsing import EvidenceError


def analyze(events, deadline_ns, process_exit):
    """在单轮证据中区分失败、未知与完整握手，绝不把日志缺失提升成柜台事实。"""
    if type(deadline_ns) is not int or deadline_ns <= 0 or type(process_exit) is not int:
        raise EvidenceError('invalid-run-contract')
    grouped = {}
    for event in events:
        grouped.setdefault((event.stage,event.request),[]).append(event)
    if sum(event.stage == 'front' and event.event == 'begin' for event in events) != 1:
        raise EvidenceError('missing-or-combined-rounds')
    stages = []
    for (stage,request), rows in grouped.items():
        starts = [row for row in rows if row.event == 'begin']
        sends = [row for row in rows if row.event == 'send-return']
        prepare_timed_out = not starts and all(row.event == 'timeout' for row in rows)
        if len(starts) > 1 or (not starts and not prepare_timed_out) or len(sends) > 1:
            raise EvidenceError('ambiguous-stage-records')
        responses = [row for row in rows if row.event == 'response']
        if len(responses) > 1:
            raise EvidenceError('ambiguous-stage-records')
        for row in responses:
            if row.received_stage is not None and (row.received_stage != stage or row.received_request != request or (row.code == 0 and row.last is not True)):
                raise EvidenceError('inconsistent-response-metadata')
        stages.append(dict(stage=stage,request=request,start_ns=starts[0].elapsed_ns if starts else min(row.elapsed_ns for row in rows),
            prepared=not prepare_timed_out,
            send_ns=sends[0].elapsed_ns if sends else None,
            send_code=sends[0].code if sends else None,
            response_ns=min(row.elapsed_ns for row in responses) if responses else None,
            response_codes=[row.code for row in responses],
            timed_out=any(row.event == 'timeout' for row in rows),
            finished=any(row.event == 'finished' for row in rows),
            ignored_events=[row.event for row in rows if row.event.startswith('ignored-')]))
    stages.sort(key=lambda row:row['start_ns'])
    expected_stages = ['front','authenticate','login','settlement-confirm']
    if [row['stage'] for row in stages] != expected_stages[:len(stages)]:
        raise EvidenceError('ambiguous-stage-sequence')
    rejected = any(row.event == 'send-return' and row.code != 0 for row in events)
    response_error = any(row.event == 'response' and row.code != 0 for row in events)
    missing_payload = any(row.event == 'response' and row.code == 0 and row.has_payload is False for row in events)
    timed_out = any(row.event == 'timeout' for row in events)
    disconnected = any(row.event in ['closed','disconnected'] for row in events)
    confirm = next((row for row in stages if row['stage'] == 'settlement-confirm'),None)
    if rejected:
        status = 'send-rejected'
    elif response_error:
        status = 'response-error'
    elif missing_payload:
        status = 'missing-response-payload'
    elif timed_out:
        status = 'timeout'
    elif disconnected:
        status = 'aborted'
    elif len(stages) == 4 and all(row['finished'] and row['response_ns'] is not None for row in stages) and all(row['send_ns'] is not None for row in stages[1:]):
        status = 'handshake-complete'
    else:
        status = 'incomplete-log'
    if process_exit == 0 and status != 'handshake-complete':
        raise EvidenceError('process-log-conflict')
    metadata_complete = all(row.received_stage is not None for row in events)
    # 过滤日志的stage是收集器当前阶段，实际回调阶段必须看received_stage。
    callbacks = [row for row in events if row.received_stage == 'settlement-confirm' or
        (row.received_stage is None and row.stage == 'settlement-confirm' and row.event == 'response')]
    sent = confirm['send_ns'] if confirm else None
    response = confirm['response_ns'] if confirm else None
    # send-return是原生函数返回时刻，不是发包时刻；同步回调可以先出现，等待量下界为0。
    return dict(status=status,process_exit=process_exit,stages=stages,
        metadata_complete=metadata_complete,confirm_callback_events=len(callbacks),
        confirm_callback_absence_proven=False,
        record_order_differs_from_event_time=any(a.elapsed_ns>b.elapsed_ns for a,b in zip(events,events[1:])),
        deadline_ns=deadline_ns,confirm_send_ns=sent,
        remaining_at_confirm_send_ns=deadline_ns-sent if sent is not None else None,
        confirm_response_wait_ns=max(0,response-sent) if sent is not None and response is not None else None,
        confirm_response_before_send_return=bool(sent is not None and response is not None and response < sent),
        response_before_local_timeout=bool(timed_out and response is not None and response < deadline_ns),
        process_failed_after_handshake=status == 'handshake-complete' and process_exit != 0)


def compare(cases):
    """成功样本不是延迟上界；只给受支持的时间对照，不估计概率或猜测根因。"""
    successful = [row['analysis']['confirm_response_wait_ns'] for row in cases
        if row['analysis']['status'] == 'handshake-complete' and row['analysis']['confirm_response_wait_ns'] is not None]
    comparisons = []
    for row in cases:
        analysis = row['analysis']
        remaining = analysis['remaining_at_confirm_send_ns']
        if analysis['status'] == 'timeout' and remaining is not None and successful:
            comparisons.append(dict(case_id=row['id'],remaining_ns=remaining,
                observed_success_wait_ns=min(successful),
                inference='budget-not-smaller-than-observed-success' if remaining >= min(successful) else 'budget-smaller-than-observed-success',
                cause='undetermined'))
    return comparisons
