package main

import (
	"errors"
	"go/ast"
	"strings"
	"testing"

	def "gitee.com/haifengat/goctp/ctpdefine"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/ctp"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/probe"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/safety"
)

// posRec 造一条持仓记录。
func posRec(ex, inst string, dir def.TThostFtdcPosiDirectionType, date byte, position, today, ydField int) *def.CThostFtdcInvestorPositionField {
	p := &def.CThostFtdcInvestorPositionField{
		PosiDirection: dir,
		PositionDate:  def.TThostFtdcPositionDateType(date),
		Position:      def.TThostFtdcVolumeType(position),
		TodayPosition: def.TThostFtdcVolumeType(today),
		YdPosition:    def.TThostFtdcVolumeType(ydField),
	}
	copy(p.ExchangeID[:], ex)
	copy(p.InstrumentID[:], inst)
	return p
}

// flattenAccount 是一个「多条腿同时有敞口」的账户 —— 正是 `-all` 最会被敲的场景。
func flattenAccount() map[string]*def.CThostFtdcInvestorPositionField {
	var L, S def.TThostFtdcPosiDirectionType = def.THOST_FTDC_PD_Long, def.THOST_FTDC_PD_Short
	var T, H byte = def.THOST_FTDC_PSD_Today, def.THOST_FTDC_PSD_History
	return map[string]*def.CThostFtdcInvestorPositionField{
		"k1": posRec("SHFE", "rb2701", L, T, 1, 1, 0),
		"k2": posRec("SHFE", "rb2701", L, H, 2, 0, 2),
		"k3": posRec("DCE", "m2701", L, T, 2, 1, 1), // 合成一条：今 1 昨 1（种子）
		"k4": posRec("SHFE", "ag2702", S, T, 1, 1, 0),
		"k5": posRec("INE", "bc2611", L, T, 1, 1, 0),
		"k6": posRec("DCE", "i2701", L, T, 0, 0, 0),   // 平过了的空记录
		"k7": posRec("SHFE", "cu2611", L, H, 0, 0, 1), // ⚠️ 昨仓已平而 YdPosition 仍是日初值
		"k8": posRec("SHFE", "ag2702", L, T, 1, 1, 1), // ⚠️ 合成一条、昨仓已平而 YdPosition 仍是 1 ⇒ 只有今 1
	}
}

// TestFlattenPlanIsSortedAndSplit 钉住 ctp-flatten 的计划**确定、分腿、不信 YdPosition**。
//
// ⚠️ 顺序断言用 8 条记录的 map 喂：若实现退回「按 map 遍历发单」，
// 这张表偶然排对的概率是八千分之一量级，而不是「多跑几次总会过」。
func TestFlattenPlanIsSortedAndSplit(t *testing.T) {
	want := []string{
		"DCE.m2701 多头 今仓 1 手",
		"DCE.m2701 多头 昨仓 1 手",
		"INE.bc2611 多头 今仓 1 手",
		"SHFE.ag2702 多头 今仓 1 手",
		"SHFE.ag2702 空头 今仓 1 手",
		"SHFE.rb2701 多头 今仓 1 手",
		"SHFE.rb2701 多头 昨仓 2 手",
	}
	for round := 0; round < 20; round++ {
		legs, skipped := flattenPlan(flattenAccount(), "")
		got := make([]string, len(legs))
		for i, l := range legs {
			got[i] = l.String()
		}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("第 %d 轮计划不对：\n得到\n  %s\n要\n  %s", round, strings.Join(got, "\n  "), strings.Join(want, "\n  "))
		}
		if len(skipped) != 0 {
			t.Fatalf("⚠️ 全平时不该跳过任何合约，得到 %v", skipped)
		}
	}
	// ⚠️ 开平标志与委托方向：平今/平昨、平多卖/平空买。
	legs, _ := flattenPlan(flattenAccount(), "")
	for _, l := range legs {
		wantOff := def.TThostFtdcOffsetFlagType(def.THOST_FTDC_OF_CloseToday)
		if l.Name == "昨仓" {
			wantOff = def.THOST_FTDC_OF_CloseYesterday
		}
		wantDir := def.TThostFtdcDirectionType(def.THOST_FTDC_D_Sell)
		if l.Side == safety.Short {
			wantDir = def.THOST_FTDC_D_Buy
		}
		if l.Offset != wantOff || l.Dir != wantDir {
			t.Errorf("⚠️ %s：开平 %q 方向 %q，要 %q / %q", l, string(l.Offset), string(l.Dir), string(wantOff), string(wantDir))
		}
	}
	// -symbol 作用域
	legs, skipped := flattenPlan(flattenAccount(), "SHFE.rb2701")
	if len(legs) != 2 || legs[0].Name != "今仓" || legs[1].Name != "昨仓" {
		t.Errorf("-symbol SHFE.rb2701 的计划不对：%v", legs)
	}
	if strings.Join(skipped, ",") != "DCE.m2701,INE.bc2611,SHFE.ag2702" {
		t.Errorf("-symbol 跳过的合约不对（要去重、排序，且不含空记录）：%v", skipped)
	}
}

// TestFlattenVerdictNamesEverything 钉住收尾判定：**全部列出**、**被保护的腿留着是预期**。
func TestFlattenVerdictNamesEverything(t *testing.T) {
	seedYd := flattenLeg{Symbol: "DCE.m2701", Side: safety.Long, Name: "昨仓", Volume: 1}
	rb := flattenLeg{Symbol: "SHFE.rb2701", Side: safety.Long, Name: "今仓", Volume: 1}
	agShort := flattenLeg{Symbol: "SHFE.ag2702", Side: safety.Short, Name: "今仓", Volume: 1}
	agLong := flattenLeg{Symbol: "SHFE.ag2702", Side: safety.Long, Name: "今仓", Volume: 1}
	seedToday2 := flattenLeg{Symbol: "DCE.m2701", Side: safety.Long, Name: "今仓", Volume: 2}
	seedToday5 := flattenLeg{Symbol: "DCE.m2701", Side: safety.Long, Name: "今仓", Volume: 5}
	bcUndeclared := flattenLeg{Symbol: "INE.bc2611", Side: safety.Long, Name: "今仓", Volume: 1}
	cases := []struct {
		name      string
		tally     flattenTally
		remaining []flattenLeg
		ok        bool
		has       []string
	}{
		{"全平干净", flattenTally{Closed: []flattenLeg{rb}}, nil, true, nil},
		{"种子被保护跳过、重查仍在 ⇒ 预期", flattenTally{Closed: []flattenLeg{rb}, Protected: []flattenLeg{seedYd}},
			[]flattenLeg{seedYd}, true, nil},
		{"⚠️ 两笔真没平掉 ⇒ 两笔都要点名，不是只点第一条",
			flattenTally{Failed: []string{"SHFE.rb2701 今仓：拒", "SHFE.ag2702 空头：拒"}}, []flattenLeg{rb, agShort}, false,
			[]string{"SHFE.rb2701 今仓：拒", "SHFE.ag2702 空头：拒", "没平掉 2 笔"}},
		{"⚠️ 报成交而重查仍在、不受保护 ⇒ 报错", flattenTally{Closed: []flattenLeg{rb}}, []flattenLeg{rb}, false,
			[]string{"平完重查仍在", "SHFE.rb2701"}},
		{"⚠️ 保护按合约+方向认：空头被保护，不替同合约的多头兜底",
			flattenTally{Protected: []flattenLeg{agShort}}, []flattenLeg{agShort, agLong}, false,
			[]string{"SHFE.ag2702 多头"}},
		{"真没平掉 + 被保护跳过 ⇒ 报错里两样都说", flattenTally{Failed: []string{"INE.bc2611 今仓：拒"}, Protected: []flattenLeg{seedYd}},
			[]flattenLeg{seedYd}, false, []string{"INE.bc2611", "按保护跳过 1 条", "DCE.m2701"}},
		// ⚠️ 下面三格是 20260913 评审第二轮实测出来的：上一版全部判「平干净了」、正常退出。
		{"⚠️ 种子 + #4 收尾失败遗留的今仓 2 手 ⇒ 多出 2 手，报错",
			flattenTally{Protected: []flattenLeg{seedToday2, seedYd}}, []flattenLeg{seedToday2, seedYd}, false,
			[]string{"共 3 手，保护只覆盖 1 手", "多出 2 手", "ctp-closeorder -cleanup"}},
		{"⚠️ 种子不在、账上 5 手今天开的 ⇒ 多出 4 手，报错",
			flattenTally{Protected: []flattenLeg{seedToday5}}, []flattenLeg{seedToday5}, false,
			[]string{"共 5 手", "多出 4 手"}},
		{"保护未声明手数（0）⇒ 一手都不当预期",
			flattenTally{Protected: []flattenLeg{bcUndeclared}}, []flattenLeg{bcUndeclared}, false,
			[]string{"保护只覆盖 0 手", "多出 1 手"}},
		{"恰好等于保护手数 ⇒ 预期",
			flattenTally{Protected: []flattenLeg{agShort}}, []flattenLeg{agShort}, true, nil},
	}
	allowed := func(symbol string, side safety.Side) int {
		switch {
		case symbol == "DCE.m2701" && side == safety.Long:
			return 1
		case symbol == "SHFE.ag2702" && side == safety.Short:
			return 1
		}
		return 0
	}
	for _, c := range cases {
		err := flattenVerdict(c.tally, c.remaining, allowed)
		if (err == nil) != c.ok {
			t.Errorf("%s：ok=%v，要 %v（%v）", c.name, err == nil, c.ok, err)
			continue
		}
		for _, h := range c.has {
			if !strings.Contains(err.Error(), h) {
				t.Errorf("%s：报错里没有 %q：%v", c.name, h, err)
			}
		}
	}
}

// TestFlattenLoopFinishesAndChecksFirst 钉住 runCTPFlatten 的发单循环**不提前返回**、
// **先 Check 再 Insert**、并用 `errors.Is(…, safety.ErrProtectedLeg)` 分出按保护跳过。
//
// ⚠️ 这三条都只能从源码读：真跑一次要连柜台、要账上有受保护腿 ——
// 方法论 93：**验证判据 = 执行危险动作**的门，先抽出来再验。
func TestFlattenLoopFinishesAndChecksFirst(t *testing.T) {
	_, files := parsePkgMain(t)
	fn := findFunc(files["main.go"], "runCTPFlatten")
	if fn == nil {
		t.Fatal("⚠️ 找不到 runCTPFlatten")
	}
	var loop *ast.RangeStmt
	ast.Inspect(fn, func(n ast.Node) bool {
		if r, ok := n.(*ast.RangeStmt); ok {
			if id, ok := r.X.(*ast.Ident); ok && id.Name == "plan" {
				loop = r
			}
		}
		return true
	})
	if loop == nil {
		t.Fatal("⚠️ runCTPFlatten 里找不到 `range plan` —— 发单不再经 flattenPlan，下面在空集上跑")
	}
	returns, checkAt, insertAt, isProtected := 0, -1, -1, false
	ast.Inspect(loop.Body, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.FuncLit:
			return false // 闭包里的 return 不是循环的 return
		case *ast.ReturnStmt:
			returns++
		case *ast.CallExpr:
			// 委托经 insertOrCancel 发出（没成交就撤，评审 20260922）—— 与直接 c.Insert 同算「下单」。
			if id, ok := v.Fun.(*ast.Ident); ok && id.Name == "insertOrCancel" {
				insertAt = int(v.Pos())
			}
			if sel, ok := v.Fun.(*ast.SelectorExpr); ok {
				switch sel.Sel.Name {
				case "Check":
					checkAt = int(v.Pos())
				case "Insert":
					insertAt = int(v.Pos())
				case "Is":
					if len(v.Args) == 2 {
						if s, ok := v.Args[1].(*ast.SelectorExpr); ok && s.Sel.Name == "ErrProtectedLeg" {
							isProtected = true
						}
					}
				}
			}
		}
		return true
	})
	if returns != 0 {
		t.Errorf("⚠️ 发单循环里有 %d 个 return —— 撞到受保护腿或一笔没平掉就退出，"+
			"排在后面的仓**不平也不报**（20260913 评审第一节）", returns)
	}
	if checkAt < 0 || insertAt < 0 || checkAt > insertAt {
		t.Errorf("⚠️ 循环里 Check 在 %d、Insert 在 %d —— 要**先 Check**，否则按保护跳过的腿会被当成没平掉", checkAt, insertAt)
	}
	if !isProtected {
		t.Error("⚠️ 循环里没有 errors.Is(…, safety.ErrProtectedLeg) —— 分不出「按保护跳过」与「真没平掉」")
	}
	// ⚠️ 收尾的 allowed 必须按**柜台报的当天交易日**取（评审第三轮）：传空串会退化成「日期未知取最小」，
	// 传写死的日期则在种子表换日之后悄悄失效。
	dayWired := false
	ast.Inspect(fn, func(n ast.Node) bool {
		c, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := c.Fun.(*ast.Ident); ok && id.Name == "protectedVolume" && len(c.Args) == 2 {
			if call, ok := c.Args[1].(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "TradingDay" {
					dayWired = true
				}
			}
		}
		return true
	})
	if !dayWired {
		t.Error("⚠️ runCTPFlatten 没有把 c.TradingDay() 传给 protectedVolume —— 保护手数不按当天取")
	}
}

// TestFlattenPlanMeetsProductionValve 把计划里的每一笔过一遍**生产的** ctpValve：
// 恰好**种子所在的「合约 + 方向」上**那两条被判成 ErrProtectedLeg，其余一笔都不是。
//
// ⚠️ 两条里**至多一条是种子**（种子只有 1 手）：受保护腿不分今昨，同一合约方向上的今仓也一起被拦。
// 上一版这里写的是「恰好种子那两条」—— 评审指出种子只有 1 手。
// 被拦下的今仓在收尾时由 flattenVerdict 按 Volume 判「多出」，见 TestFlattenVerdictNamesEverything。
//
// ⚠️ 未连接的客户端 TradingDay() 是空串 ⇒ 按「还不知道今天是哪天」照拦，与柜台截面到达之前一致。
func TestFlattenPlanMeetsProductionValve(t *testing.T) {
	c := ctp.New(ctp.Credentials{BrokerID: "9999", UserID: "x"}, nil)
	c.Valve = ctpValve(probe.Env{AllowOrder: true, MaxVolume: 1})
	legs, _ := flattenPlan(flattenAccount(), "")
	var protected []string
	for _, l := range legs {
		ex, inst := ctp.SplitSymbol(l.Symbol)
		err := c.Check(ctp.OrderReq{Exchange: ex, Instrument: inst, Direction: l.Dir,
			Offset: l.Offset, Volume: l.Volume, LimitPrice: 3000})
		if errors.Is(err, safety.ErrProtectedLeg) {
			protected = append(protected, l.String())
		} else if err != nil && l.Volume <= 1 {
			t.Errorf("⚠️ %s 被拒了、却不是按保护：%v", l, err)
		}
	}
	if got := strings.Join(protected, "；"); got != "DCE.m2701 多头 今仓 1 手；DCE.m2701 多头 昨仓 1 手" {
		t.Errorf("⚠️ 按保护判出的腿是 [%s]，要恰好 DCE.m2701 多头那两条 —— 保护表或 ErrProtectedLeg 的接线不对", got)
	}
}

// TestProtectedVolumeIsPerTradingDay 钉住保护覆盖手数**按当天那一条**取：不相加、不跨日取最大、日期未知取最小。
func TestProtectedVolumeIsPerTradingDay(t *testing.T) {
	legs := []safety.ProtectedLeg{
		{Symbol: "DCE.m2701", Side: safety.Long, TradingDay: "20260914", Volume: 1},
		{Symbol: "DCE.m2701", Side: safety.Long, TradingDay: "20260915", Volume: 1},
		// ⚠️ 同一天重复挂了一条（例如重种时忘了删旧行）：覆盖手数仍是 1，不相加
		{Symbol: "DCE.m2701", Side: safety.Long, TradingDay: "20260915", Volume: 1, Why: "重复行"},
		{Symbol: "DCE.m2701", Side: safety.Long, TradingDay: "20260916", Volume: 2},
		{Symbol: "DCE.m2701", Side: safety.Short, TradingDay: "20260915", Volume: 3},
	}
	for _, c := range []struct {
		name, day string
		side      safety.Side
		want      int
	}{
		{"同一天两条（重复行）⇒ 当天 1，不是相加的 2", "20260915", safety.Long, 1},
		{"⚠️ 14 日：表里别的日子有 2 手，也只看 14 日那条（评审第三轮实测）", "20260914", safety.Long, 1},
		{"16 日那条自己是 2", "20260916", safety.Long, 2},
		{"表里没有这一天 ⇒ 0（阀门也不会拦，走不到这里；走到了就一手都不当预期）", "20260917", safety.Long, 0},
		{"日期未知 ⇒ 取最小，与阀门「不知道就照拦」同方向", "", safety.Long, 1},
		{"方向分开", "20260915", safety.Short, 3},
	} {
		if got := protectedVolume(legs, c.day)("DCE.m2701", c.side); got != c.want {
			t.Errorf("%s：得到 %d，要 %d", c.name, got, c.want)
		}
	}
	if got := protectedVolume(legs, "20260915")("SHFE.rb2701", safety.Long); got != 0 {
		t.Errorf("表里没有的合约得到 %d，要 0", got)
	}
}
