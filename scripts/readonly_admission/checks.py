"""冻结登记的有限准入检查；配置由经哈希核验的新登记封装提供，无CLI覆盖。"""
from datetime import datetime, timedelta
import hashlib
import json
import os
from pathlib import Path
import re
from verify_evidence import verify_bundle


def digest(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


class Checks:
    """cfg是内部冻结登记，不能拿.env或柜台自由文本构造配置。"""
    def __init__(self, cfg):
        self.cfg = cfg
        self.root = Path(cfg['root'])
        self.ready = None

    def git(self, ctx, *args, root=None, text=True):
        return ctx.command(['git', '-c', 'http.version=HTTP/1.1', *args],
                           root or self.root, text=text)

    def pinned(self, ctx, path, expected):
        ctx.require(re.fullmatch(r'[0-9a-f]{64}', expected) is not None, 'invalid pinned digest')
        ctx.require(digest(path) == expected, 'pinned content changed')

    def ready_check(self, ctx):
        c = self.cfg
        self.pinned(ctx, c['ready'], c['ready_sha256'])
        self.ready = json.loads(Path(c['ready']).read_bytes())

    def receipt_check(self, ctx):
        c = self.cfg
        self.pinned(ctx, c['receipt'], c['receipt_sha256'])
        receipt = json.loads(Path(c['receipt']).read_bytes())
        ctx.require(receipt['ready_sha256'] == c['ready_sha256'], 'receipt binding')
        ctx.require(receipt['verified_at_bjt'] == self.ready['verified_at_bjt'], 'receipt verified time')
        verified = datetime.fromisoformat(self.ready['verified_at_bjt'])
        recorded = datetime.fromisoformat(receipt['recorded_at_bjt'])
        deadline = datetime.fromisoformat(c['ready_deadline_bjt'])
        ctx.require(all(at.utcoffset() == timedelta(hours=8) for at in [verified, recorded, deadline]), 'unqualified time zone')
        ctx.require(verified <= recorded < deadline, 'late or inverted readiness')

    def source_check(self, ctx):
        c = self.cfg
        ctx.require(all(re.fullmatch(r'[0-9a-f]{40}', c[key]) for key in ['source', 'main']), 'invalid source identity')
        ctx.require(self.ready['source_commit'] == c['source'] and self.ready['published_main'] == c['main'], 'source binding')
        expected = [c['exe'], 'ctp-params', '-timeout', '10s', '-env', str(self.root / '.env')]
        ctx.require(c['argv'] == expected and self.ready['argv'] == expected, 'fixed read-only argv')

    def local_check(self, ctx):
        c = self.cfg
        ctx.require(self.git(ctx, 'status', '--porcelain') == '', 'dirty source')
        for ref in ['HEAD', 'main', 'dev', 'origin/main', 'origin/dev']:
            ctx.require(self.git(ctx, 'rev-parse', ref) == c['main'], 'local ref changed')
        self.git(ctx, 'merge-base', '--is-ancestor', c['source'], c['main'])
        changes = self.git(ctx, 'diff', '--name-only', c['source'], c['main']).splitlines()
        ctx.require(all(name.startswith('docs/') for name in changes), 'runtime source drift')

    def remote_check(self, ctx):
        lines = self.git(ctx, 'ls-remote', 'origin', 'refs/heads/main', 'refs/heads/dev').splitlines()
        expected = {f"{self.cfg['main']}\trefs/heads/{name}" for name in ['main', 'dev']}
        ctx.require(len(lines) == 2 and set(lines) == expected, 'unknown or changed remote refs')

    def binary_check(self, ctx):
        c = self.cfg
        ctx.require(self.ready['binary']['path'] == c['exe'] and self.ready['binary']['sha256'] == c['binary_sha256'], 'binary declaration')
        self.pinned(ctx, c['exe'], c['binary_sha256'])
        build = ctx.command([c['go'], 'version', '-m', c['exe']], self.root)
        ctx.require('vcs.revision='+c['source'] in build and 'vcs.modified=false' in build and 'GOEXPERIMENT=nojsonv2' in build, 'actual build identity')

    def certificate_check(self, ctx):
        c = self.cfg
        self.pinned(ctx, c['certificate'], c['certificate_sha256'])
        declared = self.ready['certificate']
        ctx.require(declared == dict(path=c['certificate'], sha256=c['certificate_sha256']), 'certificate declaration')
        cert = json.loads(Path(c['certificate']).read_bytes())
        ctx.require(cert['source_commit'] == c['source'], 'certificate source')
        ctx.require(self.git(ctx, 'rev-parse', c['source']+'^{tree}') == cert['source_tree'], 'certificate tree')

    def plan_check(self, ctx):
        c = self.cfg
        self.pinned(ctx, c['plan'], c['plan_sha256'])
        ctx.require(self.ready['plan'] == dict(path=c['plan'], sha256=c['plan_sha256']), 'plan declaration')

    def dll_check(self, ctx):
        c = self.cfg
        if os.environ.get('CTP_DLL_DIR'):
            directory = Path(os.environ['CTP_DLL_DIR'])
        elif os.environ.get('GOMODCACHE'):
            directory = Path(os.environ['GOMODCACHE']) / 'gitee.com/haifengat/goctp@v1.10.17/win'
        else:
            cache = ctx.command([c['go'], 'env', 'GOMODCACHE'], self.root)
            directory = Path(cache) / 'gitee.com/haifengat/goctp@v1.10.17/win'
        ctx.require(self.ready['dlls'] == c['dlls'] and len(c['dlls']) == 2, 'dll declarations')
        for item in c['dlls']:
            actual = directory / Path(item['path']).name
            ctx.require(os.path.normcase(str(actual.resolve())) == os.path.normcase(str(Path(item['path']).resolve())), 'actual dll path')
            self.pinned(ctx, actual, item['sha256'])

    def evidence_check(self, ctx):
        c = self.cfg
        cert = json.loads(Path(c['certificate']).read_bytes())
        registry = self.git(ctx, 'show', c['source']+':tools/breakcheck/breaks.json', text=False)
        summary_path = Path(cert['summary']['path'])
        summary_raw = summary_path.read_bytes()
        report = (summary_path.parent / 'mutations.json').read_bytes()
        counts = verify_bundle(cert, registry, report, summary_raw)
        ctx.require(counts['total'] == c['mutation_total'], 'full mutation coverage')
        # 不能只信历史“已还原”声明，独立重算完整副本的实际内容与索引。
        proof = Path(c['proof_root'])
        files = self.git(ctx, 'ls-files', '-z', root=proof, text=False).split(b'\0')
        fingerprint = hashlib.sha256()
        for raw in files:
            if raw:
                path = proof / os.fsdecode(raw)
                content = (os.fsencode(os.readlink(path)) if path.is_symlink()
                           else path.read_bytes() if path.exists() else b'<missing>')
                fingerprint.update(raw+b'\0'+hashlib.sha256(content).digest())
        actual = dict(status=self.git(ctx, 'status', '--porcelain', '--untracked-files=no', root=proof),
                      head=self.git(ctx, 'rev-parse', '--verify', '--quiet', 'HEAD', root=proof),
                      tracked_sha256=fingerprint.hexdigest(),
                      index_sha256=hashlib.sha256(self.git(ctx, 'ls-files', '--stage', '-z', root=proof, text=False)).hexdigest())
        summary = json.loads(summary_raw)
        ctx.require(actual == summary['before'] == summary['after'], 'actual proof tree not restored')
        checked = []
        def visit(node):
            if isinstance(node, dict):
                if isinstance(node.get('path'), str) and isinstance(node.get('sha256'), str):
                    self.pinned(ctx, node['path'], node['sha256'])
                    checked.append(node['path'])
                for value in node.values():
                    visit(value)
            elif isinstance(node, list):
                for value in node:
                    visit(value)
        visit(cert)
        ctx.require(len(checked) >= c['minimum_evidence_files'], 'missing evidence references')
        for name, count in [('regression', 3), ('race', 3), ('fuzz', 5)]:
            s = cert['suites'][name]['summary']
            ctx.require(s['status'] == 'passed' and s['failure'] is None and s['before'] == s['after'] and s['before']['head'] == c['source'] and s['before']['status'] == '' and len(s['completed']) == count, 'incomplete suite')
            for item in s['completed']:
                ctx.require(item['exit_code'] == 0, 'suite command failed')
                ctx.require('-fuzztime=20s' in item['command'] if name == 'fuzz' else item['passed_tests'] > 0 and not item['skipped'], 'skipped or unqualified suite')
        python = Path(cert['python_tests']['path']).read_text(encoding='utf-8-sig')
        ctx.require(f"Ran {c['python_tests']} tests" in python and python.rstrip().endswith('OK'), 'python evidence')
        ci = cert['ci']
        ctx.require(ci['sha'] == c['source'] and ci['conclusion'] == 'success' and {job['name'] for job in ci['jobs']} == {'offline (windows-2022)', 'offline (ubuntu-24.04)'} and len(ci['jobs']) == 2 and all(job['status'] == 'completed' and job['conclusion'] == 'success' for job in ci['jobs']), 'source CI claims')

    def steps(self):
        return [(name, method) for name, method in [
            ('ready', self.ready_check), ('receipt', self.receipt_check),
            ('source', self.source_check), ('local-git', self.local_check),
            ('remote-git', self.remote_check), ('binary', self.binary_check),
            ('certificate', self.certificate_check), ('plan', self.plan_check),
            ('dll', self.dll_check), ('evidence', self.evidence_check)]]
