"""验证实际Go输出的跳过与零测试判定，不用伪造PASS字符串作正对照。"""
import tempfile
import unittest
from pathlib import Path
from validate import run_checked


class ValidationEvidenceTests(unittest.TestCase):
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
