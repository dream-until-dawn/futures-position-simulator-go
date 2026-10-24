"""跨平台离线验证入口。仅启动测试/构建，不读取凭据或运行柜台命令。"""
import argparse
from collections import deque
import json
import subprocess
import sys
import tempfile
from pathlib import Path
from validation_runtime import snapshot, exclusive_run, atomic_json, bounded_process

ROOT = Path(__file__).resolve().parents[1]


def run_checked(command, cwd, log, require_tests=False, timeout=1800):
    """保存完整输出；真实跳过、零测试与进程失败均不能当作回归通过。"""
    passed, skipped = 0, []
    tail = deque(maxlen=60)
    with log.open("w", encoding="utf-8", newline="\n") as output:
        with bounded_process(command, cwd, timeout) as process:
            for line in process.stdout:
                output.write(line)
                output.flush()
                tail.append(line)
                try:
                    event = json.loads(line)
                except json.JSONDecodeError:
                    continue
                if not isinstance(event, dict):
                    continue
                if event.get("Test") and event.get("Action") == "pass":
                    passed += 1
                if event.get("Action") == "skip":
                    skipped.append(event.get("Test", event.get("Package")))
            code = process.wait()
    if code != 0:
        # CI控制台保留失败上下文，完整输出仍由artifact保存。
        print("底层命令失败，最后60行输出：\n" + "".join(tail), file=sys.stderr, flush=True)
        raise RuntimeError(f"验证失败 exit={code}，完整日志：{log}")
    if require_tests and (passed == 0 or skipped):
        raise RuntimeError(f"回归证据不完整 passed={passed} skipped={skipped}，日志：{log}")
    return {"command": command, "cwd": str(cwd), "passed_tests": passed,
            "skipped": skipped, "log": str(log), "exit_code": code}


def git_state():
    return snapshot(ROOT)


def main():
    with exclusive_run(ROOT):
        run_suite()


def run_suite():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--suite", choices=["regression", "race", "fuzz", "mutation", "bench"], default="regression")
    parser.add_argument("--out", type=Path, help="日志目录；默认创建系统临时目录")
    parser.add_argument("--fuzz-time", default="20s")
    parser.add_argument("--timeout", type=float, default=3600, help="每条命令墙钟上限秒数")
    parser.add_argument("--bench-time", default="30x", help="每次性能样本的操作次数/时长")
    parser.add_argument("--bench-count", type=int, default=5, help="性能样本数")
    args = parser.parse_args()
    out = (args.out or Path(tempfile.mkdtemp(prefix="futsim-validation-"))).resolve()
    out.mkdir(parents=True, exist_ok=True)
    atomic_json(out / "summary.json", {"suite": args.suite, "status": "running"})
    before = git_state()
    commands = []
    if args.suite in ["regression", "race"]:
        for name, cwd in [("root", ROOT), ("oracle", ROOT / "cmd/oracle")]:
            command = ["go", "test", "-json", "-count=1"]
            if args.suite == "race":
                command.append("-race")
            command.append("./...")
            commands.append((name, command, cwd, True))
    elif args.suite == "fuzz":
        for name, cwd, target in [("state", ROOT, "FuzzStateSequence"),
                                  ("report", ROOT / "cmd/oracle", "FuzzReadRestingReport")]:
            commands.append((name, ["go", "test", ".", "-run=^$", f"-fuzz=^{target}$",
                                   f"-fuzztime={args.fuzz_time}", "-parallel=2"], cwd, False))
    elif args.suite == "mutation":
        if before["status"]:
            raise RuntimeError("破坏验证要求已跟踪文件干净；请先提交工作")
        commands.append(("mutation", ["go", "run", "./tools/breakcheck", "-out", str(out / "mutations.json")], ROOT, False))
    else:
        if args.bench_count <= 0:
            raise ValueError("性能样本数必须为正")
        commands.append(("bench", ["go", "test", ".", "-run=^$", "-bench=^BenchmarkOffline", "-benchmem", f"-count={args.bench_count}", f"-benchtime={args.bench_time}"], ROOT, False))
    results = []
    status, failure = "failed", None
    after = None
    try:
        for name, command, cwd, strict in commands:
            results.append(run_checked(command, cwd, out / f"{name}.log", strict, args.timeout))
            print(f"通过 {args.suite}/{name}，日志：{out / (name + '.log')}", flush=True)
        after = git_state()
        if after != before:
            raise RuntimeError("验证改变了已跟踪文件，禁止将该轮视为通过")
        status = "passed"
    except BaseException as error:
        failure = str(error)
        raise
    finally:
        # Windows默认会将换行转成CRLF；报告也必须遵守仓库LF约定。
        atomic_json(out / "summary.json", {"suite": args.suite, "status": status, "failure": failure, "completed": results, "before": before, "after": after})


if __name__ == "__main__":
    # Windows托管运行器可能使用cp1252；中文诊断统一用UTF-8。
    sys.stdout.reconfigure(encoding="utf-8")
    sys.stderr.reconfigure(encoding="utf-8")
    main()
