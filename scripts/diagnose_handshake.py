"""离线核验已登记日志：python scripts/diagnose_handshake.py --manifest 私有清单 --out 全新报告。"""
import argparse
import json
from pathlib import Path
import sys
from handshake_diagnostics.parsing import EvidenceError
from handshake_diagnostics.reporting import build_report, write_report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--manifest',type=Path,required=True)
    parser.add_argument('--out',type=Path,required=True)
    args = parser.parse_args()
    try:
        manifest = json.loads(args.manifest.read_bytes())
        report = build_report(manifest)
        write_report(args.out,report)
    except EvidenceError as error:
        print('诊断拒绝: '+str(error),file=sys.stderr)
        return 1
    except (OSError,ValueError,TypeError,OverflowError):
        print('诊断拒绝: input-or-output-invalid',file=sys.stderr)
        return 1
    print('离线诊断完成，未连接柜台；根因判定保留未知。')
    return 0


if __name__ == '__main__':
    sys.stdout.reconfigure(encoding='utf-8')
    sys.stderr.reconfigure(encoding='utf-8')
    raise SystemExit(main())
