"""Windows 启动握手：加入 Job Object 前绝不启动实际命令。"""
import subprocess
import sys


if __name__ == "__main__":
    # stdin 由验证器独占；验证命令本身不接受交互输入。
    if sys.stdin.readline() != "start\n":
        raise SystemExit(2)
    raise SystemExit(subprocess.call(sys.argv[1:], stdin=subprocess.DEVNULL))
