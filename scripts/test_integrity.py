"""真实临时仓库复现：状态文本相同不代表字节相同。"""
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch
import validate


class TrackedContentTests(unittest.TestCase):
    def test_interrupted_mutation_blocks_next_run_until_restored(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            def git(*args):
                return subprocess.check_output(["git", "-C", folder, *args])
            git("init", "-q")
            source = root / "source.txt"
            source.write_bytes(b"clean\n")
            git("add", "source.txt")
            git("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "baseline")
            git("config", "core.autocrlf", "false")
            with patch("validate.ROOT", root):
                before = validate.git_state()
                command = [sys.executable, "-c", "from pathlib import Path;import time;Path('source.txt').write_bytes(b'broken');print('applied',flush=True);time.sleep(30)"]
                with self.assertRaisesRegex(RuntimeError, "命令超时"):
                    validate.run_checked(command, root, root / "interrupted.log", timeout=1)
                self.assertEqual(source.read_bytes(), b"broken")
                with patch("sys.argv", ["validate.py", "--suite", "mutation", "--out", str(root / "offline-results")]), patch("validate.run_checked") as runner:
                    with self.assertRaisesRegex(RuntimeError, "要求已跟踪文件干净"):
                        validate.main()
                    runner.assert_not_called()
                # 只恢复已确认的原始字节，禁止自动覆盖未核对的修改。
                source.write_bytes(b"clean\n")
                self.assertEqual(before, validate.git_state())

    def test_dirty_content_and_index_are_part_of_snapshot(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            def git(*args):
                return subprocess.check_output(["git", "-C", folder, *args])
            git("init", "-q")
            file = root / "tracked"
            file.write_bytes(b"base\n")
            git("add", "tracked")
            with patch("validate.ROOT", root):
                baseline = validate.git_state()
                file.write_bytes(b"dirty one\n")
                first = validate.git_state()
                file.write_bytes(b"dirty two\n")
                second = validate.git_state()
                self.assertNotEqual(first, second, "same porcelain hid changed content")
                self.assertNotEqual(baseline, first)
                git("add", "tracked")
                self.assertNotEqual(second, validate.git_state())
                stable = validate.git_state()
                self.assertEqual(stable, validate.git_state())
                # 工作树字节和 porcelain 都不变，仅替换暂存对象，仍须检出。
                file.write_bytes(b"worktree stays dirty\n")
                indexed_before = validate.git_state()
                other = subprocess.check_output(["git", "-C", folder, "hash-object", "-w", "--stdin"], input=b"other staged bytes\n").decode().strip()
                git("update-index", "--cacheinfo", "100644," + other + ",tracked")
                self.assertNotEqual(indexed_before, validate.git_state(), "index-only change hidden")


if __name__ == "__main__":
    unittest.main()
