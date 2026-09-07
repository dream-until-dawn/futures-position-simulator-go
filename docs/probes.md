# 前期探针报告

> 执行日期 **2026-09-07**。所有命令可复现，结果贴的是当时的原始输出。
> 本文只记录**跑过的**东西；推断与设计取舍在 [design.md](./design.md)，
> 规则本身在 [cn-futures-rules.md](./cn-futures-rules.md)。

写代码之前先问四个问题，每个都用一条命令回答，而不是靠记忆：

1. 依赖能不能拉下来？
2. 结算价这类**只能由交易所产生**的数据，从哪里取？
3. 对拍的「真值」从哪里来——有没有一个能逐字段比对的权威口径？
4. 那个口径的 Go 通道要不要 cgo？要 cgo 的话 Windows 上还能不能编？

四条全部有答案，且都不是「应该可以」。

---

## 1. 工具链与依赖通路

```bash
go version                                        # go1.26.1 windows/amd64
curl -s -o /dev/null -w "%{http_code}" https://proxy.golang.org/     # 000（不可达）
curl -s -o /dev/null -w "%{http_code}" https://goproxy.cn/           # 200
curl -s -o /dev/null -w "%{http_code}" https://github.com/           # 200
```

⚠️ **`proxy.golang.org` 在本机不可达**，2.45 秒后连接失败而不是超时。

这条必须写进 README 与 CI：`GOPROXY` 未设或为默认值时，`go get` 会挂在第一个依赖上，
而报错信息（`dial tcp: i/o timeout`）不会指向真正的原因。

```bash
go env -w GOPROXY=https://goproxy.cn,direct
```

`github.com` 本身可达，所以源码托管与 issue 流程不受影响；受影响的只有模块代理。

---

## 2. 结算价的来源：五家交易所的公开日行情

**结算价不是行情，是交易所的结算结果**，无法从 K 线推出来（成交量加权 + 各所自己的
规则 + 无成交时的特殊处理）。逐日盯市的每一分钱都挂在它上面，所以它必须有稳定来源。

| 交易所 | 端点 | 结果 | 格式 | 含结算价字段 |
|---|---|---|---|---|
| 上期所 SHFE | `https://www.shfe.com.cn/data/tradedata/future/dailydata/kx20260904.dat` | **200**，122 679 B | JSON | `SETTLEMENTPRICE` `PRESETTLEMENTPRICE` |
| 上期能源 INE | `https://www.ine.cn/data/tradedata/future/dailydata/kx20260904.dat` | **200**，27 544 B | JSON（与 SHFE 同构） | 同上 |
| 中金所 CFFEX | `http://www.cffex.com.cn/sj/hqsj/rtj/202609/04/index.xml` | **200**，489 092 B | XML | `presettlementprice` `settlementpriceif` |
| 郑商所 CZCE | `https://www.czce.com.cn/cn/DFSStaticFiles/Future/2026/20260904/FutureDataDaily.txt` | **200**，37 944 B | 竖线分隔文本 | `昨结算` `今结算` `交割结算价` |
| 广期所 GFEX | `POST http://www.gfex.com.cn/u/interfacesWebTiDayQuotes/loadList`<br>`trade_date=20260904&trade_type=0` | **200**，17 433 B | JSON | `lastClear` `clearPrice` |
| 大商所 DCE | `POST http://www.dce.com.cn/publicweb/quotesdata/dayQuotesCh.html` | **412** | HTML 表格 | — |

2026-09-04 是周五，即探针当日最近的一个交易日。

三条值得单独记下来的：

- **CZCE 的 `http://` 会 301 到 `https://`**，`curl` 不加 `-L` 直接得到 0 字节且状态码 301。
  这种失败模式很容易被读成「接口挂了」。
- **CFFEX 的 XML 里期货与期权混在一起**（第一条就是 `HO2609-C-2500`），必须按
  `instrumentid` 形态过滤，不能整份当期货吃。
- ⚠️ **DCE 返回 412，加 `User-Agent` 与 `Referer` 后仍是 412。** 它有额外的反爬校验，
  这条**没有打通**。v0.1.0 的 `refdata/live` 会带着这个缺口发布，DCE 的日行情需要
  另找路径（交易所 App 接口 / 会员端下载 / 第三方数据源），或者接受由使用者自行提供。
  **不会拿别家的数据顶替，也不会插值。**

这五（六）个端点是**给使用者取结算价用的**，不是本库的运行时依赖——本库不产生行情，
结算价由回测引擎在结算步喂进来，详见 design.md 的「双层时钟」。

---

## 3. 真值通路一：CTP 口径

中国期货没有「交易所 REST API 返回持仓 JSON」这回事。持仓与资金的权威表述在**柜台**，
而柜台事实上的标准是上期技术的 **CTP（综合交易平台）**。

用 `gitee.com/haifengat/goctp@v1.10.17` 自带的 CTP v6.5.1 定义清点了一遍：

```bash
go mod download gitee.com/haifengat/goctp@v1.10.17
grep -cE '^type CThostFtdc\w+ struct' "$(go env GOMODCACHE)/gitee.com/haifengat/goctp@v1.10.17/ctpdefine/ctp_struct.go"
# 363
```

| 结构体 | 字段数 | 它决定什么 |
|---|---|---|
| `CThostFtdcTradingAccountField` | **49** | 资金账户：结存、可用、保证金占用、各类冻结、平仓盈亏、持仓盈亏 |
| `CThostFtdcInvestorPositionField` | **50** | 持仓：今昨仓、两套盈亏口径、公司保证金与交易所保证金 |
| `CThostFtdcInvestorPositionDetailField` | **31** | 逐笔持仓明细：逐笔对冲口径的盈亏挂在这里 |
| `CThostFtdcInstrumentField` | **35** | 合约规格：乘数、最小变动价位、多空保证金率、大边算法、今昨仓类型 |
| `CThostFtdcInstrumentMarginRateField` | 13 | 保证金率：按金额 / 按手数，多空分开，投机与套保分开 |
| `CThostFtdcInstrumentCommissionRateField` | 14 | 手续费率：开 / 平 / **平今**，各自按金额与按手数 |

这一步不只是数字，它直接定住了几条建模事实，而且是**从定义读出来的，不是回忆的**：

```
CThostFtdcInvestorPositionField 里同时有：
    CloseProfitByDate      逐日盯市口径的平仓盈亏
    CloseProfitByTrade     逐笔对冲口径的平仓盈亏
```

⚠️ **两套盈亏口径同时存在于同一个结构体里。** 这不是历史包袱，是中国期货的实际形态：
资金结算走逐日盯市，而客户看的账单里两个都有。**只实现一套 = 一半的字段对不上**。

```
    UseMargin              期货公司保证金
    ExchangeMargin         交易所保证金
```

⚠️ **保证金也是两层。** 期货公司在交易所标准上加收，两个值都要建模，否则风险度会算错
——风险度用的是公司口径。

```
CThostFtdcInstrumentField 里：
    PositionDateType       UseHistory / NoUseHistory
    MaxMarginSideAlgorithm NO / YES
    LongMarginRatio / ShortMarginRatio                    多空分别给
```

⚠️ **「区分今昨仓」与「单向大边」都是逐合约的开关，不是逐交易所的常识。**
按交易所硬编码（「上期所区分今昨、其余不区分」）在绝大多数合约上都对，
于是错的那几个不会被测出来。**这两个值必须从规则数据里读。**

`OffsetFlag` 的取值也一并确认了，七个：
`Open` `Close` `ForceClose` `CloseToday` `CloseYesterday` `ForceOff` `LocalForceClose`。
——**强平在 CTP 里有三种**（交易所强平 / 强减 / 本地强平），这与「强平是交易所的确定性算法」
的直觉相反，见 fidelity.md。

### Go 通道：Windows 上不需要 cgo

```bash
MOD="$(go env GOMODCACHE)/gitee.com/haifengat/goctp@v1.10.17"
grep -rl 'import "C"' "$MOD"          # 只有 lnx/quote_lnx.go、lnx/trade_lnx.go
grep -oE 'syscall' "$MOD"/win/*.go    # win/ctp_quote.go、win/ctp_trade.go
ls "$MOD/v6.5.1_20200908/win_x64/"    # thosttraderapi_se.dll .lib .h …
```

- **Windows 走 `syscall` 动态加载 DLL，不用 cgo。** 本机开发与对拍不需要 C 工具链
- Linux 才走 cgo（`lnx/*.so`）
- 模块自带 CTP v6.5.1 的头文件、win_x64 的 `.dll`/`.lib`、以及 Linux 的 `.so`

⚠️ 但它仍然是**一个带二进制的第三方模块**。按 design.md 的依赖硬约束，它只能进
`cmd/oracle` 这个**独立嵌套模块**，绝不进主模块。

---

## 4. 真值通路二：天勤 DIFF —— 更好的那一条

> 这一节是使用者提示「已有快期（天勤）免费账户」之后**追加验证**的。
> 验完的结论是：**它比原方案好**，因此被提升为主对拍口子。
> 但它也有原方案没有的**缺口**，所以 CTP 那条不废，改作权威裁决。

### 协议形态

```bash
curl -sL https://doc.shinnytech.com/diff/latest/general.html | grep -oiE 'json[ -]?merge[ -]?patch|rfc ?7386|rtn_data'
# JSON Merge Patch ×4，rfc7386 ×2，rtn_data ×6
```

**DIFF 是 JSON over WebSocket + JSON Merge Patch（RFC 7386）。**
服务端推 `rtn_data`，客户端把补丁合并进本地业务截面。

⚠️ **这意味着 Go 侧不需要 cgo、不需要 C++ SDK、不需要 DLL** ——
一个 WebSocket 客户端加一份 merge-patch 实现就够了。这是它胜过 CTP 通路的关键。

### 业务截面的字段

从 `funcset/trade.html` 与 `funcset/quote.html` 里逐个提取出来的键名：

**`trade/{user}/accounts/CNY`（18 个）**

```
account_id  currency  pre_balance  static_balance  balance  available  risk_ratio
deposit  withdraw  commission  premium
close_profit  position_profit  float_profit
margin  frozen_margin  frozen_commission  frozen_premium
```

**`trade/{user}/positions/{symbol}`（28 个）**

```
exchange_id  instrument_id  hedge_flag  last_price
volume_long  volume_long_today  volume_long_his  volume_long_frozen_today  volume_long_frozen_his
volume_short volume_short_today volume_short_his volume_short_frozen_today volume_short_frozen_his
open_price_long/short      open_cost_long/short
position_price_long/short  position_cost_long/short
float_profit_long/short    position_profit_long/short
margin_long/short
order_volume_buy_open  order_volume_buy_close  order_volume_sell_open  order_volume_sell_close
```

⚠️ **`float_profit` 与 `position_profit` 并列，`open_cost` 与 `position_cost` 并列。**
天勤原生就分开了「按开仓价」与「按昨结算价」两个基线——正是本库最核心的那条区分
（cn-futures-rules.md §5）。这是个强信号：**这套字段是照着真实柜台的语义设计的。**

**`orders` / `trades`**：`order_id` `direction` `offset` `volume_orign` `volume_left`
`price_type` `limit_price` `time_condition` `volume_condition` `status` `last_msg`
`insert_date_time` `exchange_order_id`；成交侧 `trade_id` `price` `volume` `trade_date_time`。

**`quotes/{symbol}`（34 个）**，含规则数据与结算价：

```
volume_multiple  price_tick  price_decs
max_market_order_volume  min_market_order_volume  max_limit_order_volume  min_limit_order_volume
margin  commission
upper_limit  lower_limit  pre_settlement  settlement  pre_close  open_interest  pre_open_interest
```

### ⚠️ 它给不了的三件事

对拍口子的价值不只看它给什么，更看它**不给**什么——那部分会变成盲区。

1. **只有一个 `close_profit`**，不分 `ByDate` / `ByTrade`。
   两套平仓盈亏口径的分别验证，**只能靠 CTP**
2. **`quotes.margin` / `quotes.commission` 是「每手保证金 / 每手手续费」的单一数值**，
   不是 CTP 的 6 个手续费率 + 4 个保证金率。
   ⚠️ **用它做规则数据会丢掉「平今费率」这个维度**——而日内策略的手续费几乎全落在那上面
3. **快期模拟是天勤自己的模拟撮合与结算，不是 CTP 柜台。**
   「与快期模拟一致」≠「与真实账户一致」，**这个差现在完全未知**

结论：**天勤做主对拍（覆盖广、门槛低、纯 Go），CTP 做权威裁决（补上述三个缺口）。**
详见 [design.md](./design.md) §5。

### 顺带发现：一份公开免鉴权的合约字典

```bash
curl -sL https://openmd.shinnytech.com/t/md/symbols/latest.json
```

**200，无需认证**，约 8 MB，覆盖全市场合约与指数，每条带
`class` `exchange_id` `instrument_id` `ins_name` `volume_multiple` `price_tick` `price_decs` 等。

这解决了 §7 表里「SHFE 的合约参数端点全 404」那个缺口——合约规格不必去爬六家交易所。

⚠️ 但**本机下载它不稳定**：`curl` 两次都在传输中途被截断（167 KB 与 8.05 MB 处
各断在一个未闭合的字符串上），PowerShell 走得通但很慢。
`cmd/refdata-sync` 必须做**完整性校验 + 断点重试**，不能拿到什么就用什么
——⚠️ 一份被截断的合约字典解析失败会报错，但如果截断恰好落在两条记录之间，
**它会静默地少几个合约**。

---

## 5. 模拟环境的可达性

```
http://www.simnow.com.cn/    202   （curl 在此域名上段错误，PowerShell Invoke-WebRequest 正常）
http://www.openctp.cn/       200
```

两个都可达。**SimNow**（上期技术官方模拟）是 CTP 通路的对拍环境；
**openctp** 的 TTS 提供 7×24 的 CTP 兼容环境，用于非交易时段的冒烟。

天勤侧从 TqSdk 源码里读到的服务入口（`raw.githubusercontent.com/shinnytech/tqsdk-python`）：
`auth.shinnytech.com`（鉴权）、`api.shinnytech.com/ns`（服务发现）、
`openmd.shinnytech.com`（公开行情与合约字典）。
`TqKq` 即快期模拟账户，其交易与持仓可在快期专业版 / v2 / v3 / APP 上直接查看
——**这一条很重要：对拍出分歧时，可以用人眼在快期客户端上核对第三方视图。**

> ✅ **已过期（2026-09-07 14:2x）：二者都已实际登录成功，见 §6。**
> 下面这段保留原文，因为它记录了当时的状态；但**不要读到这里就停**。

~~⚠️ 二者都尚未实际登录过。在拿到账号并跑通之前，本项目关于「与真实柜台一致」
的任何说法都只是计划，不是结论。~~

**更新后的说法**：两条通路都已登录成功，但「与真实柜台一致」仍然只是**计划**
——理由变了：不再是连不上，而是**判别实验一条都还没跑**。

---

## 6. 实测记录：两条通路都跑通了（2026-09-07 14:0x–14:2x）

> 使用者填好 `.env` 后的验证。**这是本仓库第一批「实测」等级的证据。**

### 6.1 快期模拟：端到端通

用 Go 写的一次性探针（`gorilla/websocket` + 自实现的 RFC 7386 合并），
完整走了 鉴权 → 网关解析 → WebSocket 登录 → 业务截面合并：

```
[1] auth               OK            auth.shinnytech.com，返回 JWT
[2] td gateway         wss://otg-sim.shinnytech.com/trade
[3] websocket          CONNECTED
[4] merge patch        5 条报文 / 4 个补丁
[5] trade 截面          accounts banks orders position_ready positions
                       pre_insert_orders session trade_more_data trades
                       trading_day transfers user_id
```

登录应答：`{"code":401,"level":"INFO","content":"登录成功"}`（401 是它的内部码，不是 HTTP）。
`trading_day = "20260907"`，与当日一致。账户是全新的：`balance = deposit = 1000000`、
`pre_balance = 0`、无持仓无委托。

**Go 侧依赖全部可拉取**（`GOPROXY=https://goproxy.cn,direct`），**不需要 cgo**。
design.md §5 里「纯 Go 可达」这条从推断变成了事实。

### 6.2 ⚠️ 文档列了 18 个账户字段，实际有 23 个

| | 字段 |
|---|---|
| 文档有、实际也有 | 18 个，一个不缺 |
| **实际有、文档没列** | `ctp_available` `ctp_balance` `market_value` `pre_option_market_value` `user_id` |

这正是「**挑着比永远发现不了『有个字段我压根没建模』**」的现场实例——
按 DIFF 文档建模会静默漏掉 5 个字段，而且不会有任何报错。

⚠️ **其中 `ctp_balance` 与 `ctp_available` 是重大发现。**
快期模拟**在同一份业务截面里同时给出 CTP 口径与天勤口径的权益与可用资金**。

这意味着 [fidelity.md](./fidelity.md) 第 4 节里那条「快期模拟与真实柜台的口径差**完全未测量**」
——至少在权益与可用资金这两个字段上，**可以在同一个账户内直接测量**，不必等到
v0.8.0 的双口子交叉验。这条要写进 v0.1.0 的实验清单。

（当前账户空仓，两个口径都是 1000000.00，看不出差异。**空仓时相等不是证据**——
必须建仓后再比，否则又是一次「阴性当阳性用」。）

### 6.3 SimNow：登录成功，但库的初始化链路卡住

```
[1] front connected    tcp://182.254.243.31:30001
    [低层] ReqAuthenticate -> 0        终端认证请求被接受
    [低层] ReqUserLogin    -> 0        ← 能走到这里，说明认证已成功返回 ErrorID=0
[3] TradingDay         20260907        ← 登录成功
[4] 合约数              0（180 秒内始终为 0）
```

**结论：`InvestorID` / 密码 / `BrokerID` / 前置地址 / `AppID` / `AuthCode` 全部有效。**
卡住的是 `goctp` 登录后自动发起的合约表查询——CTP 的查询接口有频率限制，
`goctp` 不做节流也不重试，链路就停在那里。

这是**库的问题，不是凭据问题**。v0.1.0 的 `cmd/oracle probe` 自己写查询节流即可绕过（`cmd/oracle/ctp/`）。

### 6.4 ⚠️ 一个把我自己绊了一下的假阳性

最初判 SimNow「登录超时失败」，是因为两个错误叠在一起：

1. **`goctp` 的 `OnRspUserLogin` 不在登录应答时触发**，而是等整条初始化链路
   （结算确认 + 合约 + 账户 + 持仓查询）跑完才触发。链路卡住 → 回调不来 →
   看起来像「登录没有应答」。**改成直接轮询 `TradingDay` 立刻就看到登录早就成功了。**
2. 我先用 TCP 连接测试判前置有效性，得到「两个端口都 CONNECTED」。
   随后扫 30000–30015 发现**十六个端口全部 accept**——这台主机前面有代理，
   **TCP 层的连通性测试对 CTP 前置毫无判别力**。

第 2 条已作为一条方法论写进 [silent-risks.md](./silent-risks.md)：
**一个总是返回「成功」的检查，和一个正确的检查，在成功样本上长得一模一样。**

真正有判别力的做法是**在协议层登录**：行情 API 在 30001 与 30011 上都登录成功，
在 30007 上超时并断开——这才把「有前置」和「没前置」分开。

---

## 7. 仍然没能回答的

诚实记下来，免得后面把「没测」读成「测过了」：

| 问题 | 状态 |
|---|---|
| DCE 日行情端点 | **412，未打通**。见 §2 |
| SHFE 的合约参数 / 每日交易参数端点 | 猜了两个，**全 404**。已由天勤的公开合约字典绕开（§4） |
| 快期账户能否登录、快期模拟的实际行为 | ✅ **已验证**，见 §6.1 |
| SimNow 能否登录 | ✅ **已验证**，见 §6.3。合约表查询待 v0.1.0 自写节流 |
| 快期模拟与真实 CTP 柜台的口径差 | 仍**未测量**，但 §6.2 发现了 `ctp_balance` / `ctp_available`，可在同账户内直接测（需先建仓） |
| 盘中的 `UseMargin` 是否随价格变动 | **未验证**，这是与加密货币差别最大的一条 |
| 各交易所今昨仓与大边的实际取值 | **未验证**，只知道字段在哪 |

---

## 待办（需要账号）—— 已完成，以下保留原文

> ✅ **两项均已完成（2026-09-07 14:2x），实测结果见 §6。** 下面保留原文，
> 是为了让「当时缺什么、拿到后验出了什么」这条线索完整。

### 1. 快期账户（使用者已有）

请把以下三项给我，我会写进 `.env`（已在 `.gitignore` 里），**不会提交进仓库**：

- 快期账户名与密码（`https://account.shinnytech.com/` 注册的那个）
- 是否已在快期客户端上看到过**快期模拟**账户（用于人眼交叉核对）

拿到后立刻能做的：跑通 DIFF 的 WebSocket 通路，把账户与持仓截面的原始 JSON 存成夹具，
并开始 [roadmap.md](./roadmap.md) 里的**判别实验**。

### 2. SimNow 账户（免费，待注册）

<https://www.simnow.com.cn/>

- 注册后会拿到 `InvestorID`（六位数字）与密码，`BrokerID` 固定 `9999`
- 前置地址在「产品服务」页，分**电信**与**移动**两套；另有一套 **7×24 环境**

它负责天勤给不了的三件事（§4）：两套平仓盈亏口径、完整的 6+4 费率与保证金率表、
以及**裁决天勤与真实柜台的口径差**。

---

暂时不需要真实期货账户。真实账户能提供的增量（期货公司的加收比例、各家风控参数）
在 v1.0 范围之外，且不免费。
