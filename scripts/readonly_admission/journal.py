"""先独占登记，再逐事件排他落盘；任何写入错误直接向上传播。"""
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import traceback


class Journal:
    def __init__(self, directory, identity):
        self.directory = Path(directory)
        self.directory.mkdir(mode=0o700, parents=False, exist_ok=False)
        self.sequence = 0
        self.event('start', dict(identity=identity,
                                recorded_at_utc=datetime.now(timezone.utc).isoformat()))

    def raw(self, name, data):
        path = self.directory / name
        with path.open('xb') as stream:
            if stream.write(data) != len(data):
                raise OSError('short private evidence write')
            stream.flush()
            os.fsync(stream.fileno())
        if path.read_bytes() != data:
            raise OSError('private evidence readback mismatch')
        return dict(file=name, sha256=hashlib.sha256(data).hexdigest(), bytes=len(data))

    def event(self, kind, value):
        self.sequence += 1
        # kind只由内部固定调用点提供，不能从柜台文本生成路径。
        name = f'{self.sequence:03d}-{kind}.json'
        raw = (json.dumps(value, ensure_ascii=True, allow_nan=False, indent=2)+'\n').encode('utf-8')
        return self.raw(name, raw)

    def cause(self, error):
        """保存原始异常链而不捕获局部变量；此文件不得直接向用户输出。"""
        self.sequence += 1
        text = ''.join(traceback.format_exception(type(error), error, error.__traceback__))
        return self.raw(f'{self.sequence:03d}-private-error.txt', text.encode('utf-8', errors='backslashreplace'))
