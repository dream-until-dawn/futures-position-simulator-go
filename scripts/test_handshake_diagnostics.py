"""全部使用合成数据及临时目录；不读取真实凭据，不构造柜台连接。"""
import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

from handshake_diagnostics.parsing import EvidenceError, duration_ns, parse_log
from handshake_diagnostics.analysis import analyze
from handshake_diagnostics.reporting import build_report, write_report


def line(stage, request, event, elapsed, code=0, metadata=True, received=None, last=None, payload=None):
    text = f'[ctp.handshake] stage={stage} request={request} event={event} code={code} elapsed={elapsed}'
    if metadata:
        response = event == 'response'
        received = (stage if response else '') if received is None else received
        last = response if last is None else last
        payload = response if payload is None else payload
        text += f' received_stage={received} received_request={request if received else 0} last={str(last).lower()} has_payload={str(payload).lower()}'
    return text


def fixture(success=True, metadata=True):
    rows = []
    for stage,request,start,end in [('front',0,'50ms','99ms'),('authenticate',1,'100ms','600ms'),('login',2,'600ms','1.2757791s')]:
        rows.append(line(stage,request,'begin',start,metadata=metadata))
        if request:
            rows.append(line(stage,request,'send-return',start,metadata=metadata))
        rows.extend([line(stage,request,'response',end,metadata=metadata),line(stage,request,'finished',end,metadata=metadata)])
    at = '1.2757791s' if success else '1.6250514s'
    rows += [line('settlement-confirm',3,'begin',at,metadata=metadata),line('settlement-confirm',3,'send-return',at,metadata=metadata)]
    if success:
        rows += [line('settlement-confirm',3,'response','4.2250658s',metadata=metadata),line('settlement-confirm',3,'finished','4.2250658s',metadata=metadata)]
    else:
        rows += [line('settlement-confirm',3,'timeout','10.0005823s',metadata=metadata)]
    return ('\n'.join(rows)+'\n').encode()


def case(path, raw, name='sample'):
    path.write_bytes(raw)
    return dict(id=name,source_commit='a'*40,path=str(path),sha256=hashlib.sha256(raw).hexdigest(),
        deadline_ns=10000000000,process_exit=0,send_code_origin='native')


class HandshakeDiagnosticsTests(unittest.TestCase):
    def test_duration_exact_boundaries(self):
        self.assertEqual(duration_ns('1.6250514s'),1625051400,'纳秒时刻解析不精确')
        self.assertEqual(duration_ns('1h2m3.000000001s'),3723000000001)
        self.assertEqual(duration_ns('1µs'),1000)
        for value in ['-1s','1e3s','nan','1.0000000001s','9223372036854775808ns']:
            with self.assertRaises(EvidenceError):duration_ns(value)

    def test_success_and_timeout_budget(self):
        good = analyze(parse_log(fixture()),10000000000,0)
        bad = analyze(parse_log(fixture(False)),10000000000,1)
        self.assertEqual(good['status'],'handshake-complete')
        self.assertEqual(good['confirm_response_wait_ns'],2949286700,'确认等待错误')
        self.assertEqual(bad['remaining_at_confirm_send_ns'],8374948600,'总预算计算错误')
        self.assertEqual(bad['status'],'timeout')
        self.assertEqual(bad['confirm_callback_events'],0)
        self.assertFalse(bad['confirm_callback_absence_proven'],'无日志被升格为没有柜台回调')

    def test_legacy_metadata_remains_unknown(self):
        events = parse_log(fixture(False,False))
        self.assertIsNone(events[-1].last,'旧日志缺字段被当成false')
        result = analyze(events,10000000000,1)
        self.assertFalse(result['metadata_complete'])
        self.assertFalse(result['confirm_callback_absence_proven'])

    def test_print_interleaving_is_not_protocol_order(self):
        rows = fixture().decode().splitlines()
        response = rows.pop(1)
        rows.insert(4,response)
        result = analyze(parse_log(('\n'.join(rows)+'\n').encode()),10000000000,0)
        self.assertEqual(result['status'],'handshake-complete','打印交错被误判协议失败')
        self.assertTrue(result['record_order_differs_from_event_time'],'打印交错未被识别')

    def test_synchronous_reply_and_send_rejection(self):
        raw = fixture().replace(b'event=send-return code=0 elapsed=1.2757791s',b'event=send-return code=-3 elapsed=1.2757791s')
        result = analyze(parse_log(raw),10000000000,1)
        self.assertEqual(result['status'],'send-rejected','成功回调掩盖发送非零')

    def test_callback_before_send_return_is_not_negative_wait(self):
        raw=fixture().replace(b'event=begin code=0 elapsed=1.2757791s',b'event=begin code=0 elapsed=1.27s').replace(b'4.2250658s',b'1.2757790s')
        result=analyze(parse_log(raw),10000000000,0)
        self.assertEqual(result['confirm_response_wait_ns'],0)
        self.assertTrue(result['confirm_response_before_send_return'])

    def test_filtered_callback_is_observed(self):
        rows=fixture(False).decode().splitlines()
        rows.insert(-1,line('settlement-confirm',3,'ignored-request','2s',received='settlement-confirm').replace('received_request=3','received_request=999'))
        result=analyze(parse_log(('\n'.join(rows)+'\n').encode()),10000000000,1)
        self.assertEqual(result['confirm_callback_events'],1,'被过滤回调被当作没有回调')
        self.assertEqual(result['status'],'timeout')

    def test_response_error_and_missing_payload(self):
        raw=fixture().replace(b'event=response code=0 elapsed=4.2250658s',b'event=response code=51 elapsed=4.2250658s')
        self.assertEqual(analyze(parse_log(raw),10000000000,1)['status'],'response-error')
        raw=fixture().replace(b'elapsed=4.2250658s received_stage=settlement-confirm received_request=3 last=true has_payload=true',b'elapsed=4.2250658s received_stage=settlement-confirm received_request=3 last=true has_payload=false')
        self.assertEqual(analyze(parse_log(raw),10000000000,1)['status'],'missing-response-payload')

    def test_partial_wrong_stage_and_duplicate_rejected(self):
        raw=fixture().replace(b'received_stage=settlement-confirm received_request=3 last=true',b'received_stage=login received_request=3 last=true')
        with self.assertRaisesRegex(EvidenceError,'inconsistent-response'):analyze(parse_log(raw),10000000000,1)
        raw=fixture().replace(b'received_stage=settlement-confirm received_request=3 last=true',b'received_stage=settlement-confirm received_request=3 last=false')
        with self.assertRaises(EvidenceError):analyze(parse_log(raw),10000000000,1)
        with self.assertRaises(EvidenceError):analyze(parse_log(fixture()+fixture()),10000000000,0)

    def test_prepare_timeout_without_send_is_unknown_send(self):
        rows=fixture(False).decode().splitlines()[:-3]
        rows.append(line('settlement-confirm',3,'timeout','10.1s'))
        result=analyze(parse_log(('\n'.join(rows)+'\n').encode()),10000000000,1)
        self.assertIsNone(result['confirm_send_ns'])
        self.assertIsNone(result['remaining_at_confirm_send_ns'])

    def test_incomplete_and_process_conflict(self):
        raw=('\n'.join(fixture(False).decode().splitlines()[:-1])+'\n').encode()
        self.assertEqual(analyze(parse_log(raw),10000000000,1)['status'],'incomplete-log')
        with self.assertRaisesRegex(EvidenceError,'process-log-conflict'):analyze(parse_log(fixture(False)),10000000000,0)
        self.assertTrue(analyze(parse_log(fixture()),10000000000,1)['process_failed_after_handshake'])

    def test_strict_fields_do_not_echo_secrets(self):
        for raw in [fixture().replace(b'code=0',b'code=SECRET',1),fixture().replace(b'event=begin',b'event=SECRET',1),fixture().replace(b'elapsed=50ms',b'elapsed=50ms user=SECRET',1),fixture().replace(b'code=0',b'code=0 code=0',1),b'\xff']:
            with self.assertRaises(EvidenceError) as caught:parse_log(raw)
            self.assertNotIn('SECRET',str(caught.exception))
        with self.assertRaisesRegex(EvidenceError,'no-handshake-evidence'):parse_log(b'credentials=SECRET\n')

    def test_case_hash_origin_and_comparison(self):
        with tempfile.TemporaryDirectory() as temp:
            a=case(Path(temp)/'a.log',fixture(),'success')
            b=case(Path(temp)/'b.log',fixture(False),'timeout');b['process_exit']=1
            result=build_report([a,b])
            self.assertEqual(result['comparison'][0]['cause'],'undetermined')
            self.assertEqual(result['comparison'][0]['inference'],'budget-not-smaller-than-observed-success')
            text=json.dumps(result)
            self.assertNotIn(str(temp),text,'报告泄漏私有日志路径')
            a['sha256']='0'*64
            with self.assertRaisesRegex(EvidenceError,'input-fingerprint-mismatch',msg='日志哈希不匹配未拒绝'):build_report([a])
            a['sha256']=hashlib.sha256(fixture()).hexdigest();a['send_code_origin']='unknown'
            with self.assertRaises(EvidenceError):build_report([a])

    def test_invalid_case_identity_and_source(self):
        with tempfile.TemporaryDirectory() as temp:
            original=case(Path(temp)/'a.log',fixture())
            for key,value in [('id','SECRET/path'),('source_commit','abcd'),('deadline_ns',True),('process_exit',None)]:
                changed=dict(original);changed[key]=value
                with self.assertRaises(EvidenceError):build_report([changed])
            with self.assertRaises(EvidenceError):build_report([original,original])

    def test_output_is_exclusive_and_roundtrips(self):
        with tempfile.TemporaryDirectory() as temp:
            report=build_report([case(Path(temp)/'private.log',fixture())])
            output=Path(temp)/'out.json';write_report(output,report)
            self.assertEqual(json.loads(output.read_bytes()),report)
            before=output.read_bytes()
            with self.assertRaises(FileExistsError,msg='旧报告被覆盖'):write_report(output,{'replacement':True})
            self.assertEqual(output.read_bytes(),before)

    def test_real_cli_success_failure_and_no_overwrite(self):
        with tempfile.TemporaryDirectory() as temp:
            folder=Path(temp);raw=fixture();item=case(folder/'private.log',raw)
            manifest=folder/'manifest.json';manifest.write_text(json.dumps([item]),encoding='utf-8')
            output=folder/'out.json';command=[sys.executable,str(Path(__file__).with_name('diagnose_handshake.py')),'--manifest',str(manifest),'--out',str(output)]
            result=subprocess.run(command,capture_output=True)
            self.assertEqual(result.returncode,0,result.stderr)
            before=output.read_bytes();result=subprocess.run(command,capture_output=True)
            self.assertNotEqual(result.returncode,0,'CLI覆盖已有报告')
            self.assertEqual(output.read_bytes(),before)
            item['sha256']='0'*64;manifest.write_text(json.dumps([item]),encoding='utf-8');output.unlink()
            result=subprocess.run(command,capture_output=True)
            self.assertNotEqual(result.returncode,0,'CLI哈希不匹配仍成功')
            self.assertNotIn(str(folder).encode(),result.stderr)
            self.assertFalse(output.exists())


if __name__ == '__main__':
    unittest.main()
