#!/bin/sh
# 状态断言散落面的数法。定义写死在这里，好让「从 N 降到 M」可以被复核。
#
# 一处 = 一行文本，同时满足：
#   1. 出现在 probes.md 之外的文档里（probes.md 是状态的唯一来源，不计）
#   2. 含下列状态词之一：已实测 / 已登录 / 已配置 / 已解除 / 未打通 / 待注册 / 尚未
#   3. 不是在说「待实测」这个证据等级本身（那是分级名，不是状态断言）
cd "$(dirname "$0")/.." || exit 1
grep -rn '已实测\|已登录\|已配置\|已解除\|未打通\|待注册\|尚未' \
     README.md docs/design.md docs/fidelity.md docs/cn-futures-rules.md docs/roadmap.md docs/silent-risks.md \
  | grep -vE '待实测」|「待实测|条待实测|待实测项' \
  | tee /dev/stderr | wc -l
