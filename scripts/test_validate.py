"""验证实际Go输出的跳过与零测试判定，不用伪造PASS字符串作正对照。"""
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from validate import run_checked


class ValidationEvidenceTests(unittest.TestCase):
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
