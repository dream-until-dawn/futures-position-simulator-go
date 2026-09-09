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
