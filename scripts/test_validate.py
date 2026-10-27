"""验证实际Go输出的跳过与零测试判定，不用伪造PASS字符串作正对照。"""
import os
import contextlib
import io
import json
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch
from pathlib import Path
from validate import main, run_checked


class ValidationEvidenceTests(unittest.TestCase):
    def test_summary_lf_on_success_and_failure(self):
        # 同时覆盖正常完成和失败落盘；按字节检查，不让文本读取自动归一化。
        for failed in [False, True]:
            with self.subTest(failed=failed), tempfile.TemporaryDirectory() as folder:
                with patch("sys.argv", ["validate.py", "--out", folder]), patch("validate.git_state", return_value=""), patch("validate.run_checked", side_effect=RuntimeError("故障") if failed else [None, None, None]), contextlib.redirect_stdout(io.StringIO()):
                    if failed:
                        with self.assertRaisesRegex(RuntimeError, "故障"):
                            main()
                    else:
                        main()
                raw = (Path(folder) / "summary.json").read_bytes()
                self.assertNotIn(b"\r", raw)
                self.assertIn(b"\n", raw)
                self.assertEqual(json.loads(raw)["status"], "failed" if failed else "passed")

    def test_process_failure_emits_diagnostic(self):
        with tempfile.TemporaryDirectory() as folder, contextlib.redirect_stderr(io.StringIO()) as diagnostic:
            with self.assertRaisesRegex(RuntimeError, "exit=7"):
                run_checked([sys.executable, "-c", "print('underlying failure'); raise SystemExit(7)"], Path(folder), Path(folder) / "failed.log")
            self.assertIn("underlying failure", diagnostic.getvalue())

    def test_cli_accepts_non_utf8_host_console(self):
        env = dict(os.environ, PYTHONIOENCODING="cp1252")
        command = [sys.executable, str(Path(__file__).with_name("validate.py")), "--help"]
        result = subprocess.run(command, env=env, capture_output=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("跨平台离线验证", result.stdout.decode("utf-8"))

    def test_real_go_pass_skip_and_zero_tests(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            (root / "go.mod").write_text("module evidence\n\ngo 1.23\n", encoding="utf-8")
            test = root / "evidence_test.go"
            test.write_text('package evidence\nimport "testing"\nfunc TestEvidence(t *testing.T) {}\n', encoding="utf-8")
            command = ["go", "test", "-json", "-count=1", "."]
            self.assertGreater(run_checked(command, root, root / "pass.log", True)["passed_tests"], 0)
            test.write_text('package evidence\nimport "testing"\nfunc TestEvidence(t *testing.T) {t.Skip("missing input")}\n', encoding="utf-8")
            with self.assertRaisesRegex(RuntimeError, "skipped="):
                run_checked(command, root, root / "skip.log", True)
            with self.assertRaisesRegex(RuntimeError, "passed=0"):
                run_checked(command + ["-run=^Missing$"], root, root / "zero.log", True)


if __name__ == "__main__":
    unittest.main()
