"""复核器必须同时接受真实形状的正例、拒绝每一类伪完整结果。"""
import copy
import json
import unittest
from verify_evidence import sha256, verify_bundle


class EvidenceTests(unittest.TestCase):
    def fixture(self):
        source = "a" * 40
        registry = [{"name": "1 red", "pkg": ".", "test": "TestOne", "expect": "red", "want": "assert"},
                    {"name": "2 green", "pkg": ".", "test": "TestTwo", "expect": "green", "want": ""}]
        report = [dict(registry[0], verdict="红对了", detail="file_test.go:7: assert"),
                  dict(registry[1], verdict="如预期仍然绿", detail="")]
        state = {"head": source, "status": "", "tracked_sha256": "b"*64, "index_sha256": "c"*64}
        summary = {"suite": "mutation", "status": "passed", "failure": None, "before": state,
                   "after": copy.deepcopy(state), "completed": [{"command": ["go", "run", "./tools/breakcheck", "-out", "report.json"], "exit_code": 0}]}
        return registry, report, summary

    def check(self, registry, report, summary, corrupt_hash=False):
        raw = [json.dumps(value, ensure_ascii=False).encode() for value in [registry, report, summary]]
        cert = {"format": 1, "source_commit": "a"*40}
        cert.update({name+"_sha256": sha256(blob) for name, blob in zip(["registry", "report", "summary"], raw)})
        if corrupt_hash:
            cert["report_sha256"] = "0"*64
        return verify_bundle(cert, *raw)

    def test_complete_and_reject_every_invalid_boundary(self):
        self.assertEqual(self.check(*self.fixture()), {"source_commit": "a"*40, "total": 2, "red": 1, "green": 1, "unexpected": 0})
        changes = [
            lambda r,p,s: p.pop(),
            lambda r,p,s: p.__setitem__(1, copy.deepcopy(p[0])),
            lambda r,p,s: p[0].update(test="Wrong"),
            lambda r,p,s: p[0].update(verdict="编译通过"),
            lambda r,p,s: p[0].update(detail="wrong reason"),
            lambda r,p,s: s.update(status="running"),
            lambda r,p,s: s.update(failure="restore error"),
            lambda r,p,s: s["after"].update(tracked_sha256="d"*64),
            lambda r,p,s: s["before"].update(status=" M x"),
            lambda r,p,s: s["before"].update(head="e"*40),
            lambda r,p,s: s["completed"][0].update(exit_code=1),
            lambda r,p,s: s["completed"][0]["command"].append("-compile"),
            lambda r,p,s: s["completed"].append(copy.deepcopy(s["completed"][0])),
        ]
        for i, change in enumerate(changes):
            with self.subTest(boundary=i):
                r,p,s = self.fixture()
                change(r,p,s)
                with self.assertRaises(ValueError):
                    self.check(r,p,s)
        with self.assertRaisesRegex(ValueError, "SHA256"):
            self.check(*self.fixture(), corrupt_hash=True)

    def test_legacy_green_missing_want_uses_go_string_default(self):
        registry, report, summary = self.fixture()
        del registry[1]["want"]
        self.assertEqual(self.check(registry, report, summary)["green"], 1)
        # 只兼容登记省略；报告自身仍须携带 Go 输出的空字符串。
        report[1]["want"] = "unexpected"
        with self.assertRaisesRegex(ValueError, "条目来源字段不匹配"):
            self.check(registry, report, summary)
