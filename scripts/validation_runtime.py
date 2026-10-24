"""验证运行边界：内容快照、排他锁、原子报告与进程树清理。"""
from contextlib import contextmanager
import hashlib
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import threading


def snapshot(root):
    """读取实际字节及索引，不把相同 Git 状态误当成相同内容。"""
    def git(*args):
        return subprocess.check_output(["git", *args], cwd=root)
    files = git("ls-files", "-z").split(b"\0")
    digest = hashlib.sha256()
    for raw in files:
        if not raw:
            continue
        path = root / os.fsdecode(raw)
        content = (os.fsencode(os.readlink(path)) if path.is_symlink()
                   else path.read_bytes() if path.exists() else b"<missing>")
        digest.update(raw + b"\0" + hashlib.sha256(content).digest())
    # ls-files --stage 包含模式与暂存对象；无 HEAD 的临时仓库也可使用。
    return {"status": git("status", "--porcelain", "--untracked-files=no").decode("utf-8"),
            "tracked_sha256": digest.hexdigest(),
            "index_sha256": hashlib.sha256(git("ls-files", "--stage", "-z")).hexdigest(),
            "head": git("rev-parse", "--verify", "--quiet", "HEAD").decode().strip()
            if (root / ".git").exists() and subprocess.run(["git", "rev-parse", "--verify", "--quiet", "HEAD"], cwd=root, stdout=subprocess.DEVNULL).returncode == 0 else None}


@contextmanager
def exclusive_run(root):
    """以工作树为单位互斥；异常遗留锁必须人工确认进程退出后清理。"""
    lock_dir = root / "offline-results"
    lock_dir.mkdir(exist_ok=True)
    path = lock_dir / ".validation.lock"
    try:
        fd = os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
    except FileExistsError:
        raise RuntimeError(f"已有验证运行或遗留锁，禁止并行/自动抢占：{path}") from None
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as stream:
            json.dump({"pid": os.getpid(), "root": str(root)}, stream)
        yield
    finally:
        path.unlink()


def atomic_json(path, value):
    """同目录写完再替换，避免半截 JSON 被读取为完整证据。"""
    fd, temporary = tempfile.mkstemp(prefix=path.name + ".", dir=path.parent)
    try:
        with os.fdopen(fd, "w", encoding="utf-8", newline="\n") as stream:
            json.dump(value, stream, ensure_ascii=False, indent=2)
            stream.write("\n")
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
    finally:
        Path(temporary).unlink(missing_ok=True)


def stop_tree(process):
    """即使孙进程仍持有输出管道，也必须终止整个命令树。"""
    if getattr(process, "validation_job", None) is not None:
        process.validation_job.terminate()
    elif os.name == "nt":
        subprocess.run(["taskkill", "/PID", str(process.pid), "/T", "/F"],
                       stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=15)
    else:
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass


@contextmanager
def bounded_process(command, cwd, timeout):
    """墙钟超时独立于输出；调用方异常/中断也清理命令树。"""
    if timeout <= 0:
        raise ValueError("命令超时必须为正")
    options = ({"creationflags": subprocess.CREATE_NEW_PROCESS_GROUP} if os.name == "nt"
               else {"start_new_session": True})
    launch = ([sys.executable, str(Path(__file__).with_name("process_worker.py")), *command]
              if os.name == "nt" else command)
    process = subprocess.Popen(launch, cwd=cwd, stdout=subprocess.PIPE,
                               stdin=subprocess.PIPE if os.name == "nt" else subprocess.DEVNULL,
                               stderr=subprocess.STDOUT, text=True, encoding="utf-8",
                               errors="replace", **options)
    try:
        if os.name == "nt":
            from windows_job import ProcessJob
            process.validation_job = ProcessJob(process)
            process.stdin.write("start\n")
            process.stdin.close()
    except BaseException:
        stop_tree(process)
        process.stdout.close()
        process.wait(timeout=20)
        raise
    expired = threading.Event()
    def expire():
        expired.set()
        stop_tree(process)
    timer = threading.Timer(timeout, expire)
    timer.start()
    try:
        yield process
        if expired.is_set():
            raise RuntimeError(f"命令超时（{timeout}s），已终止进程树")
    except BaseException:
        stop_tree(process)
        raise
    finally:
        timer.cancel()
        timer.join()
        if getattr(process, "validation_job", None) is not None:
            process.validation_job.close()
        process.stdout.close()
        process.wait(timeout=20)
