package kq

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// 脱敏用**白名单**，不用黑名单。
//
// 同一个疏忽，两种写法一个静默一个响：
//
//	黑名单（擦掉已知敏感字段）  漏一个 → 泄漏          → 静默
//	白名单（只留已知安全字段）  漏一个 → 夹具缺字段    → 对拍报错
//
// 它与「字段集 ⊆ 已知集」是同一条原理的两半：那条管**多出来**的，
// 白名单管**该去掉**的。
//
// ⚠️ 脱敏在**落盘之前**做。先落原始盘再擦，原始文件已经上过磁盘、
// 可能已经进过 git index —— 一次 `git add -A` 就够了。

// AccountPlaceholder 替换掉业务截面路径里的账户 UUID。
const AccountPlaceholder = "ACCOUNT"

// accountKeep 是资金账户截面里允许进夹具的键。
//
// 实测 23 个键（DIFF 文档只列了 18）。判定依据写在旁边，
// 送审时这张表本身要交给评审逐个核对——白名单少一个键，夹具里就少一个字段，
// 那是能查的；白名单多留一个不该留的，只有对着原始快照看才查得出来。
var accountKeep = map[string]string{
	"currency":                "保留：币种，无标识性",
	"pre_balance":             "保留：金额",
	"static_balance":          "保留：金额",
	"balance":                 "保留：金额",
	"available":               "保留：金额",
	"risk_ratio":              "保留：比率",
	"deposit":                 "保留：金额",
	"withdraw":                "保留：金额",
	"commission":              "保留：金额",
	"premium":                 "保留：金额",
	"close_profit":            "保留：金额",
	"position_profit":         "保留：金额",
	"float_profit":            "保留：金额",
	"margin":                  "保留：金额",
	"frozen_margin":           "保留：金额",
	"frozen_commission":       "保留：金额",
	"frozen_premium":          "保留：金额",
	"ctp_balance":             "保留：金额（CTP 口径，文档未列）",
	"ctp_available":           "保留：金额（CTP 口径，文档未列）",
	"market_value":            "保留：金额（文档未列）",
	"pre_option_market_value": "保留：金额（文档未列）",
}

// accountDrop 是明确判定为丢弃的账户键。列出来是为了区分
// 「我判定它该丢」与「我没见过它」——后者必须报未分类。
var accountDrop = map[string]string{
	"account_id": "丢弃：账号标识",
	"user_id":    "丢弃：账户 UUID",
}

// positionKeep 是持仓截面里允许进夹具的键。持仓字段全部是数量与金额，无标识性。
var positionKeep = map[string]string{
	"exchange_id": "保留", "instrument_id": "保留", "hedge_flag": "保留", "last_price": "保留",
	"volume_long": "保留", "volume_long_today": "保留", "volume_long_his": "保留",
	"volume_long_frozen": "保留", "volume_long_frozen_today": "保留", "volume_long_frozen_his": "保留",
	"volume_short": "保留", "volume_short_today": "保留", "volume_short_his": "保留",
	"volume_short_frozen": "保留", "volume_short_frozen_today": "保留", "volume_short_frozen_his": "保留",
	"open_price_long": "保留", "open_price_short": "保留",
	"open_cost_long": "保留", "open_cost_short": "保留",
	"position_price_long": "保留", "position_price_short": "保留",
	"position_cost_long": "保留", "position_cost_short": "保留",
	"float_profit_long": "保留", "float_profit_short": "保留",
	"position_profit_long": "保留", "position_profit_short": "保留",
	"margin_long": "保留", "margin_short": "保留", "margin": "保留",
	"order_volume_buy_open": "保留", "order_volume_buy_close": "保留",
	"order_volume_sell_open": "保留", "order_volume_sell_close": "保留",
	"pos_long_his": "保留", "pos_long_today": "保留",
	"pos_short_his": "保留", "pos_short_today": "保留",
	"future_margin": "保留", "open_price_long_his": "保留", "open_price_short_his": "保留",

	// ⚠️ 以下 20 个键是**首次实战时由字段集断言抓出来的**：持仓记录一出现就冒了出来，
	// DIFF 文档一个都没列。它们不是噪声，是**结构性证据**——
	// margin / open_cost / position_cost 都按今昨分开给，
	// 这正是逐日盯市要求的两条基线在柜台侧的形态。
	"margin_long_today":         "保留：今仓保证金（文档未列）",
	"margin_long_his":           "保留：昨仓保证金（文档未列）",
	"margin_short_today":        "保留：今仓保证金（文档未列）",
	"margin_short_his":          "保留：昨仓保证金（文档未列）",
	"open_cost_long_today":      "保留：今仓开仓成本（文档未列）",
	"open_cost_long_his":        "保留：昨仓开仓成本（文档未列）",
	"open_cost_short_today":     "保留：今仓开仓成本（文档未列）",
	"open_cost_short_his":       "保留：昨仓开仓成本（文档未列）",
	"position_cost_long_today":  "保留：今仓持仓成本（文档未列）",
	"position_cost_long_his":    "保留：昨仓持仓成本（文档未列）",
	"position_cost_short_today": "保留：今仓持仓成本（文档未列）",
	"position_cost_short_his":   "保留：昨仓持仓成本（文档未列）",
	"volume_long_yd":            "保留：昨仓量的另一口径（与 volume_long_his 并存，文档未列）",
	"volume_short_yd":           "保留：昨仓量的另一口径（文档未列）",
	"float_profit":              "保留：多空合计浮动盈亏（文档未列）",
	"position_profit":           "保留：多空合计持仓盈亏（文档未列）",
	"market_value":              "保留：市值（文档未列）",
	"market_value_long":         "保留：市值（文档未列）",
	"market_value_short":        "保留：市值（文档未列）",
	"market_status":             "保留：合约交易状态（文档未列）",
}

var positionDrop = map[string]string{
	"user_id":     "丢弃：账户 UUID",
	"investor_id": "丢弃：投资者代码",
	"account_id":  "丢弃：账号标识",
}

// tradeKeep 是成交截面的白名单。
//
// ⚠️ 成交此前**根本没进夹具**，而这正好是本项目核心那条设计的死角：
//
//	本库存逐笔明细，理由是「均价是有损压缩」——
//	(2 手 @100, 1 手 @130) 与 (3 手 @110) 均价相同，
//	平掉 1 手 @120 时逐笔对冲 +20、按均价 +10，两个结果都不会报错。
//
// 而夹具只存了持仓截面，也就是**只存了均价**。
// 于是从存档证据里重建不出本库的输入，逐笔对冲口径在夹具上**永远不可对拍** ——
// 一个为了不丢明细而做的设计，它的证据层把明细丢了。
//
// 落在实测上：现有 20 份 2 手样本里，两笔全是同一个价
// （open_cost = open_price × 2 × 乘数，见 probes.md §9），
// 也就是加权平均这条**从未被考验过**，而单看持仓截面**看不出这一点**。
var tradeKeep = map[string]string{
	"trade_id":          "保留：成交编号，同一笔的幂等键",
	"order_id":          "保留：对应委托，用于把成交归到某次下单",
	"exchange_id":       "保留：交易所",
	"instrument_id":     "保留：合约",
	"exchange_trade_id": "保留：交易所成交号",
	"direction":         "保留：买卖方向",
	"offset":            "保留：开平标志 —— ⚠️ 平今平昨的判定全靠它",
	"price":             "保留：**成交价** —— 逐笔对冲口径的基线，本库的 Lot.OpenPrice",
	"volume":            "保留：成交手数",
	"trade_date_time":   "保留：成交时刻（纳秒）",
	"hedge_flag":        "保留：投机套保标志 —— ⚠️ 它决定保证金率，白名单初稿照 DIFF 文档列时漏了，是实况采集当场报出来的",
	"commission":        "保留：这一笔的手续费",
	"seqno":             "保留：序号",
}

var tradeDrop = map[string]string{
	"user_id":     "丢弃：账户 UUID",
	"investor_id": "丢弃：投资者代码",
	"account_id":  "丢弃：账号标识",
	"broker_id":   "丢弃：经纪商标识",
}

// orderKeep 是**委托**截面的白名单。
//
// ⚠️ 委托此前不进夹具，而那让**冻结**这一整块永远不可对拍：
// 柜台的 `volume_*_frozen_*` 与 `frozen_margin` 都是「挂着的委托」的函数，
// 而夹具里没有委托 —— 于是本库算出来的冻结拿什么去比都比不了。
// 20260909 加冻结实现时，破坏验证当场演示了这一点：
// 把冻结合计改成漏掉昨仓那部分，**全套测试照样绿**，
// 因为没有任何一份夹具能走到那一支。
//
// ⚠️ 保留的每一项都是**数量、价格或状态**，无标识性；
// 账户 / 投资者 / 经纪商那几个标识一律进 orderDrop。
var orderKeep = map[string]string{
	"order_id":            "保留：委托编号，同一笔的幂等键；也是成交记录里 order_id 的对端",
	"exchange_order_id":   "保留：交易所委托号",
	"exchange_id":         "保留：交易所",
	"instrument_id":       "保留：合约",
	"direction":           "保留：买卖方向",
	"offset":              "保留：开平标志 —— ⚠️ 冻的是今仓还是昨仓全靠它",
	"volume_orign":        "保留：委托手数（DIFF 的拼写就是 orign，不是 origin）",
	"volume_left":         "保留：**未成交手数** —— 冻结量按它算，不是按委托量",
	"limit_price":         "保留：委托价",
	"price_type":          "保留：价格类型（限价/市价）",
	"volume_condition":    "保留：成交量类型",
	"time_condition":      "保留：有效期类型",
	"hedge_flag":          "保留：投机套保标志 —— 它决定保证金率",
	"status":              "保留：ALIVE / FINISHED",
	"last_msg":            "保留：**拒因原话** —— 实验 6 的全部产出都在这个字段上",
	"insert_date_time":    "保留：报入时刻（纳秒）",
	"is_dead":             "保留：是否已终结",
	"is_online":           "保留：是否已报入交易所",
	"is_error":            "保留：是否出错",
	"seqno":               "保留：序号",
	"frozen_margin":       "保留：这一笔冻结的保证金。⚠️ 实测**柜台不发这个字段**（20260909 三份样本里都缺席）—— 留在白名单里是因为它哪天出现时不该被判成漂移；账户侧的 frozen_margin 是另一个字段，那个有",
	"frozen_commission":   "保留：这一笔冻结的手续费",
	"frozen_premium":      "保留：期权权利金冻结（本库不建模期权，但照实记）",
	"exchange_trade_id":   "保留：若有",
	"order_sys_id":        "保留：交易所系统号",
	"insert_date_time_ns": "保留：报入时刻的另一种表示（若有）",
}

var orderDrop = map[string]string{
	"user_id":     "丢弃：账户 UUID",
	"investor_id": "丢弃：投资者代码",
	"account_id":  "丢弃：账号标识",
	"broker_id":   "丢弃：经纪商标识",
	"user_key":    "丢弃：会话标识",
}

// quoteKeep 是行情截面的白名单。
//
// ⚠️ 行情进夹具的理由与成交同一条：**让夹具自足**。
//
//	昨结算价是按额手续费的基准（kq_facts 4）、也是持仓保证金的基准（kq_facts 1）、
//	还是逐日盯市重置基线的那个数 —— 而它此前**不在夹具里的任何地方**。
//
// 于是拿夹具重算手续费或保证金，都要先去别处找一个数补进来，
// 而「别处」意味着那个数不属于这份证据，它可以被换掉而没人发现。
//
// ⚠️ 只留**本库用得上**的字段，不整份行情照搬：
// 五档盘口与成交量每一拍都在动，存进夹具只会让同名夹具永远「内容不同」，
// 而那条冲突守卫会因此天天报警 —— 一个天天报警的守卫等于没有守卫。
var quoteKeep = map[string]string{
	"instrument_id":     "保留：合约",
	"exchange_id":       "保留：交易所",
	"volume_multiple":   "保留：合约乘数 —— ⚠️ 漏乘会得到量级正确到肉眼看不出的错值",
	"price_tick":        "保留：最小变动价位",
	"price_decs":        "保留：报价小数位",
	"pre_settlement":    "保留：**昨结算价** —— 手续费基准、保证金基准、逐日盯市基线，三处都用它",
	"settlement":        "保留：今结算价 —— ⚠️ 结算之前它是无值",
	"pre_close":         "保留：昨收盘价（与昨结算价不是一回事，留着是为了能证明用的不是它）",
	"last_price":        "保留：最新价",
	"upper_limit":       "保留：涨停价",
	"lower_limit":       "保留：跌停价",
	"open_interest":     "保留：持仓量",
	"pre_open_interest": "保留：昨持仓量",
	"expired":           "保留：是否已到期",
	"datetime":          "保留：行情时刻 —— 判断这份行情是不是停在某个时刻的依据",
	"margin":            "保留：柜台给的每手保证金 —— ⚠️ 单一数值，不是四个保证金率",
	"commission":        "保留：柜台给的每手手续费 —— ⚠️ 单一数值，丢掉了平今这个维度",
	"trading_day":       "保留：交易日",
}

var quoteDrop = map[string]string{
	"user_id":    "丢弃：账户 UUID",
	"account_id": "丢弃：账号标识",
}

// Fixture 是脱敏后的夹具，可以入库。
type Fixture struct {
	TradingDay string                    `json:"trading_day"`
	CapturedAt string                    `json:"captured_at"`
	Note       string                    `json:"note,omitempty"`
	Account    map[string]any            `json:"account"`
	Positions  map[string]map[string]any `json:"positions"`

	// Quotes 是被观察合约的行情快照，**只含本库用得上的字段**。
	//
	// ⚠️ 它让夹具自足：昨结算价此前不在夹具里的任何地方，
	// 而它同时是手续费基准、保证金基准与逐日盯市基线。
	Quotes map[string]map[string]any `json:"quotes"`

	// Trades 是本交易日的逐笔成交。
	//
	// ⚠️ 它不是「顺手多存一点」：没有它，夹具里只有均价，
	// 而均价是有损压缩 —— 逐笔对冲口径在存档证据上就**永远不可对拍**。
	// 见 tradeKeep 的注释。
	Trades map[string]map[string]any `json:"trades"`

	// Orders 是**挂着与已终结的委托**。
	//
	// ⚠️ 它此前不进夹具，而那让**冻结**这一整块永远不可对拍：
	// 柜台的 volume_*_frozen_* 与 frozen_margin 都是「挂着的委托」的函数。
	// 见 orderKeep 的注释。
	//
	// ⚠️ omitempty：20260909 之前的夹具没有这个键，
	// 而它们**仍然是有效的证据**，不该因为格式加了一项就作废。
	Orders map[string]map[string]any `json:"orders,omitempty"`

	// Unclassified 记录白名单与丢弃表都没见过的键。
	//
	// ⚠️ 它非空即判失败，不是警告。字段集漂移必须自己报出来，
	// 而不是等有人想起来去数一遍——参照项目那次是靠人补全字段对拍，
	// 才一次暴露 35 处差异与 4 个从未建模的字段。
	Unclassified []string `json:"unclassified"`
}

// Sanitize 把交易与行情截面按白名单过成夹具。
//
// ⚠️ quotes 应当**只含被观察的合约**，由调用方筛好再传进来。
// 整份行情有几万个合约，而夹具是证据不是数据库。
func Sanitize(account, positions, trades, quotes, orders map[string]any,
	tradingDay, capturedAt, note string) *Fixture {
	f := &Fixture{
		TradingDay: tradingDay,
		CapturedAt: capturedAt,
		Note:       note,
		Account:    map[string]any{},
		Positions:  map[string]map[string]any{},
		Trades:     map[string]map[string]any{},
		Orders:     map[string]map[string]any{},
		Quotes:     map[string]map[string]any{},
	}
	unknown := map[string]struct{}{}

	for k, v := range account {
		if _, ok := accountKeep[k]; ok {
			f.Account[k] = v
			continue
		}
		if _, ok := accountDrop[k]; ok {
			continue
		}
		unknown["accounts/"+k] = struct{}{}
	}

	for sym, raw := range positions {
		p, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		out := map[string]any{}
		for k, v := range p {
			if _, ok := positionKeep[k]; ok {
				out[k] = v
				continue
			}
			if _, ok := positionDrop[k]; ok {
				continue
			}
			unknown["positions/"+k] = struct{}{}
		}
		f.Positions[sym] = out
	}

	for id, raw := range trades {
		t, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		out := map[string]any{}
		for k, v := range t {
			if _, ok := tradeKeep[k]; ok {
				out[k] = v
				continue
			}
			if _, ok := tradeDrop[k]; ok {
				continue
			}
			unknown["trades/"+k] = struct{}{}
		}
		f.Trades[id] = out
	}

	for id, raw := range orders {
		o, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		out := map[string]any{}
		for k, v := range o {
			if _, ok := orderKeep[k]; ok {
				out[k] = v
				continue
			}
			if _, ok := orderDrop[k]; ok {
				continue
			}
			unknown["orders/"+k] = struct{}{}
		}
		f.Orders[id] = out
	}

	for sym, raw := range quotes {
		q, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		out := map[string]any{}
		for k, v := range q {
			if _, ok := quoteKeep[k]; ok {
				out[k] = v
				continue
			}
			if _, ok := quoteDrop[k]; ok {
				continue
			}
			// ⚠️ 行情的键集比业务截面大得多（五档盘口、成交量、涨跌幅…），
			// 而本库只用其中十几个。这里**不报漂移**，只是不留 ——
			// 与账户/持仓/成交相反，理由是那三个是「柜台对账户说的话」，
			// 多一个键意味着有个概念本库不知道；而行情是公共数据，
			// 多一个键通常只是行情商多给了一个指标。
			// ⚠️ 代价写在这里：行情侧**没有漂移探测**，
			// 上游把 pre_settlement 改名的话，本库会安静地少一个字段。
			// 挡它的是 fixture.Load 那边的「必需字段缺失即报错」。
		}
		f.Quotes[sym] = out
	}

	for k := range unknown {
		f.Unclassified = append(f.Unclassified, k)
	}
	sort.Strings(f.Unclassified)
	return f
}

var (
	uuidRe = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)
	jwtRe  = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}`)
)

// Scrubbed 是脱敏之外的**独立复查**：直接在序列化结果上找不该出现的东西。
//
// ⚠️ 它与白名单是两套不同原理的机制，所以不会一起失效。
// 一个 bug 同时骗过两种不同机制，要比骗过同一种机制的两处难得多。
// secrets 传 .env 里那几个值（账号、密码、authID），本函数不回显它们。
// Secret 是一个待复查的凭据值，带上它在 .env 里的键名。
//
// ⚠️ 带键名不是为了好看：命中时报「KQ_PASSWORD 出现在夹具里」，
// 比报「某个长度 12 的值出现在夹具里」可操作得多，而键名本身不是秘密。
type Secret struct {
	Name  string
	Value string
}

// minCheckable 是独立复查能真正判别的最短值长度。
//
// ⚠️ 比它短的值查不了，原因是**假阳性**而不是假阴性：
// 四位数的 "9999" 会命中 "999989.3742" 这种价格里的子串，
// 于是每份夹具都报警——而一个永远报警的检查，和没有检查是一回事，
// 甚至更糟，因为它会训练人忽略告警。
const minCheckable = 8

// BlindSpots 报告独立复查**查不了**哪些凭据。
//
// ⚠️ 它的存在本身就是要点：一个不声明自己盲区的检查器，
// 会让人以为「没报错 = 都查过了」。这里把查不了的那部分明说出来。
func BlindSpots(secrets []Secret) []string {
	var out []string
	for _, s := range secrets {
		if s.Value != "" && len(s.Value) < minCheckable {
			out = append(out, s.Name)
		}
	}
	return out
}

func Scrubbed(f *Fixture, secrets []Secret) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	s := string(b)

	if m := uuidRe.FindString(s); m != "" {
		return fmt.Errorf("夹具里出现 UUID 形状的串（前 8 位 %s…），脱敏漏了", m[:8])
	}
	if jwtRe.MatchString(s) {
		return fmt.Errorf("夹具里出现 JWT 形状的串，脱敏漏了")
	}
	for _, sec := range secrets {
		if sec.Value == "" || len(sec.Value) < minCheckable {
			continue // 查不了的在 BlindSpots 里单独报，不在这里静默吞掉
		}
		if strings.Contains(s, sec.Value) {
			return fmt.Errorf("夹具里出现了 .env 中 %s 的值（长度 %d），脱敏漏了",
				sec.Name, len(sec.Value))
		}
	}
	if len(f.Unclassified) > 0 {
		return fmt.Errorf("字段集漂移：%d 个键既不在白名单也不在丢弃表里：%v",
			len(f.Unclassified), f.Unclassified)
	}
	return nil
}

// WhitelistReport 打印白名单本身，供评审逐键核对。
func WhitelistReport() string {
	var sb strings.Builder
	sb.WriteString("账户截面白名单\n")
	writeTable(&sb, accountKeep, accountDrop)
	sb.WriteString("\n持仓截面白名单\n")
	writeTable(&sb, positionKeep, positionDrop)
	return sb.String()
}

func writeTable(sb *strings.Builder, keep, drop map[string]string) {
	type row struct{ k, v string }
	var rows []row
	for k, v := range keep {
		rows = append(rows, row{k, v})
	}
	for k, v := range drop {
		rows = append(rows, row{k, v})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].k < rows[j].k })
	for _, r := range rows {
		fmt.Fprintf(sb, "  %-28s %s\n", r.k, r.v)
	}
}
