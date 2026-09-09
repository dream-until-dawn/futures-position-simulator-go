package ctp

// 脱敏用**白名单**，与 `kq` 那侧同一条纪律，但**判据强一档**。
//
// # ⚠️ 为什么这一侧可以做得比 DIFF 那侧强
//
//	DIFF   字段是动态 JSON。只能在**运行时**查「有没有没见过的字段」——
//	       而**样本里没出现过的字段永远不会被查到**
//	CTP    字段是 Go 结构体。反射能在**测试时穷举**，
//	       于是「有字段没被决定过」变成一条**跑测试就会红**的断言
//
// 差别是实质性的：DIFF 那侧的「未分类字段 0 个」说的是
// 「这一份样本里没有没见过的」，CTP 这侧说的是「这个结构体里一个都没漏」。
//
// ⚠️ 所以这里的规矩是：**每个字段都要有一条决定**，keep 或 drop，
// 二者都要写理由。少一个字段 → `TestFieldDecisionsAreComplete` 红。
//
// # ⚠️ 不许照抄 kq 的白名单
//
// `kq` 的 accountKeep / positionKeep 按**天勤 DIFF 的字段名**列。
// CTP 的字段名一个都不一样。照抄会得到一张看起来很完整、
// 实际一个键都对不上的表 —— 于是脱敏后的夹具**空空如也**，
// 而空夹具在下游是「跳过」，不是「报错」。

// decision 是对一个字段的处置。
type decision struct {
	// Keep 为真表示这个字段可以进夹具。
	Keep bool
	// Why 是理由。⚠️ 空串不许：一条没有理由的处置，
	// 与「随手写的」分不开，而白名单是要交给评审逐键核对的东西。
	Why string
}

func keep(why string) decision { return decision{Keep: true, Why: why} }
func drop(why string) decision { return decision{Keep: false, Why: why} }

// 几条共用的理由。写成常量是为了让「同一类」在评审时一眼可见 ——
// 而不同类的字段**不许**共用一条理由。
const (
	whyMoney    = "资金金额，无标识性"
	whyVolume   = "手数，无标识性"
	whyPrice    = "价格，无标识性"
	whyIdent    = "⚠️ **标识性**：能定位到具体的人或账户，绝不入库"
	whyOption   = "期权相关，本库明确不建模（fidelity.md 第 5 节）"
	whyCombo    = "组合持仓，本库明确不建模"
	whySpecProd = "特殊产品（特殊法人）专用，本账户恒为零，且与本库的核算无关"
)

// accountFields 是 CThostFtdcTradingAccountField 每个字段的处置。
var accountFields = map[string]decision{
	"BrokerID":  drop(whyIdent),
	"AccountID": drop(whyIdent),

	"PreMortgage":  keep(whyMoney),
	"PreCredit":    keep(whyMoney),
	"PreDeposit":   keep(whyMoney),
	"PreBalance":   keep("上日结存 —— 逐日盯市的起点，account 包的核心输入"),
	"PreMargin":    keep(whyMoney),
	"InterestBase": keep(whyMoney),
	"Interest":     keep(whyMoney),
	"Deposit":      keep("入金 —— static_balance 恒等式的一项（kq_facts 10）"),
	"Withdraw":     keep("出金 —— ⚠️ 该项**从未参与过运算**，见 §13 第 12 条"),

	"FrozenMargin":     keep("挂单冻结的保证金 —— 与 order.Frozen 对拍"),
	"FrozenCash":       keep(whyOption),
	"FrozenCommission": keep("挂单冻结的手续费 —— 与 order.Frozen 对拍"),
	"CurrMargin":       keep("当前占用保证金 —— margin 包的对拍目标"),
	"CashIn":           keep(whyMoney),
	"Commission":       keep("手续费 —— fee 包的对拍目标"),
	"CloseProfit":      keep("平仓盈亏 —— ⚠️ 这里只有一个，两套口径在**持仓**结构体里"),
	"PositionProfit":   keep("持仓盈亏 —— pnl 包的对拍目标"),
	"Balance":          keep("结存 —— 三条恒等式之一（kq_facts 6）"),
	"Available":        keep("可用 —— 三条恒等式之一"),
	"WithdrawQuota":    keep(whyMoney),
	"Reserve":          keep(whyMoney),

	"TradingDay":   keep("交易日 —— 夹具的主键之一，不落它的截面拒绝入库"),
	"SettlementID": keep("结算编号 —— 判断这份截面在结算前还是结算后"),

	"Credit":                 keep(whyMoney),
	"Mortgage":               keep(whyMoney),
	"ExchangeMargin":         keep("**交易所**口径保证金 —— ⚠️ 与 CurrMargin（公司口径）的差正是 §13 第 14 条"),
	"DeliveryMargin":         keep(whyMoney),
	"ExchangeDeliveryMargin": keep(whyMoney),
	"ReserveBalance":         keep(whyMoney),
	"CurrencyID":             keep("币种 —— 本库仅 CNY，取到别的要当场报错"),

	"PreFundMortgageIn":     keep(whyMoney),
	"PreFundMortgageOut":    keep(whyMoney),
	"FundMortgageIn":        keep(whyMoney),
	"FundMortgageOut":       keep(whyMoney),
	"FundMortgageAvailable": keep(whyMoney),
	"MortgageableFund":      keep(whyMoney),

	"SpecProductMargin":              keep(whySpecProd),
	"SpecProductFrozenMargin":        keep(whySpecProd),
	"SpecProductCommission":          keep(whySpecProd),
	"SpecProductFrozenCommission":    keep(whySpecProd),
	"SpecProductPositionProfit":      keep(whySpecProd),
	"SpecProductCloseProfit":         keep(whySpecProd),
	"SpecProductPositionProfitByAlg": keep(whySpecProd),
	"SpecProductExchangeMargin":      keep(whySpecProd),

	"BizType":    keep("业务类型 —— 期货/期权，用来确认这份截面确实是期货"),
	"FrozenSwap": keep(whyMoney),
	"RemainSwap": keep(whyMoney),
}

// positionFields 是 CThostFtdcInvestorPositionField 每个字段的处置。
var positionFields = map[string]decision{
	"reserve1":     drop("⚠️ 保留字段，且在 goctp 里是**未导出**的（小写），反射读不到值"),
	"BrokerID":     drop(whyIdent),
	"InvestorID":   drop(whyIdent),
	"InvestUnitID": drop(whyIdent),

	"PosiDirection": keep("持仓多空 —— 本库 Direction 的对拍目标"),
	"HedgeFlag":     keep("投机套保 —— 本库仅投机，取到别的要当场报错"),
	"PositionDate":  keep("⚠️ **今仓/昨仓** —— 本项目最核心那对区分的柜台侧表达"),
	"YdPosition":    keep("昨仓量"),
	"Position":      keep(whyVolume),
	"TodayPosition": keep("今仓量"),

	"LongFrozen":        keep("多头冻结手数"),
	"ShortFrozen":       keep("空头冻结手数"),
	"LongFrozenAmount":  keep(whyMoney),
	"ShortFrozenAmount": keep(whyMoney),

	"OpenVolume":  keep(whyVolume),
	"CloseVolume": keep(whyVolume),
	"OpenAmount":  keep(whyMoney),
	"CloseAmount": keep(whyMoney),

	"PositionCost":       keep("持仓成本 —— position 包的对拍目标"),
	"OpenCost":           keep("开仓成本 —— ⚠️ 部分平仓时怎么冲减是 simnow_pending#10"),
	"PositionCostOffset": keep(whyMoney),
	"PreMargin":          keep(whyMoney),
	"UseMargin":          keep("占用保证金 —— margin 包的对拍目标"),
	"FrozenMargin":       keep(whyMoney),
	"FrozenCash":         keep(whyOption),
	"FrozenCommission":   keep(whyMoney),
	"CashIn":             keep(whyMoney),
	"Commission":         keep(whyMoney),
	"ExchangeMargin":     keep("**交易所**口径保证金 —— 与 UseMargin 的差是 §13 第 14 条"),

	"CloseProfit":        keep("平仓盈亏（合计）"),
	"CloseProfitByDate":  keep("⚠️ **逐日盯市**口径的平仓盈亏 —— simnow_pending#2 的一半"),
	"CloseProfitByTrade": keep("⚠️ **逐笔对冲**口径的平仓盈亏 —— 另一半。快期只给一个数，这里给两个"),
	"PositionProfit":     keep("持仓盈亏"),

	"PreSettlementPrice": keep("昨结算价 —— 逐日盯市基线"),
	"SettlementPrice":    keep("今结算价"),
	"TradingDay":         keep("交易日 —— 夹具主键之一"),
	"SettlementID":       keep("结算编号"),

	"MarginRateByMoney":  keep("按额保证金率 —— refdata 的对拍目标"),
	"MarginRateByVolume": keep("按手保证金率"),

	"CombPosition":    keep(whyCombo),
	"CombLongFrozen":  keep(whyCombo),
	"CombShortFrozen": keep(whyCombo),

	"StrikeFrozen":       keep(whyOption),
	"StrikeFrozenAmount": keep(whyOption),
	"AbandonFrozen":      keep(whyOption),
	"YdStrikeFrozen":     keep(whyOption),

	"TasPosition":     keep("TAS（结算价交易）持仓，本库不建模"),
	"TasPositionCost": keep("TAS 持仓成本，本库不建模"),

	"ExchangeID":   keep("交易所代码 —— 定位**合约**，不是定位人"),
	"InstrumentID": keep("合约代码 —— 同上"),
}

// quoteFields 是**行情快照**的全部字段决定（20260910 新增）。
//
// ⚠️ 这里刻意不写「共 N 个」：我第一版数出 44，而真实是 46 ——
// 我的正则只匹配大写开头的字段名，漏掉了两个小写的 `reserve`。
// **是 TestFieldDecisionsAreComplete 当场抓住的**，而它抓的正是
// 「你以为你穷举了」这件事。⇒ 数由反射给，不由人给。
//
// # ⚠️ 它为什么必须存在
//
// probes.md §6.9 声明过一个盲区：「`PositionProfit` 与今结算价严格对上，
// 但夹具里**根本没有最新价字段**，所以『基准是今结算价』与『基准是最新价
// 而此刻两者相等』分不开。」⚠️ **而声明一个盲区不等于关掉它。**
// 20260910 评审指出：次日那份实验清单里**没有一件抓行情**，
// 于是那个盲区会原样留到后天 —— 而**能力早就接好了**（`MarketData` 一直在读
// `LastPrice`），缺的只是落盘时把它一起写进去。
//
// # ⚠️ 盘口刻意全丢
//
// 五档买卖价量共 20 个字段一律 drop。理由不是「用不到」，是
// **本库明确不做盘口**（fidelity.md：默认下单按 100% 全量成交建模）——
// 收下一份自己不建模的数据，会让后来的人以为它是有支撑的。
var quoteFields = map[string]decision{
	// —— 身份：⚠️ 这一组是**最要紧的**，见 §6 那次「查到了 ≠ 查到的是我问的那个」
	"InstrumentID":   keep("合约代码 —— 行情查询是**前缀匹配**，不核这个会拿到期权当期货"),
	"ExchangeID":     keep("交易所 —— 与 InstrumentID 合起来才唯一"),
	"ExchangeInstID": keep("交易所合约代码 —— 与 InstrumentID 可能不同，留着好对账"),
	"TradingDay":     keep("交易日 —— 一份不知道属于哪天的截面等于没落盘"),
	"ActionDay":      keep("自然日 —— 夜盘时它与 TradingDay 不同，正是本库反复栽的那个区分"),
	"UpdateTime":     keep("行情时刻 —— 与账户截面的采集时刻对齐要用"),
	"UpdateMillisec": keep("行情毫秒 —— 同上；换挡那一瞬要靠它定位（kq_facts 41 同形）"),

	// —— 本次要的那个判别
	"LastPrice":          keep("⚠️ **最新价** —— 这就是本表存在的理由：分开「今结算价基准」与「最新价基准」"),
	"SettlementPrice":    keep("今结算价 —— 与 LastPrice 成对，两者不等的那一刻才有判别力"),
	"PreSettlementPrice": keep("昨结算价 —— 保证金与手续费的候选基准之一"),
	"ClosePrice":         keep("收盘价 —— kq_facts 37 的逐日盯市基线就压在收盘价与结算价的差上"),
	"PreClosePrice":      keep("昨收盘价 —— 同上，且它与昨结算价不同"),
	"UpperLimitPrice":    keep("涨停价 —— 拒因实验与 FarPrice 都用它"),
	"LowerLimitPrice":    keep("跌停价 —— 同上"),

	// —— 当日行情
	"OpenPrice":       keep("开盘价 —— 与收盘/结算并列，凑齐一天的价格族"),
	"HighestPrice":    keep("最高价"),
	"LowestPrice":     keep("最低价"),
	"AveragePrice":    keep("⚠️ 均价 —— 结算价在多数品种上由它派生，是结算价来源的旁证"),
	"Volume":          keep("成交量"),
	"Turnover":        keep("成交额 —— 与 Volume、AveragePrice 三者互为校验"),
	"OpenInterest":    keep("持仓量"),
	"PreOpenInterest": keep("昨持仓量"),

	// —— ⚠️ 保留字段：**小写、不导出，但反射看得见**
	"reserve1": drop("CTP 保留字段 —— 不导出、无语义、柜台不填；留着只会让夹具多两个空键"),
	"reserve2": drop("CTP 保留字段 —— 同上"),

	// —— 期权，本库不建模
	"PreDelta":  drop("期权 delta —— 本库只做期货（fidelity.md 覆盖范围）"),
	"CurrDelta": drop("期权 delta —— 同上"),

	// —— ⚠️ 盘口五档：全丢，理由见本表开头
	"BidPrice1": drop("盘口 —— **本库不做盘口**，收下不建模的数据会让人以为它有支撑"),
	"AskPrice1": drop("盘口 —— 同上"),
}

func init() {
	// ⚠️ 五档剩下的 18 个字段用循环补齐，**理由逐条相同**，
	// 手写 18 遍只会让人跳过读它们。而漏一个就会被
	// TestFieldDecisionsAreComplete 抓住 —— 所以这里不是在偷懒躲过检查。
	for i := 1; i <= 5; i++ {
		for _, pre := range []string{"BidPrice", "BidVolume", "AskPrice", "AskVolume"} {
			quoteFields[pre+string(rune('0'+i))] =
				drop("盘口 —— **本库不做盘口**，收下不建模的数据会让人以为它有支撑")
		}
	}
}
