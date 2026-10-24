"""验证运行器真实超时、进程树、中断、互斥与原子写入的正反例。"""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import patch
from validation_runtime import atomic_json, bounded_process, exclusive_run
from validate import run_checked


class RuntimeTests(unittest.TestCase):
    def test_lock_conflict_and_release_after_interrupt(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            with self.assertRaises(KeyboardInterrupt):
                with exclusive_run(root):
                    with self.assertRaisesRegex(RuntimeError, "禁止并行"):
                        with exclusive_run(root):
                            self.fail("second owner entered")
                    raise KeyboardInterrupt()
            with exclusive_run(root):
                pass

    def test_atomic_report_preserves_previous_on_failed_replace(self):
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder) / "summary.json"
            atomic_json(path, {"status": "running"})
            with patch("validation_runtime.os.replace", side_effect=OSError("disk")):
                with self.assertRaises(OSError):
                    atomic_json(path, {"status": "passed"})
            self.assertEqual(json.loads(path.read_bytes())["status"], "running")
            self.assertEqual(list(path.parent.iterdir()), [path])
            atomic_json(path, {"status": "failed"})
            self.assertEqual(json.loads(path.read_bytes())["status"], "failed")

    def test_timeout_kills_child_and_preserves_output(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            marker = root / "child-survived"
            child = "import time,pathlib;time.sleep(3);pathlib.Path(" + repr(str(marker)) + ").write_text('bad')"
            parent = "import subprocess,sys,time;subprocess.Popen([sys.executable,'-c'," + repr(child) + "]);print('started',flush=True);time.sleep(30)"
            start = time.monotonic()
            with self.assertRaisesRegex(RuntimeError, "命令超时"):
                run_checked([sys.executable, "-c", parent], root, root / "timeout.log", timeout=1)
            self.assertLess(time.monotonic() - start, 10)
            self.assertIn("started", (root / "timeout.log").read_text())
            time.sleep(3)
            self.assertFalse(marker.exists(), "descendant survived timeout")
            result = run_checked([sys.executable, "-c", "print('ok')"], root, root / "ok.log", timeout=5)
            self.assertEqual(result["exit_code"], 0)

    def test_interrupt_reaps_running_process(self):
        with tempfile.TemporaryDirectory() as folder:
            with self.assertRaises(KeyboardInterrupt):
                with bounded_process([sys.executable, "-c", "import time;time.sleep(30)"], folder, 10) as process:
                    raise KeyboardInterrupt()
            self.assertIsNotNone(process.poll())

    def test_timeout_after_parent_exits_still_kills_descendant(self):
        with tempfile.TemporaryDirectory() as folder:
            marker = Path(folder) / "orphan-survived"
            child = "import time,pathlib;time.sleep(3);pathlib.Path(" + repr(str(marker)) + ").write_text('bad')"
            parent = "import subprocess,sys,time;subprocess.Popen([sys.executable,'-c'," + repr(child) + "]);time.sleep(.2)"
            with self.assertRaisesRegex(RuntimeError, "命令超时"):
                run_checked([sys.executable, "-c", parent], folder, Path(folder) / "orphan.log", timeout=1)
            time.sleep(3)
            self.assertFalse(marker.exists(), "orphan survived after root exited")

    def test_success_also_reaps_detached_output_descendant(self):
        with tempfile.TemporaryDirectory() as folder:
            marker = Path(folder) / "background-survived"
            child = "import time,pathlib;time.sleep(2);pathlib.Path(" + repr(str(marker)) + ").write_text('bad')"
            parent = "import subprocess,sys;subprocess.Popen([sys.executable,'-c'," + repr(child) + "],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)"
            result = run_checked([sys.executable, "-c", parent], folder, Path(folder) / "success.log", timeout=5)
            self.assertEqual(result["exit_code"], 0)
            time.sleep(2.5)
            self.assertFalse(marker.exists(), "background descendant survived normal completion")


if __name__ == "__main__":
    unittest.main()
