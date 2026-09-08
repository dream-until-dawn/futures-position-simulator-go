#!/usr/bin/env bash
# 分段续跑 settle-watch，直到指定时刻。
#
# ⚠️ 为什么要分段：一条 WebSocket 连着六小时，断了就静默停在那里，
# 而「停了」与「没有变动」在日志上长得一模一样 —— 那正是要观测的那个时刻最不能出的事。
# 分段之后，每一段的起止都会打进日志，断在哪一段是看得见的。
#
# ⚠️ 每一段都会落一份「起始快照」夹具。那是**刻意**的：
# 段与段之间若发生了变动，两段的起始快照之间就是它的区间 ——
# 而没有起始快照的话，那段变动会整个消失。
#
#   ./watch-until.sh 21:15 5s SHFE.rb2701,DCE.m2701
#
# 用法上的两条：
#   - 时刻用 HH:MM，**当天**；跨零点请自己拆两次
#   - 间隔直接传给 -every
set -u

UNTIL="${1:?要一个结束时刻，形如 21:15}"
EVERY="${2:-5s}"
SYMS="${3:-SHFE.rb2701,DCE.m2701}"
SEG_MIN="${4:-45}"

ENV_FILE="$(cd "$(dirname "$0")/../.." && pwd)/.env"
cd "$(dirname "$0")" || exit 1

end_epoch=$(date -d "today $UNTIL" +%s 2>/dev/null) || {
  echo "看不懂的时刻：$UNTIL" >&2
  exit 2
}

echo "== 分段盯盘：每 $EVERY 采一次，分段 ${SEG_MIN} 分钟，直到 $UNTIL =="
echo "   合约 $SYMS"
echo "   ⚠️ 段与段之间会各落一份起始快照 —— 那是区间的两个端点，不是冗余"

seg=0
while :; do
  now=$(date +%s)
  left=$(( end_epoch - now ))
  if [ "$left" -le 30 ]; then
    echo "=== 到点了（$(date +%H:%M:%S)），共跑了 $seg 段 ==="
    break
  fi
  run_min=$SEG_MIN
  if [ $(( left / 60 )) -lt "$SEG_MIN" ]; then
    run_min=$(( left / 60 + 1 ))
  fi
  seg=$(( seg + 1 ))
  echo "=== 第 $seg 段开始 $(date +%H:%M:%S)，跑 ${run_min} 分钟 ==="
  go run . probe -exp settle-watch \
    -symbols "$SYMS" -every "$EVERY" -timeout "${run_min}m" -env "$ENV_FILE"
  code=$?
  echo "=== 第 $seg 段结束 $(date +%H:%M:%S)，退出码 $code ==="
  # ⚠️ 非零退出**不中断**：一段连不上不代表下一段也连不上，
  # 而中断会让后面的时间完全没有覆盖。退出码照打，谁看日志谁能发现。
  sleep 2
done
