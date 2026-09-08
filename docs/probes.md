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

### ⚠️ 更正：它是 **334 MiB**，不是 12.5 MB —— 我量错了 27 倍

本文档先前写着「约 12.5 MB 且很慢」，后来改成「16.3 MB 仍未完整」。**两个都是错的。**
2026-09-07 用一个 Range 请求当场量准：

```
curl -r 0-100 https://openmd.shinnytech.com/t/md/symbols/latest.json
HTTP/1.1 206 Partial Content
Accept-Ranges: bytes
Content-Range: bytes 0-100/350177909      ← 350,177,909 字节 = 334 MiB
```

⚠️ **我量的是「我下了多少」，不是「它有多大」。** `ls -la` 量的是那个被截断的
本地文件，而它每次断在不同的地方，于是我每次都得到一个不同的、看起来合理的数。

⚠️ **而且我从没试过 Range** —— 但原因不是我以为它不可用。

> **更正一次归因错误。** 本文档先前写着「上游数据层一度以为服务端会忽略 Range，
> 我照单收下没验」。**这句两处都错**：
> (a) 上游没在消息里对我说过这句（他说的是「要 `-C -` 续传」，
>     而 `-C -` 恰恰**要求** Range 可用）；
> (b) 本库自己的历史文档写的是「完整性校验 + **断点重试**」——
>     **我一直相信续传可用。**
>
> 把自己的疏忽算到别人头上，比疏忽本身更难查，因为它会让复盘停在错的地方。

真实的形状是另一种，而且更难防：

> **工具就在手里，我从没想到用它去量。**
> 不是被一个错误挡住了，是**我不知道有东西需要量**——那个数字从没被怀疑过。

⚠️ 所以「两个错互相掩护」是**上游那边**的形状，不是本库这边的。
本库这边是「一个错 + 一个从未被触发的疑问」。两者的修法不同：
前者要打破错误之间的相互掩护，后者要**有一个能触发疑问的信号**。

按实测约 1.3 MB / 3 分钟，全量下载要 **13 小时左右**。

### 正确用法：Range 取片段，不整包下载

判「端点在不在 / 结构对不对 / 总量多大」，一个 256 KB 的 Range 请求就够：

```
curl -r 0-262143 …            256 KB
→ 269 条完整条目，volume_multiple / price_tick / price_decs / margin /
  commission / trading_time / expire_datetime 全部在内
```

⚠️ **这不改变 refdata 的选型，反而印证了它**：design.md 早已把 refdata 的主源
定为**柜台**（`ReqQryInstrument` 等），这个端点从来只是便利与交叉验证的通道。
把它当主源规划过的人，会按「12.5 MB，慢一点而已」去做，然后撞上 13 小时。

### ⚠️ 更正：截断的危险不在解析器上，在「修补」上

本文档先前写着「如果截断恰好落在两条记录之间，它会静默地少几个合约」。
**这句是错的**，2026-09-07 由上游数据层指出，本库两个语言各验一遍：

```
Python  截断在两条记录之间 → json.JSONDecodeError
Go      同上               → unexpected end of JSON input
【修补后】再解析            → err=nil，静默少 1 条      ← 危险在这一步
```

**标准解析器对截断输入一律报错**——外层容器没闭合，截在哪都一样。
静默丢**只发生在有人自作聪明地把它补全之后**。

这个区别决定了缓解措施完全不同：

| 错的结论 | 正确的做法 |
|---|---|
| 「加一道解析后的数量校验」 | **根本不要修补**：让它自然报错，然后 `curl -C -` 续传重来 |

⚠️ 落盘前必须能**不加任何补全地**完整 `Unmarshal`，失败就续传重来。
这个端点在两边的网络条件下都很难一次拉完，**完整性校验不是可选项**。

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

<!-- 历史留档:start -->

~~⚠️ 二者都尚未实际登录过。在拿到账号并跑通之前，本项目关于「与真实柜台一致」
的任何说法都只是计划，不是结论。~~

<!-- 历史留档:end -->

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

## 7. 判别实验：交易日 20260908 的夜盘（自然日 2026-09-07 周一 21:03–21:40）

⚠️ 本节记的是**怎么跑出来的**与**原始数字**。结论与它们的边界写在
[cn-futures-rules.md](./cn-futures-rules.md)，计数写在 [state.md](./state.md)，
这里不复述。

### 7.0 交易日跳变：一个欠着的采样点

21:03:18（周一自然日 2026-09-07）读到 `trading_day=20260908`。
即**周一夜盘属于周二**，与文档一致。

⚠️ 但真正欠数据层会话的是**跳变的秒级时刻**：20:55 一次、21:00 前后每 5 秒一次、21:05 一次。
20:55 那个点我错过了，所以**「是否恰好落在 21:00:00」今晚没测到**。
21:03 已经是 20260908，只能说明「跳变发生在 21:03 之前」，
不能说明它发生在 21:00:00。⚠️ **区间不是点**，这两句话的强度差得很远。

### 7.1 可复现的命令

    cd cmd/oracle
    go build -o oracle.exe .

    # 实验 3：单向大边按品种还是按合约合并（跨合约样本）
    ./oracle.exe probe -exp max-margin-side -env ../../.env         -dump <绝对路径>/testdata/probes -symbols "SHFE.rb2701,SHFE.rb2610"

    # 实验 3b：单向大边到底启没启用（同合约双向样本）
    ./oracle.exe probe -exp max-margin-lock -env ../../.env         -dump <绝对路径>/testdata/probes -symbols "SHFE.rb2701"

    # 实验 1/2 今仓版：margin 随不随行情动
    ./oracle.exe probe -exp margin-price   -env ../../.env -symbols "SHFE.rb2701"

    # 手续费：跨品种看是不是全局统一口径
    ./oracle.exe probe -exp fee-rates -env ../../.env         -symbols "DCE.i2701,SHFE.cu2701,DCE.m2703"

    # 手续费基准价：同合约一买一卖，两个成交价
    ./oracle.exe probe -exp fee-base  -env ../../.env -symbols "DCE.i2701"

    # 手续费收法：同品种两月份，每手固定额 vs 按昨结算价比例
    ./oracle.exe probe -exp fee-form  -env ../../.env -symbols "DCE.m2703,DCE.m2705"
    ./oracle.exe probe -exp fee-form  -env ../../.env -symbols "SHFE.rb2610,SHFE.rb2705"

    # 第 0 步：结算与今昨仓滚动的三态判定
    ./oracle.exe probe -exp settle-check -env ../../.env -symbols "SHFE.rb2701,DCE.m2701"

    # 挂单冻结：远价挂一手，量 frozen_* 与 available
    ./oracle.exe probe -exp frozen -env ../../.env -symbols "SHFE.rb2610"

    # 过夜种子（隔一次结算才有昨仓）
    ./oracle.exe probe -exp overnight-setup -env ../../.env

    # 收尾。⚠️ 不带 -symbols 是【全账户】平仓
    ./oracle.exe probe -exp flatten -env ../../.env -symbols "DCE.i2701"

⚠️ `-dump` 用**绝对路径**。`.env` 里的默认值也已改成绝对路径：
相对路径随 cwd 走，从 `cmd/oracle` 里跑会在 `cmd/oracle/testdata` 下另开一棵夹具树，
而日志里看不出它落在哪棵树上。仓库里有一条测试专门盯这件事
（根包 `TestNoStrayFixtureTrees`）。

### 7.2 原始数字

保证金与手续费（每手，昨结算价与乘数均取自同一截面）：

| 合约 | 昨结算价 | 乘数 | 每手保证金 | 保证金率 | 开仓费 | 平今费 |
|---|---|---|---|---|---|---|
| `SHFE.rb2610` | 3100 | 10 | 2170.00 | 7% | 0.3100 | 0.3100 |
| `SHFE.rb2701` | 3158 | 10 | 2210.60 | 7% | — | — |
| `SHFE.rb2705` | 3189 | 10 | 2232.30 | 7% | 0.3189 | 0.3189 |
| `DCE.m2701` | 3404 | 10 | 2382.80 | 7% | — | — |
| `DCE.m2703` | 3338 | 10 | 2336.60 | 7% | 1.5000 | 1.5000 |
| `DCE.m2705` | 3019 | 10 | 2113.30 | 7% | 1.5000 | 1.5000 |
| `DCE.i2701` | 734.5 | 100 | 8079.50 | 11% | 7.3450 | 7.3450 |
| `SHFE.cu2701` | 108330 | 5 | 59581.50 | 11% | 27.0825 | 27.0825 |
| `SHFE.ag2702` | 16095 | 15 | 53113.50 | **22%** | 12.07125 | 12.07125 |

乘数由持仓字段 `open_cost / (open_price × volume)` 反解，不查表、不按位数假设。

大边（三次账户级采样，单位元）：

| 样本 | 只有多头腿 | 只有空头腿 | 两腿齐备 |
|---|---|---|---|
| `rb2701` 多 + `rb2610` 空 | 2210.60 | 2170.00 | 4380.60 |
| `rb2701` 多 + `rb2701` 空 | 2210.60 | 2210.60 | 4421.20 |

手续费基准价（同合约一买一卖）：

| 方向 | 成交价 | 手续费 |
|---|---|---|
| `DCE.i2701` 买开 | 738.50 | 7.3450 |
| `DCE.i2701` 卖开 | 738.00 | 7.3450 |

⚠️ 昨结算价 734.50 × 100 × `1e-4` = 7.3450。**基准是昨结算价，不是成交价。**

挂单冻结（`rb2610` 跌停价挂一手买单，挂住不成交）：

| | `balance` | `available` | `margin` | `frozen_margin` | `frozen_commission` |
|---|---|---|---|---|---|
| 挂单前 | 998641.9010 | 989455.1010 | 9186.80 | 0 | 0 |
| 挂单中 | 998641.9010 | 987284.7910 | 9186.80 | 2170.0000 | 0.3100 |
| 撤单后 | — | — | 9186.80 | 0 | 0 |

⚠️ `12.07125` 与 `2170.0000` 这两个数都必须**从夹具的原始 JSON 读**，
不能从探针日志抄：日志的 `%.4f` 会把 `12.07125` 打成 `12.0712`，
而「柜台取到 4 位」正是待测的问题之一。探针的打印精度已改到 6 位。

账户恒等式（持 4 手种子、当日已有平仓的截面）：

    static_balance  1000000.0000
    close_profit       -740.0000
    commission          217.1312
    position_profit       0.0000
    margin             9186.8000
    balance          999042.8688   = 1000000 - 740 + 0 - 217.1312
    available        989856.0688   = balance - margin
    risk_ratio            0.0092   = margin / balance

### 7.3 一次真实的操作失误与两处工具缺陷

诚实记下来，因为它们都改了工具：

1. **清散仓时把过夜种子一起平了。** `flatten` 默认全账户。现在支持 `-symbols` 限定，
   不给才是全平，且会先把要动的仓逐条列出来。种子已重建。
2. **下单安全阀把平仓也挡了。** `PROBE_MAX_VOLUME=1` 对所有委托一视同仁，
   于是 2 手仓平不掉——一个防止扩大风险的守卫阻止了缩小风险。现在它只管开仓。
3. **同名夹具静默覆盖。** `fee-form` 一趟豆粕一趟螺纹，两个不同样本、两个不同结论，
   第二趟把第一趟整份盖掉。现在按内容判断：相同才覆盖，不同则另存并吼出来。
4. **`status` 读到半截截面**：持仓 4 手而 `margin` 打印成 `0.0000`。
   现在先等截面静默，且在自相矛盾时明说。

这四条的方法论形态见 [silent-risks.md](./silent-risks.md) 第 13、14 条。

---

## 7.5 昨仓批的执行计划（交易日 20260909，即自然日 2026-09-08 周二 21:00 起）

⚠️ 顺序不是偏好，有两处是**硬约束**：

    0. settle-check       结算发生了没有、今昨仓滚了没有 —— 三态判定
                          ⚠️ 归因依赖 §7.3b 的时段边界对照组，那一组已在日盘取得
    1. close-profit-sign  第 11 条 ⚠️ 必须最先，构造成本随后单调上升
    2. margin-price-full  第 1 条昨仓版
    3. yd-vs-his          第 7 条
    4. close-order        第 4 条（m2701，平少于昨仓量）
    5. baseline-full      第 2 / 10 条
    6. 平昨手续费          第 8 条
    7. flatten -symbols   ⚠️ 限定合约，只平种子

    ./oracle.exe probe -exp settle-check      -env ../../.env -symbols "SHFE.rb2701,DCE.m2701"
    ./oracle.exe probe -exp close-profit-sign -env ../../.env -symbols "SHFE.rb2701"

### 第 0 步有**三种**结果，不是两种

原计划只写了「滚了 → 往下跑 / 没滚 → 整批作废」，而那把第三种归进了作废：

| 结果 | 判据 | 动作 |
|---|---|---|
| 结算未发生 | `pre_balance` 未推进 | 昨仓批整批作废 |
| 结算发生 + 已滚昨仓 | `pre_balance` 推进，且有昨仓 | 正常往下跑 |
| **结算发生但没滚今昨** | `pre_balance` 推进，仍全是今仓 | ⚠️ **这是一条结论，不是故障** |

第三种若成立，说明**本柜台做日终结算但不滚今昨仓**——
它直接改写第 7 条与第 4 条的前提（那两条问「昨仓怎么算」，而昨仓根本不出现）。
把它归进「作废」，等于把这批里可能最重要的发现丢掉。

⚠️ 判据刻意用**与持仓无关**的指标（`pre_balance` / `close_profit` / `pre_settlement`），
因为「持仓没滚」正是待判的事情之一。

### ⚠️ 第三种结论有一个前提，而它差点被漏掉

干跑这条探针时它当场报出了第三种结论——**那是假阳性**：
基线取的是 20260907，而种子是在 20260908 **之内**建的，
那次结算发生时它根本还不存在。**结算之后才建的仓本来就是今仓，
判它「没滚」是在判一件没到期的事。**

所以判据补了一条前提：**基线截面里必须先有这些今仓**，否则第三种结论无判别力，
探针会明说「判据不成立」而不是给结论。
⚠️ 这条前提是干跑抓到的，不是设计时想到的——**而它落在三个分支里最贵的那一个上**。

### 第 1 步是市场相关的，所以它有时限、且方向中性

「构造一笔为正的平仓盈亏」需要行情配合，而它排在最前且阻塞后面全部。两个约束：

- **方向中性**：同合约开一多一空，行情往哪边走都有一条腿盈利，平掉盈利那条。
  不需要对方向下注——⚠️ **需要押注才能构造的样本，成本和效度会一起随行情走。**
- **20 分钟时限**：等不到就跳过，去跑昨仓批。
  种子过了下一次结算仍然是昨仓，**昨仓批不是一次性的**；
  拿窗口去赌第 1 步，赔率不对。

标的选 `rb`（一个 tick 10 元、盘口活），不选 `cu`（一个 tick 50 元）。
⚠️ 此前算「24 个 tick」用的是 `cu`，那是为了翻当日的 −1190；
新交易日归零之后前提变了，**标的应当重选**。

---

## 7.3 日期约定：夹具名与实测记录一律用**交易日**

⚠️ **这条此前存在但一处也没写下来**，而它今晚就会撞车。

夹具文件名里的 8 位日期是 `trading_day`，不是自然日——20 份夹具全都遵守，靠的是我记得。
问题在于交易日与自然日**不是同一个东西**：

    交易日 20260908 的夜盘  →  物理上发生在自然日 2026-09-07 周一晚
    自然日 2026-09-08 的夜盘 →  属于交易日 20260909

于是「2026-09-08 夜盘」这个标签**同时指向两场不同的实验**，
而没有任何字段能把它们分开。⚠️ 这一周它已经制造了两次真实的日期错误
（实现方与评审方各一次）——**能骗过写它的人和评审的人的标签，
也会骗过三个月后的读者**，而那时错的是证据的日期，不是日程。

约定，两条：

1. **夹具名与实测记录里的日期，一律是交易日**，写成紧凑形式：`交易日 20260908`。
2. **凡指自然日必须显式写「自然日」**，并用带横线的形式：`自然日 2026-09-07`。

两种格式因此在**字面上**就分得开，不靠读的人记得是哪一种。

⚠️ 这两条不靠人记：`TestFixtureNameMatchesTradingDay` 断言每份夹具的文件名日期
等于它内部的 `trading_day`；`TestSessionLabelsAreQualified` 断言文档里带横线的
日期紧接「夜盘/日盘」时，前面必须直接写着「自然日」。
**靠记得是条目层的解法，钉在字面上是动作层的解法**——见 [silent-risks.md](./silent-risks.md) 第 26 条。

---

## 7.3b 时段边界对照组（自然日 2026-09-08 日盘，交易日 20260908）

⚠️ **这一组的价值不在它测出了什么，在于没有它，今晚测出的东西无法归因。**

第 0 步要判「结算做了什么」，做法是拿新交易日的截面与交易日 20260908 的基线比。
但周一夜盘收盘（23:00）到周二日盘开盘（09:00）之间**也隔着一道时段边界**，
而那里**没有结算**。不先量出「只跨时段时什么不变」，
今晚看到的变化就可能只是「又跨了一个时段」。

基线采于自然日 2026-09-07 23:10（夜盘收盘后），现在是自然日 09-08 日盘，
期间 `commission` 与 `close_profit` 均未变动（无成交）：

| 字段 | 基线 | 跨停盘之后 | |
|---|---|---|---|
| `volume_long_today` | 2 | 2 | 今昨仓**不滚** |
| `volume_long_his` | 0 | 0 | |
| `open_price_long` | 3151 | 3151 | |
| `position_price_long` | 3151 | 3151 | 逐日盯市基线**不重置** |
| `margin_long` | 4421.2 | 4421.2 | 保证金**不重算** |
| `last_price` | 3165 | 3157 | 行情（走了 8 个点） |

**行情驱动之外的字段一个都没变。** 于是今晚若这些字段变了，只能归给**结算**。

⚠️ 顺带把保证金基准的证据加强了一档：价格跨时段走了 8 个点而 `margin_long` 分毫不动。
按最新价算应为 3157 × 10 × 7% × 2 = 4419.8。夜盘那次价格只走了 2 个点，
这次的排除力度大得多，而且**跨了一道停盘**。

### ⚠️ 这条探针的判据修了两次，两次的修补各自制造了对方的病

	一跑  取当日**最早**的基线  → 报 22 处「时段边界改动了持仓」
	                              假阳性：基线采于种子建立之前，变的是**我自己下的单**
	二跑  改取当日**最近**的基线 → 挑到 2 分钟前的截面，last_price 3158 → 3158
	                              **一道边界都没跨**，判据空转

合格的对照基线要**同时**满足三条，缺一条就从一头或另一头失效：

1. 同一交易日（否则中间隔着结算，那是 `settle-check` 的事）
2. **此后没有发生过成交**（用 `commission` / `close_profit` 是否变动来核）
3. **与现在不在同一个自然日**（那才保证跨过 23:00–09:00 那道停盘）

⚠️ 第 3 条用**自然日**判，因为时段是墙钟上的东西——这是 §7.3 那条日期约定
在代码里第一次派上用场。

---

## 7.4 夹具保留策略：只因「被证明是错的」而删，不因「有更新的了」而删

⚠️ 这条规矩是被一次实测逼出来的，而它的论据本身就说明了为什么需要它。

`static_balance = pre_balance + deposit − withdraw` 这条恒等式，
**只有 2026-09-07 那两份夹具能验证**——整批夹具里只有它们的 `deposit` 非零
（那天是开户入金 100 万；此后再没有出入金）。

按「留最新的就够了」的直觉，这两份在 09-08 的夹具落盘之后就该被清掉。
清掉之后不会有任何东西报错，只会**永远失去验证这条关系的能力**。

> **判别力是随样本组合涨的，而删除是不可逆的。**
> 一份夹具今天没用，不等于它明天没用——它可能是唯一携带某个非零值的那份。

这条不只是规矩，它**有守卫**：`TestAccountIdentitiesHoldOnEveryFixture`
会统计每一类判别力各有几份样本，缺哪一类就明说哪一类在空转。
实测把 09-07 那两份移走再跑：

    --- FAIL: TestAccountIdentitiesHoldOnEveryFixture
        ⚠️ 没有一份夹具有入金 —— `static_balance = pre + deposit − withdraw` 这条恒等式空转

⚠️ 所以「删夹具」这个动作的代价是**当场可见**的，而不是几个月后才发现某条断言一直在空转。

---

## 8. 仍然没能回答的

诚实记下来，免得后面把「没测」读成「测过了」：

| 问题 | 状态 |
|---|---|
| DCE 日行情端点 | **412，未打通**。见 §2 |
| SHFE 的合约参数 / 每日交易参数端点 | 猜了两个，**全 404**。已由天勤的公开合约字典绕开（§4） |
| 快期账户能否登录、快期模拟的实际行为 | ✅ **已验证**，见 §6.1 |
| SimNow 能否登录 | ✅ **已验证**，见 §6.3。合约表查询待 v0.1.0 自写节流 |
| 快期模拟与真实 CTP 柜台的口径差 | ⚠️ **已测量但样本弱**：有持仓、有浮亏时 `balance == ctp_balance`、`available == ctp_available`，差 `0.000000`（§7.2）。但样本里没有期权、没有昨仓、没有冻结，而那三样正是可能分岔的地方 |
| 盘中的 `UseMargin` 是否随价格变动 | ✅ **已验证：不随行情动**（§7.2）。今仓的基准是昨结算价 |
| 各交易所今昨仓的实际取值 | **未验证**，昨仓要等种子过夜结算 |
| 单向大边的实际取值 | ⚠️ **在快期模拟上测不出来**——那个口子根本没实现大边（§7.2）。这不是「还没测」，是要换口子 |
| 平昨手续费档 | **未验证**，需要昨仓。开仓档与平今档同额**不能外推到平昨** |
| 四个冻结项在 `Available` 里的作用 | ✅ **已验证**（§7.2）：`FrozenMargin` / `FrozenCommission` 从 `Available` 扣、不进 `Balance`、不进 `Margin`、撤单完整释放。⚠️ `FrozenCash` / `frozen_premium` 属期权，全程为 0，未测 |
| 手续费的取整口径 | ⚠️ **「到分」两个候选已排除**（`ag2702` 每手 `12.07125`）；「不取整」与「取到更细的位数」仍分不开 |
| 交易日跳变的**秒级时刻** | ⚠️ **未测到**：20:55 采样点错过了，21:03 的读数只能说明「21:03 之前已跳」（§7.0） |

---

<!-- 历史留档:start -->

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

<!-- 历史留档:end -->
