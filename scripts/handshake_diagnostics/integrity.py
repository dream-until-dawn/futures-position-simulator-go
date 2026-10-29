"""核对收集器已经承诺的事件时间语义，不用打印顺序推断网络行为。"""
from .parsing import EvidenceError


def validate_stage_timing(rows, deadline_ns):
    """只拒绝矛盾证据；缺尾日志、同步回调和prepare超时保持原有诊断语义。"""
    starts = [row for row in rows if row.event == 'begin']
    responses = [row for row in rows if row.event == 'response']
    finished = [row for row in rows if row.event == 'finished']
    # 接受回报和wait完成都会检查严格早于deadline；过滤的late回报不受此条件约束。
    if any(row.elapsed_ns >= deadline_ns for row in responses):
        raise EvidenceError('response-outside-deadline')
    if any(row.elapsed_ns >= deadline_ns for row in finished):
        raise EvidenceError('finished-outside-deadline')
    if any(row.elapsed_ns < deadline_ns for row in rows if row.event == 'timeout'):
        raise EvidenceError('timeout-before-deadline')
    if starts:
        begin = starts[0].elapsed_ns
        if begin >= deadline_ns:
            raise EvidenceError('begin-outside-deadline')
        if any(row.elapsed_ns < begin for row in responses):
            raise EvidenceError('response-before-begin')
        if any(row.elapsed_ns < begin for row in rows if row.event == 'send-return'):
            raise EvidenceError('send-before-begin')
    if finished and not responses:
        raise EvidenceError('finished-without-response')
    if responses and any(row.elapsed_ns < responses[0].elapsed_ns for row in finished):
        raise EvidenceError('finished-before-response')
