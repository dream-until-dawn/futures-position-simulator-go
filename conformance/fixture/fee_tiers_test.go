package fixture

import (
	"sort"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// TestFeeRateTiersAreIndistinguishable 把「三档费率在本口子上分不开」
// 从**散文**变成一条会红的断言。
//
// # 它替换掉了什么
//
// kq_facts 18 与 34 都是「盲区声明」：平今费额恒等于开仓、平昨费额也恒等于开仓，
// 于是 fee.pick 里那段「按开平标志选费率档」**选错了也看不出来**。
// 这件事此前只写在注释与 t.Logf 里 —— ⚠️ 而一句写在注释里的盲区，
// 在盲区消失的那天不会有任何动静。
//
// # 判据
//
// 同一交易日、同一合约内，按开平标志分组算**每手费额**：
//
//	三档都相等   盲区仍在 —— 记下来，并说清它让哪句话变得没有判别力
//	出现不相等   ⚠️ **报错**：盲区消失了，去更新 kq_facts 18/34，
//	             并把 fee 的档位对拍真正打开
//
// ⚠️ 第二支「因为变好了而红」是刻意的，与本仓库其它棘轮同形：
// 一个只在变坏时红的守卫，管不住「文档停在旧世界」这一类。
func TestFeeRateTiersAreIndistinguishable(t *testing.T) {
	type key struct {
		day types.TradingDay
		sym string
	}
	// 每手费额 -> 出处，按 (交易日, 合约, 开平标志) 分桶。
	per := map[key]map[types.Offset]map[string][]string{}
	for _, f := range loadAll(t) {
		for _, tr := range f.Trades {
			if tr.Volume <= 0 {
				continue
			}
			k := key{f.TradingDay, tr.Instrument.Native()}
			if per[k] == nil {
				per[k] = map[types.Offset]map[string][]string{}
			}
			if per[k][tr.Offset] == nil {
				per[k][tr.Offset] = map[string][]string{}
			}
			each := tr.Commission.Div(decimal.NewFromInt(int64(tr.Volume)))
			s := each.String()
			per[k][tr.Offset][s] = append(per[k][tr.Offset][s], f.Path+" "+tr.TradeID)
		}
	}

	comparable, pairs := 0, 0
	for k, byOff := range per {
		if len(byOff) < 2 {
			continue // 只有一种开平标志，比不了
		}
		comparable++
		// 同一档内部先要自洽：不自洽说明这一天的基准价变过（换挡窗口，kq_facts 41），
		// ⚠️ 那时跨档比较毫无意义，会把「基准变了」读成「费率不同」。
		mixed := false
		for off, vals := range byOff {
			if len(vals) > 1 {
				mixed = true
				t.Logf("ⓘ %s %s 的 %s 档内部就有 %d 种每手费额 %v —— "+
					"这一组跳过（多半是换挡窗口，kq_facts 41）",
					k.day, k.sym, off, len(vals), sortedKeys(vals))
			}
		}
		if mixed {
			continue
		}
		offs := make([]types.Offset, 0, len(byOff))
		for off := range byOff {
			offs = append(offs, off)
		}
		sort.Slice(offs, func(i, j int) bool { return offs[i] < offs[j] })
		var base string
		for i, off := range offs {
			v := sortedKeys(byOff[off])[0]
			if i == 0 {
				base = v
				continue
			}
			pairs++
			if v != base {
				t.Errorf("⚠️ %s %s：%s 档每手 %s，而 %s 档每手 %s —— "+
					"**三档第一次分得开了**。这不是坏消息，是 kq_facts 18/34 那个盲区"+
					"消失了：去更新它们，并把 fee 的档位对拍真正打开（在此之前"+
					"「平今算对了」这句话的判别力一直是零）",
					k.day, k.sym, offs[0], base, off, v)
			}
		}
	}

	t.Logf("可比较的（交易日, 合约）组 %d 个，跨档比对 %d 次", comparable, pairs)
	// ⚠️ 判别力：没有可比较的组时，上面一次比对都没做，而结论会是「全都相等」。
	if comparable < 2 || pairs < 2 {
		t.Fatalf("⚠️ 只有 %d 个可比较的组、%d 次比对 —— "+
			"「三档分不开」这句话在这么点样本上什么都不说明", comparable, pairs)
	}
	t.Logf("⚠️ 三档在全部语料上仍然分不开 —— 于是 fee 里「按开平标志选档」" +
		"这段代码选错了也不会红。这不是代码没测，是**这个口子测不了它**（simnow_pending#3）")
}

func sortedKeys(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
