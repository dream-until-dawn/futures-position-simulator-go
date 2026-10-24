"""独立复核一次完整破坏扫描；检查一致性，不提供柜台真实性或签名证明。"""
import argparse
import hashlib
import json
from pathlib import Path
import re
import subprocess


def sha256(raw):
    return hashlib.sha256(raw).hexdigest()


def verify_bundle(certificate, registry_raw, report_raw, summary_raw):
    """纯数据校验：拒绝不完整、重复、篡改、编译模式与补跑拼接。"""
    def require(condition, reason):
        if not condition:
            raise ValueError(reason)
    require(certificate.get("format") == 1, "证书格式无效")
    source = certificate.get("source_commit", "")
    require(re.fullmatch(r"[0-9a-f]{40}", source), "源码提交必须是完整 SHA")
    for name, raw in [("registry", registry_raw), ("report", report_raw), ("summary", summary_raw)]:
        require(certificate.get(name + "_sha256") == sha256(raw), name + " SHA256 不匹配")
    registry, report, summary = map(json.loads, [registry_raw, report_raw, summary_raw])
    require(summary.get("suite") == "mutation" and summary.get("status") == "passed" and summary.get("failure") is None, "该轮未成功完成")
    before, after = summary.get("before"), summary.get("after")
    require(isinstance(before, dict) and before == after and before.get("status") == "" and before.get("head") == source, "源码或收尾状态不一致")
    for key in ["tracked_sha256", "index_sha256"]:
        require(re.fullmatch(r"[0-9a-f]{64}", before.get(key, "")), "缺少内容/索引指纹")
    runs = summary.get("completed", [])
    require(len(runs) == 1 and runs[0].get("exit_code") == 0, "退出码或命令数量无效")
    command = runs[0].get("command", [])
    require(len(command) == 5 and command[:4] == ["go", "run", "./tools/breakcheck", "-out"], "不是完整破坏命令")
    require(isinstance(registry, list) and isinstance(report, list) and len(registry) > 0 and len(registry) == len(report), "清单覆盖数量不一致")
    expected = {row["name"]: row for row in registry}
    actual = {row["name"]: row for row in report}
    require(len(expected) == len(registry) and len(actual) == len(report) and expected.keys() == actual.keys(), "清单存在重复/缺项/额外项")
    counts = {"red": 0, "green": 0}
    for name, row in expected.items():
        observed = actual[name]
        require(all(observed.get(key) == row.get(key) for key in ["name", "pkg", "test", "expect", "want"]), "条目来源字段不匹配：" + name)
        expect = row["expect"]
        require(expect in counts, "未知预期分类")
        verdict = "红对了" if expect == "red" else "如预期仍然绿"
        require(observed.get("verdict") == verdict, "异常判定：" + name)
        if expect == "red":
            require(bool(row["want"]) and row["want"] in observed.get("detail", ""), "缺少对应断言：" + name)
        counts[expect] += 1
    return {"source_commit": source, "total": len(report), **counts, "unexpected": 0}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--certificate", type=Path, required=True)
    parser.add_argument("--report", type=Path, required=True)
    parser.add_argument("--summary", type=Path, required=True)
    parser.add_argument("--repo", type=Path, default=Path(__file__).resolve().parents[1])
    args = parser.parse_args()
    cert = json.loads(args.certificate.read_bytes())
    source = cert.get("source_commit", "")
    if not re.fullmatch(r"[0-9a-f]{40}", source):
        raise ValueError("源码提交无效")
    tree = subprocess.check_output(["git", "rev-parse", source + "^{tree}"], cwd=args.repo).decode().strip()
    if tree != cert.get("source_tree"):
        raise ValueError("源码 tree 不匹配")
    registry = subprocess.check_output(["git", "show", source + ":tools/breakcheck/breaks.json"], cwd=args.repo)
    print(json.dumps(verify_bundle(cert, registry, args.report.read_bytes(), args.summary.read_bytes()), ensure_ascii=False))


if __name__ == "__main__":
    import sys
    sys.stdout.reconfigure(encoding="utf-8")
    main()
