package main

import (
	"fmt"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/ctp"
)

// tickVerdict 是「柜台报的 tick」与「人手填的 tick」两者之间的判定，**不碰柜台**：纯函数，离线可测。
//
// ⚠️ 三种情形要分开，因为它们的失败方向完全不同：
//
//	柜台报了、人没填    用柜台的 —— 转录这一环整个去掉
//	两个都有且相等      用柜台的，并把「对上了」说出来（人填的那个此刻是多余的，不是错的）
//	两个都有但不等      **不跑**。一个是错的，而这条命令分不出是哪一个 ——
//	                    照跑下去会拿到四个码，其中一些属于另一种拒因，而输出上标着原来的名字
//	柜台没报、人填了    用人填的，但**记成 flag** —— 语料里要看得出这一条的 tick 没被柜台核过
//	两个都没有          不跑
func tickVerdict(counter, flag float64) (tickUsed, string, error) {
	switch {
	case counter > 0 && flag <= 0:
		return tickUsed{Value: counter, Source: tickFromCounter},
			fmt.Sprintf("柜台报的最小变动价位 %g（没填 -tick，直接用它）", counter), nil
	case counter > 0 && flag > 0 && counter == flag:
		return tickUsed{Value: counter, Source: tickFromCounter},
			fmt.Sprintf("柜台报的 %g 与 -tick 填的一致 —— 记成柜台来源（人填的那个此刻是多余的）", counter), nil
	case counter > 0 && flag > 0:
		return tickUsed{}, "", fmt.Errorf("⚠️ **不跑**：柜台报的最小变动价位是 %g，而 -tick 填的是 %g —— "+
			"一个是错的，而本命令分不出是哪一个。照跑下去会拿到四个码，"+
			"其中一些属于**另一种拒因**，而输出上仍标着原来那个名字（20260914 `bc` 就是这样错过一次）",
			counter, flag)
	case flag > 0:
		return tickUsed{Value: flag, Source: tickFromFlag},
			fmt.Sprintf("⚠️ 柜台没报出最小变动价位，用 -tick 填的 %g —— "+
				"语料里这一条记成 %q，它**没有被柜台核过**", flag, tickFromFlag), nil
	default:
		return tickUsed{}, "", fmt.Errorf("⚠️ **不跑**：柜台没报出最小变动价位，-tick 也没填 —— " +
			"价格要按 tick 构造，缺了它这一轮的每一个码都归不到它该归的那一项")
	}
}

// resolveTick 向柜台问一次合约参数，再交给 tickVerdict 判。
//
// ⚠️ 查不到**不当致命**：柜台答不上来时还有 -tick 那条路，
// 而「查询失败」与「这个合约没有 tick」是两回事 —— 前者只是少了一个交叉核对的来源。
func resolveTick(c *ctp.Client, symbol string, flag float64, timeout time.Duration,
	logf func(string, ...any)) (tickUsed, error) {

	var counter float64
	inst, err := c.Instrument(symbol, timeout)
	if err != nil {
		logf("[rej] ⚠️ 向柜台查 %s 的合约参数失败：%v —— 少了一个交叉核对的来源，不致命", symbol, err)
	} else {
		counter = float64(inst.PriceTick)
		logf("[rej] 柜台合约参数 最小变动价位=%g 合约乘数=%d", counter, int(inst.VolumeMultiple))
	}
	tk, why, err := tickVerdict(counter, flag)
	if err != nil {
		return tickUsed{}, err
	}
	logf("[rej] tick ⇒ %g（出处 %s）：%s", tk.Value, tk.Source, why)
	return tk, nil
}
