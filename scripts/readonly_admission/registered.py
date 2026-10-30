"""新登记的固定只读接线；调用者必须先核源码和登记文件哈希。"""
import hashlib
import json
import os
from pathlib import Path
import re
from handshake_diagnostics.reporting import build_report, write_report
from .checks import Checks
from .envfile import locked_env
from .runner import run_once


def diagnose(folder, source, trading_day):
    """只读取既有执行记录，保留握手失败样本，不生成不存在的柜台事件。"""
    folder = Path(folder)
    commands = [json.loads(path.read_bytes()) for path in folder.glob('*-command.json')]
    commands = [row for row in commands if row['stage'] == 'execution']
    if len(commands) != 1:
        raise ValueError('no unique completed execution record')
    command = commands[0]
    code = command['observed_exit_code']
    if type(code) is not int:
        raise ValueError('unknown process exit')
    output = command['output']
    path = folder / output['file']
    raw = path.read_bytes()
    if hashlib.sha256(raw).hexdigest() != output['sha256']:
        raise ValueError('execution output changed')
    report = build_report([dict(id='registered-readonly', source_commit=source,
        path=str(path), sha256=output['sha256'], deadline_ns=10000000000,
        process_exit=code, send_code_origin='native')])
    target = folder / 'handshake-diagnosis.json'
    if target.exists():
        if json.loads(target.read_bytes()) != report:
            raise ValueError('existing diagnosis differs')
    else:
        write_report(target, report)
    case = report['cases'][0]
    days = re.findall(r'\[ctp\] 握手完整成功，交易日=([0-9]{8})，最大报单引用=[0-9]+', raw.decode('utf-8-sig'))
    day = days[0] if len(days) == 1 else None
    if code == 0:
        journal = case['entry_journal']
        if day != trading_day or case['analysis']['status'] != 'handshake-complete' or not journal or any(journal[key]['count'] < 1 for key in ['front', 'authenticate', 'login', 'settlement-confirm']):
            raise ValueError('successful process lacks qualified handshake evidence')
        # 固定v6.5.1/goctp v1.10.17声明枚举；未知/零结构不能冒充参数查询成功。
        text = raw.decode('utf-8-sig')
        for key, values in [('MarginPriceType', '1234'), ('Algorithm', '1234'), ('AvailIncludeCloseProfit', '02')]:
            found = re.findall(r'^\s*'+key+r'\s+"([0-9])"\s+[^\r\n]*$', text, re.MULTILINE)
            if len(found) != 1 or found[0] not in values:
                raise ValueError('missing or unknown broker parameter declaration')
    return dict(process_exit=code, status=case['analysis']['status'], trading_day=day,
                report_sha256=hashlib.sha256(target.read_bytes()).hexdigest())


def inspect_processes(ctx, cfg):
    """查询未知即拒绝，不把权限错误当作没有进程；排除本次封装自己。"""
    ctx.require(os.name == 'nt', 'registered target requires Windows')
    # 仅返回计数，不把其他应用命令行或身份写入本轮证据。
    script = ("[Console]::OutputEncoding=[System.Text.UTF8Encoding]::new($false);"
        "$ErrorActionPreference='Stop';$p=@(Get-CimInstance Win32_Process | Where-Object {"
        "$_.Name -like '*ctp-readonly*' -or ($_.Name -like '*python*' -and "
        "$_.CommandLine -like '*"+cfg['operator_path'].replace("'", "''")+"*' -and $_.ProcessId -ne "+str(os.getpid())+")});"
        "@{matching_count=$p.Count;query_succeeded=$true}|ConvertTo-Json -Compress")
    state = json.loads(ctx.command(['powershell', '-NoProfile', '-Command', script], cfg['root']))
    ctx.require(state == dict(matching_count=0, query_succeeded=True), 'unknown or existing process')


def execute_readonly(ctx, cfg):
    """只在全部准入成功后锁定既有文件；固定argv由source阶段再次约束。"""
    with ctx.stage('env'):
        ctx.check_window()
        with locked_env(Path(cfg['root']) / '.env') as fingerprint:
            ctx.journal.event('environment', dict(sha256=fingerprint))
            with ctx.stage('execution'):
                ctx.check_window()
                ctx.command(cfg['argv'], cfg['root'], timeout=40)
                # 有退出0也必须有完整诊断与真实交易日，才可完成本轮。
                with ctx.stage('diagnosis'):
                    diagnose(ctx.journal.directory, cfg['source'], cfg['trading_day'])


def run_registered(cfg, directory, allowed):
    """具体日期/目标由经核验的封装固定；没有命令行绕过或内部重试。"""
    checks = Checks(cfg)
    steps = [('process', lambda ctx: inspect_processes(ctx, cfg)), *checks.steps()]
    identity = dict(source_commit=cfg['source'], published_main=cfg['main'],
                    argv_sha256=hashlib.sha256(json.dumps(cfg['argv'], ensure_ascii=True).encode()).hexdigest())
    return run_once(directory, identity, steps, allowed,
                    lambda ctx: execute_readonly(ctx, cfg))
