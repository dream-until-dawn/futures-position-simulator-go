# 架构设计与决策记录

> 决策定稿于 2026-09-07。本文是 ADR（架构决策记录）+ 架构说明的合体。
> 规则依据见 [cn-futures-rules.md](./cn-futures-rules.md)，可行性依据见 [probes.md](./probes.md)。

## 1. 项目定位

用 Go 实现**中国期货市场**的持仓与结算内核，目标是尽可能贴近 100% 还原真实行为，
并保持良好的外部可引入性，供策略回测引擎等第三方使用。

一句话概括职责：**「成交与结算价 → 持仓 / 资金 / 结算单」的记账机。**

它不预测价格、不产生结算价、不模拟盘口深度——那些是市场结果或市场数据。
它做的是：给定一笔成交、一根 K 线、或一个交易日的结算价，算出持仓、盈亏、
手续费、保证金与风险度会变成什么样。

### 与参照项目的关键差异

本项目参照 [`okx-position-simulator-go`](https://github.com/dream-until-dawn/okx-position-simulator-go)
的方法论（证据等级、逐字段对拍、静默风险清单），但**核心模型完全不同**：

| | OKX（加密永续） | 中国期货 |
|---|---|---|
| 记账节奏 | **连续**：标记价一变，盈亏与保证金立刻变 | **逐日盯市**：日内一套账，日终按结算价兑现并重算 |
| 成本基线 | 开仓均价，一直不变 | 昨仓以**昨结算价**为基线，每日结算重置一次 |
| 盈亏口径 | 一套 | **两套并存**（逐日盯市 / 逐笔对冲） |
| 持仓身份 | 只有多空 | 多空 × **今昨** × 投机套保 |
| 保证金 | 一层，随标记价连续变动 | **两层**（交易所 / 期货公司），盘中很可能是静态的 |
| 强平 | 交易所的确定性算法，可 100% 复现 | **期货公司的风控制度，含人工环节，不可复现** |

⚠️ **最后一行决定了本项目「100% 模拟」的边界在哪。** 展开见
[fidelity.md](./fidelity.md) 与 cn-futures-rules.md §10。

---

## 2. 已定决策（ADR）

| # | 决策 | 结论 | 理由 |
|---|---|---|---|
| 1 | 职责边界 | 核算与结算内核 + 内置撮合 | 撮合是每个引擎都要做的事，各自重写既浪费又易错（尤其成交角色与冻结释放）。但**手工灌成交的路径保持完整可用** |
| 2 | 数值精度 | `shopspring/decimal` | 金额必须十进制。中国期货的结存要逐日累加，浮点误差会在长周期回测里漂出可见的量 |
| 3 | v1.0 覆盖范围 | 六家交易所的**期货**、**投机**持仓、单账户单币种（CNY） | 覆盖绝大多数回测场景。期权、组合保证金、套保套利明确排除，理由见 §6 |
| 4 | 规则数据来源 | Provider 接口 + `go:embed` 内置快照 + 可选运行时拉取 | 零配置可跑、离线可复现、又能取最新 |
| 5 | 驱动模型 | **双层时钟**：盘中推进 + 显式日终结算 | 见 §4，这是与参照项目最大的架构差异 |
| 6 | 一致性验证 | **双口子对拍**：天勤快期模拟（主）+ SimNow CTP（权威裁决） | 见 §5 |
| 7 | 集成形态 | 仅 Go 原生 API | 不做假柜台服务端，专注把库本身做扎实 |
| 8 | 状态即纯数据 | 可 JSON 序列化 + 可深拷贝，内部不藏 channel / goroutine / 闭包 | 参数扫描、walk-forward、事件重放要它。**必须第一天定，事后补代价极大** |
| 9 | 并发 | 默认不加锁 | 回测是单线程热路径，decimal 本身已有开销。并发需求用可选包装器解决 |
| 10 | 逐笔持仓明细 | **一等公民**，不可退化为均价 | 见下方 |
| 11 | 强平 | 只建模**判据**，不建模**执行** | 见下方 |

### 决策 10：逐笔明细必须从第一天就在

中国期货有两套盈亏口径同时存在（cn-futures-rules.md §5）。逐笔对冲口径要求知道
「这一手是哪一笔开的」，而均价是**有损压缩**：

```
(2 手 @100, 1 手 @130)  与  (3 手 @110)   均价相同
平掉 1 手 @120 时：
    逐笔对冲（先进先出）  = +20 × 1 × 乘数
    按均价              = +10 × 1 × 乘数
```

⚠️ **两个结果都不会报错。** 只存均价，逐笔对冲口径就再也算不回来，而且是**静默地**
算不回来——它会给出一个合理的数。所以持仓状态机从第一天就存明细，
均价是从明细**推出来的视图**，不是存储形态。

代价是内存与状态体积。回测里持仓明细的条数等于未平仓的开仓笔数，可控。

### 决策 11：强平只建模判据

OKX 的强平是交易所算法，输入相同则输出相同。中国期货的强平是**期货公司的风控制度**：
「什么时候通知」「宽限多久」「先平哪个合约」「一次平多少」各家不同，且含人工环节。
CTP 的平仓标志里同时有「交易所强平」「强减」「本地强平」三种取值，正说明它不是单一算法。

因此本库：

- **给判据**：风险度、可用资金是否为负、是否触发追保线——这些是确定的、可复现的
- **不给执行**：强平的时点、顺序、数量不做假设，输出**信号**与**风险敞口**
- 提供一个**可选**的约定策略（按风险度降序减仓直到回到阈值以下），
  显式标注为**约定而非还原**，**默认不启用**

**在中国期货语境下声称「100% 模拟强平」是不诚实的。本项目不这么说。**

---

## 3. 包结构

模块路径 `github.com/dream-until-dawn/futures-position-simulator-go`，根包名 `futsim`，`go 1.22`。

```
/                       package futsim —— 门面
  doc.go                  包文档
  simulator.go            门面：出入金、报单、成交、行情、结算、查询、状态存取
  config.go               构造配置
  state.go                状态存档与恢复

  types/                枚举与值类型。取值**规范化**，不绑定任何一种线格式；
                        CTP 与 DIFF 两套映射都在包内显式给出（见下）
                          交易所、合约、买卖方向、开平标志、投机套保、今昨仓、报单状态
  ctperr/               与 CTP 错误码对齐的错误类型

  refdata/              规则数据：合约规格 / 保证金率 / 手续费率 / 交易日历 / 交易时段
    live/                 ← 独立子包：从公开与授权接口拉取（唯一引入 net/http 的地方）
    exchange/             交易所日行情：结算价/收盘价（⚠️ 独立于柜台的那条通路）
    ~~snapshot/~~         ⚠️ **已并入 refdata（v0.1.0）**：内置快照就是一份
                          序列化的规则数据，读写它的 Save/Load 与 Provider 同住
                          refdata/persist.go。单开一个包只会让快照格式与它描述的
                          结构体分居两处。⚠️ go:embed 尚未接入，快照目前由
                          cmd/refdata-sync 生成、由调用方 Load

  ── 纯函数层：无状态，输入即输出 ──────────────────────
  fee/                  手续费：开 / 平昨 / 平今，按金额与按手数
  margin/               保证金：两层口径、多空分率、单向大边、交割月递增
  pnl/                  盈亏：逐日盯市与逐笔对冲两套口径

  ── 状态层 ──────────────────────────────────────
  position/             持仓状态机：今昨仓、逐笔明细、开平校验
  account/              资金账户：结存链条、可用、各类冻结
  order/                报单：校验、冻结、撤单
                        ⚠️ 八项校验已落地（v0.4.0 的一半）；冻结与撤单未做。
                        本包最要紧的一条：**跑不了的校验不许当成通过** ——
                        Check 返回 Result{Rejected, Unchecked}，而 OK()
                        要求全部查过
  match/                内置撮合：**通过八项校验的限价单，按自己的报价立刻 100% 全量成交**
                        （2026-09-09 使用者裁决「不做盘口」）。市价单、部分成交、成交角色**不建模**；
                        涨跌停与最小变动价位在 order 的八项校验里。见下方「match 的形状」
  ~~settle/~~           ⚠️ **已并入 position（v0.2.0）**：日终结算的两件事
                        （逐日盯市推进基线、今昨仓滚动）都要动逐笔明细，
                        而明细住在 position 里。单开一个包只能拿到
                        Side 的导出面，于是要么把 lots 导出、要么在两个包
                        之间来回搬 —— 前者破坏「明细是内部表示」，
                        后者是纯开销。实现见 position.Settle。
                        ⚠️ 结算单（settlement statement）是 v0.6.0 的事，
                        到时另说 —— 它是**陈述**，不是状态推进。
                        变更记录见 roadmap.md
  risk/                 风险度、追保判据、可选的约定强平策略

  conformance/          对拍：逐字段判定与四档验收
    fixture/            夹具：解析、重放、结转、与柜台截面逐字段比
    ctpfixture/         ⚠️ **CTP/SimNow 侧**的夹具单独一包：字段名、结构、可得的量
                        与 DIFF 那侧没有一个相同，共用会诱人复用那边的加载器，
                        而复用出来的东西「跑得通、但一个键都对不上」

  view/                 与 CTP 结构体、DIFF 业务截面字段级同构的视图

  internal/decimalx/    取整与舍入口径（金额、价格、手续费）

  cmd/refdata-sync/     生成内置快照（只用 stdlib + 本库，留在主模块）

  cmd/oracle/           ← 独立嵌套模块（自带 go.mod）：一切需要连柜台的工具
                        oracle = test oracle，**判定真值的那一方**，不是数据库
    go.mod
    main.go               两个子命令：oracle probe / oracle conformance
    probe/                判别实验的执行器（v0.1.0 的核心交付物）
    conformance/          双口子逐字段对拍（v0.2.0 起）
    kq/                   天勤 DIFF 客户端：WebSocket + RFC 7386 合并
    ctp/                  SimNow CTP 客户端。⚠️ 原写「含查询节流（goctp 缺这个）」——
                          **那个诊断 20260909 被复测推翻**：goctp 自己就有 1100ms 的
                          ticker，而合约表 2 秒拿到 16962 个。没有东西需要绕过，
                          这个包因此还没有存在的理由。见 probes.md §6.3
```

⚠️ **上面这棵树是计划，不是进度。** 哪些包已落地以
[state.md](./state.md) 的 `packages_done` 为唯一来源——本文不复述状态，
理由见 §7 与 state.md 的自指豁免那一节。

### 费率类型住在 refdata，不住在 fee / margin

`CommissionRates` 与 `MarginRates` 是**规则数据**，不是计算逻辑。
把它们定义在 `fee` / `margin` 里，`refdata` 返回费率时就得反过来 import 那两个包
——而依赖图是 `refdata ← {fee, margin, pnl}`，那样**就成环了**。

`fee.Rates` / `margin.Rates` 保留为**类型别名**，调用方两种写法都通。

### match 的形状：一笔单从「查过了」到「成交了」之间只有一步

⚠️ 这一节是**实现之前**写的（2026-09-14），它要先回答三个问题，否则实现会顺手替人裁掉。

#### 1. 入口是什么：`Fill(req, facts)`，而不是 `Fill(req, result)`

    match.Fill(req order.Request, facts order.Facts) (Trade, error)

`match` **自己调** `order.Validate`，不接收调用方传进来的 `order.Result`。
理由：一个 `Result` 与它对应的是哪一笔 `Request`，在类型上没有任何绑定 ——
拿 A 单的「通过」去撮合 B 单，编译器与测试都不会响。**让这种错误说不出口，比守着它强**
（与 `ctpValve(env)` 不再收腿、`dumpSlices` 收阶段枚举是同一个手法）。

判定照 `order` 包那条最要紧的规矩：

    Rejected != nil      ⇒ 不成交，错误里带拒因（哪一项、为什么）
    Unchecked 非空       ⇒ 不成交，错误里列出**没能查的每一项、缺什么**
    OK()                 ⇒ 成交

⚠️ 第二行是故意的：**「查不了」不当成「通过」**。回测里一笔因为缺可用资金而没查成的开仓，
若照样成交，就会开出实际开不出的仓（silent-risks 第 7 条）。调用方明知故犯时，
应当先把缺的事实补上，而不是让 `match` 替它放行。

#### 2. 成交成什么：价 = 自己的报价，量 = 全部，时刻 = 立刻

    Trade{Instrument, Direction, Offset, Hedge, Price = req.Price, Volume = req.Volume}

⚠️ 这是**裁决**，不是实测（roadmap「已裁决：`match` 不做盘口」）。偏离有两个维度、**方向相反**：

    价格维度      可成交的限价单实盘成交在**对手价**上（kq_facts 43：报涨停 3321 成交 3166）
                  本库按**自己的报价** ⇒ 买贵了、卖便宜了 ⇒ **保守**
    成交与否维度  本库假定**立刻全量成交**；挂在远离市场的限价单实盘**可能永远不成交**
                  ⇒ **乐观**，而且是危险的那一头（silent-risks 第 9 条）

⚠️ roadmap 对这条裁决的落地要求是：**两个方向都要写在导出面的文档注释里**，不埋在实现中。
⇒ 由 `match` 包自己的一条测试钉住（包文档里必须同时出现「100% 全量成交」「保守」「乐观」三处），
因为这段话在任何对拍上**都不会红** —— 夹具不留盘口，成交价逻辑写成什么样都不会被观测否掉。

#### 3. 它**不做**什么（每一项都写明为什么）

| 不做 | 为什么 |
|---|---|
| 改持仓、改资金 | `Trade` 是一条**成交记录**，不是状态推进。把它应用到 `position.Open/Close` 与 `account` 是门面（`futsim`，未落地）的事 —— 放进 `match` 会让它 import 状态层，而撮合规则与记账规则就长在了一个函数里，改一边要读两边 |
| 市价单 | `order.Request` 本来只有限价（「市价单的成交价由盘口决定，而本库没有盘口」）。本库**没有**假装能撮合它的地方 |
| 部分成交、FAK / FOK | 裁决是 100% 全量；部分成交需要深度，与「不做盘口」同源 |
| 成交角色（主动 / 被动） | 本库的手续费不按成交角色区分：`refdata.CommissionRates` 只按开 / 平昨 / 平今、按额 / 按手分档，柜台费率查询（`ctp-rates`）读到的字段也没有这一维。⚠️ 它是**按本库费率结构推得**，不是实测「柜台不分角色」—— 若将来有交易所按角色分档，要从费率结构那一层改起 |
| 涨跌停、最小变动价位 | 已在 `order` 的八项校验里，且那两项**能**被验证（`refdata.PriceLimits` 与实测的两家取整方向）。`match` 不重复查 |
| 裸 `CLOSE` 在 `UseHistory` 合约上 | `order` 的可平量校验对它报错（simnow_pending#1 未裁决），于是它走不到 `match`。⚠️ 2026-09-15 F6a 起这句只在 **CTP 口径（门面 `Choices.UndatedCloseOnUseHistory` 零值）** 下成立：快期口径下门面的 `validate` 先把它改写成平昨，**走得到** `match.Fill`（按平昨成交、成交记录保留 `CLOSE`）；`match` 本身仍不看口径 |

⚠️ 这张表里**「不做」与「做不了」要分开读**：前三行是**裁决与分层**（换个口子也不做），
第四行是**按结构推得**（有反例就要做），后两行是**已经在别处做了**（最后一行自 F6a 起是「CTP 口径下在 `order` 里拒、快期口径下在门面里改写」—— 仍不在 `match` 里做）。

### ctperr 的形状：码只从语料来，而一个码有三个坐标

⚠️ 这一节是**实现之前**写的（2026-09-15）。roadmap 的约束是「`ctperr` 的取值一个都不许先填」——
那条约束现在第一次**能**满足了：`testdata/refdata/ctp-reject-codes.json` 有三个交易所拍下来的 12 条观测。
而拍语料的几个夜盘已经把形状定死了，下面每一条都有出处。

#### 1. 一个码有三个坐标：交易所 × 拒因 × 码空间

    type Space uint8        // 码出自哪一处
      SpaceCTP             RspInfo.ErrorID（柜台错误码）
      SpaceStatusPrefix    OrderStatus 的 StatusMsg 文本前缀 `NN:`（而这时 ErrorID 是 0）
    type Code struct{ Space Space; Value int }

    Lookup(ex types.Exchange, r Reason) (Code, bool)

- **码空间是必需的一维**（state.md 20260910）：`CTP 50 = 平今仓位不足`，`前缀 50 = 价格跌破跌停板` ——
  同一个数、两个意思。只有一个整数的表会把两者并成一条，**而它不报错，只会在某一天把一个拒因解释成另一个**。
- **交易所是必需的一维**（§13 #16 / #6）：同一情形的码随交易所变 —— 平昨超过昨仓，大商所 `30`、上期所与能源中心 `51`。
- ⚠️ `SpaceStatusPrefix` **不叫「交易所码」**：§13 #6 登记了一个没排除的替代解释 —— 价格类检查可能在**柜台前置**就拒了、
  没进交易所。那样三所「同号」只说明同一层检查给同一个码。⇒ 名字只说**码写在哪**，不说**码出自哪一层**；
  语料从下一轮起记 `has_order_sys_id`，那一栏为 `true` 的观测才能把码归给交易所。

#### 2. 拒因是 ctperr 自己的枚举，而且**只到语料测到的粒度**

    type Reason uint8
      ReasonPriceTick                 价格不是最小变动价位的整数倍
      ReasonAboveUpperLimit           价格高于涨停
      ReasonBelowLowerLimit           价格低于跌停
      ReasonCloseYesterdayExceeds     平昨手数超过昨仓（语料是「账上无仓时平昨」）

- **不 import `order`**：包图写的是 `types → ctperr`。拒因与 `order.Check` 的对应是**调用方**的事，
  而且一对多（`CheckPriceLimit` 分涨停 / 跌停两个码；`CheckClosable` 按平今 / 平昨分）。
- ⚠️ **粒度只到语料**：语料里「可平量」那条是**平昨**、**账上无仓**。平今超量、有仓但不够的码**没有观测** ——
  所以没有 `ReasonCloseTodayExceeds`，也不把平昨的码推给平今（20260910 提交正文里记过 `CTP 50 平今仓位不足`，
  **但那只活在提交正文里，没进语料** —— 与 #13 被打回同一个毛病，不收）。

#### 3. 表与语料**双向**钉死

    表里每一格   ⇒ 语料里至少一条 outcome=rejected、violates 恰好一项、交易所与拒因对得上、码一致的观测
    语料里每一条 rejected 且单一违反的观测 ⇒ 表里有对应格且码一致

⚠️ 两个方向缺一不可：只查前者，漏填的格（语料有、表没有）永远不响；只查后者，**手打进表的格**永远不响 ——
而后者正是「取值不许先填」要防的那一个。测试读 `testdata/refdata/ctp-reject-codes.json`（主模块的测试可以读 testdata，
运行时代码不读：表是 Go 代码，`go get` 的使用者拿不到 testdata）。

#### 4. 查不到就说查不到

`Lookup` 对**没测过**的组合（郑商所 / 广期所、任何交易所的平今）返回 `false`，**不回落到「最常见的那个码」**。
一个回落的查表，会让「郑商所的跌停码是 50」这句从没观测过的话，在调用方那里看起来和测过的一样。

#### 5. 它**还不做**的

| 不做 | 为什么 |
|---|---|
| ~~从 `order.Result` 映射到码~~ | ✅ 2026-09-15 接上，见下面第 6 条 |
| 错误文案 | `StatusMsg` 是柜台自由文本，按纪律不进库；ctperr 只有码 |
| 快期（DIFF）那侧的码 | 那是天勤自己的码空间（cn-futures-rules.md 那一节写明「填不了 `ctperr`」） |

#### 6. 与 `order` 的接线：拒因在**拒绝的那一刻**写进 `Rejection`（2026-09-15，实现之前写）

    order.Rejection{Check, Reason string, Kind ctperr.Reason}      // 新增 Kind 字段（Reason 已是中文说明）
    match.RejectedError{Rejection, Exchange types.Exchange}        // 新增 Exchange
    func (e *match.RejectedError) Code() (ctperr.Code, bool)       // = ctperr.Lookup(Exchange, Rejection.Kind)

- **在 `Validate` 里写，不由调用方事后拆**：拒绝的那一刻 `Validate` 手里有涨停价与跌停价、开平标志、今昨手数；
  调用方拿到的只剩一个 `Check` 与一句中文 —— 从中文里解析涨停还是跌停，就是在拿自由文本做判断。
- **粒度仍只到语料**：
  - `CheckPriceTick` ⇒ `ReasonPriceTick`；`CheckPriceLimit` 高于 / 低于 ⇒ `ReasonAboveUpperLimit` / `ReasonBelowLowerLimit`
  - `CheckClosable` **平昨、且账上无仓（今 0 且昨 0）** ⇒ `ReasonCloseYesterdayExceeds`
  - ⚠️ 平昨而有今仓、昨仓**有但不够**、平今、裸 `CLOSE`，以及其余五项 ⇒ `ReasonUnknown`（语料只测过「账上无仓时平昨」）
  - ⚠️ 评审 20260915 打回过「只看昨仓为 0」：大商所跨结算后本库记作今仓、CTP 却接受平昨，那一版会给柜台接受的单配上码
- **验收那一行怎么核**（「被拒报单的错误码一致」）：对语料里每一条单一违反的拒单，在 `order` 这边构造同一种违反，
  断言 `Validate` 拒在同一项、`Kind` 查出来的码与语料的码一致。⚠️ 它核的是「本库给这一种违反挑的拒因」与柜台对得上 ——
  ctperr 的表本身来自同一份语料，所以**码值**这一半是同源的，不是独立验证；独立的是「拒因挑得对不对」那一半。

### 门面的形状：`futsim.Simulator` 把成交记进持仓与资金

⚠️ 这一节是**实现之前**写的（2026-09-15）。`match.Trade`、`position.MeasuredCloseOrder`、`account.Algorithm` 都已落地，
缺的是把它们串起来的那一处 —— 而那一处此刻**只存在于** `conformance/fixture.Rebuild`（对拍的支撑代码）里。
一个库的记账链条只活在对拍工具里，就是第二份实现迟早要出现的形状：门面照着写一份，两份一起退化时对拍全绿。

#### 1. 口径全部由调用方显式选，零值报错

门面要做的每一处「按哪个价 / 怎么合并 / 算不算」，本库都已有一个零值即「未实测」的枚举。门面**不替调用方选**：

    type Choices struct {
        FeeBasis    fee.PriceBasis      // 按额手续费按哪个价   ← 新增（F1）
        FeeRounding fee.Rounding        // 手续费取整           ← decimalx.Rounding 的导出别名（F1）
        MarginBasis margin.PriceBasis   // 保证金按哪个价
        SideScope   margin.SideScope    // 单向大边的合并范围
        Mark        pnl.Mark            // 盘中持仓盈亏的计价价
        Algorithm   account.Algorithm   // 浮盈算不算进可用
        FreezeMargin order.FreezeMarginBasis // 开仓挂单冻结保证金按哪个价（F3 加，见 §7）
    }

| 项 | CTP / SimNow 实测 | 快期模拟实测 |
|---|---|---|
| `FeeBasis` | **成交价**：平昨 @3137 收 3.142（昨结算 3147 ⇒ 3.152，否）。⚠️ 以行为费率「按额 0.0001 + 每手 0.005」为前提，而那个 0.005 声明里没有（§13 #19，开着） | **昨结算价**（kq_facts 4，`cu2701` 八个成交价一个费额） |
| `FeeRounding` | ⚠️ **未收敛**（§13 #5：两边都排除了「到分」，剩下两个候选在现有费率结构下给同一个数） | 同左 |
| `MarginBasis` | `OpenTodayPreSettleHistory`（§13 #1） | `PreSettleAll`（`Rebuild` 现用；实现时核过判别力：换成今仓按开仓价，快期夹具 margin 差 22.4，破坏 518） |
| `SideScope` | `ByProduct`（§13 #3） | ⚠️ 快期**没实现大边**（simnow_pending#6）—— 这一项在那个口子上测不了 |
| `Mark` | `MarkLast`，基线是结算推进后的 `Basis`（§13 #2） | `MarkLast`（`Rebuild` 现用；换成昨结算价时 position_profit 700 → −320，破坏 519） |
| `Algorithm` | `AlgorithmOnlyLost`（§13 #17） | `AlgorithmAll`（kq_facts 11） |
| `FreezeMargin`（F3 加） | `FreezeAtOrderPrice`（`ctp-frozen-20260910`，见 §7） | `FreezeAtPreSettlement`（kq_facts 46） |

⇒ 提供两个**预设** `CTPChoices()` / `KQChoices()`，**只填实测过的格**；`FeeRounding` 在两个预设里都留零值，
`New` 因此报错，调用方必须自己写一行 `c.FeeRounding = …` —— **那一行就是「我知道这一项没实测」的签字**。
快期预设的 `SideScope` 同理留空（测不了不等于测过）。
⚠️ 预设不是「推荐配置」，是「这个口子上量到的是什么」；一条测试钉住预设里每个非零格都在上表有出处、零值格恰好是表里标 ⚠️ 的那几格。

⚠️ `decimalx` 是 internal 包：`fee.Compute` 的导出签名里有 `decimalx.Rounding`，模块外的调用方**点不出它的常量**。
门面的 `Choices` 若照搬，外部根本填不了 —— 所以 F1 先在 `fee` 包里给出 `type Rounding = decimalx.Rounding` 与常量别名。

#### 2. 入口与链条（F1：灌成交路径）

    func New(cfg Config) (*Simulator, error)      // Config{Day, PreBalance, Rules refdata.Provider, Choices}
    func (s *Simulator) Deposit / Withdraw(day, amount) error
    func (s *Simulator) Mark(day, Quote) error    // Quote{Instrument, Last/HasLast, PreSettlement/HasPreSettlement}
    func (s *Simulator) ApplyTrade(day, match.Trade) error
    func (s *Simulator) Account() account.Snapshot
    func (s *Simulator) Position(types.InstrumentID, types.HedgeFlag) (*position.Position, bool)   // 返回副本

`ApplyTrade` 一步之内：

    ① 查齐输入   合约规格、费率、保证金率（refdata）；昨结算价、计价价（Mark 给过的）
                 —— **任何一样缺就报错，状态一点不动**
    ② 持仓       开 ⇒ position.Open
                 平今 / 平昨 ⇒ position.Close 直传
                 裸 CLOSE 与强平标志 ⇒ MeasuredCloseOrder(合约的 PositionDateType)；没有实测顺序 ⇒ 报错（UseHistory 上 simnow_pending#1）
    ③ 手续费     fee.Compute(按 FeeBasis 取价) ⇒ account.AddCommission
    ④ 平仓盈亏   pnl.CloseProfit(被消耗的明细).ByDate ⇒ account.AddCloseProfit（逐日盯市口径进结存，§5）
    ⑤ 重算截面   **全部**持仓 ⇒ margin.Compute(MarginBasis, SideScope) ⇒ account.SetMargin(公司, 交易所)
                 全部持仓 ⇒ pnl.PositionProfit(Mark) 求和 ⇒ account.SetPositionProfit
    ⑥ account.Check()

- ③ ~~裸 `CLOSE` 的手续费按②实际消耗的今昨**拆两档**：昨仓部分走平仓档、今仓部分走平今档~~（F1 当时标「推得」）
  ⚠️⚠️ **2026-09-15 被 #4 夹具否掉**（§13 #21）：`DCE.m2701` 通用平仓消耗了昨仓，柜台收的是**平今档** 0.1，不是平昨档 0.2。
  现在的做法：平仓量 ≤ **平仓前的今仓量**的那一段全走平今档（§13 #21 的候选 a、b 在这一段一致；
  ⚠️ 候选 c「行为平昨费率 ≠ 声明」未排除，这一段只在 m2701 上有观测）；
  超出的部分，平今档与平昨档声明费率相同时放行，不同时**报错**。强平标志同一支
- ⑤ 必须是**全部**持仓而不是这一笔的合约：`ByProduct` 下同品种另一个月份的反向仓会被这一笔改变占用
- ⚠️ **不许留半截状态**。做法：②–⑤ 全部在**持仓的副本**上算（`position.Position.Clone`，F1 新增），
  手续费、平仓盈亏、占用、持仓盈亏四个数都算出来之后，才把副本换进去、把四个数写进账户。
  任何一步缺输入（费率、保证金率、昨结算价、计价价 —— `Mark` 没给过就报错，不拿成交价顶）都在换进去之前失败。
  一条测试钉住：一笔失败的 `ApplyTrade` 之后，持仓与账户快照与调用前逐字段相同
- ⚠️ 换进去之后写账户那几步**仍然**可能失败（按现有代码只有「交易日不对」一条路，而它在①就查过 —— 但不是类型保证的）：
  模拟器进入**失效态**，此后每个动词都返回那个错误。⚠️ 不回滚、不假装没发生 —— 半截状态上继续记账，
  后面每一个数都错而且没有任何报错；让它停下是唯一不静默的做法
- 计价价与昨结算价由 `Mark` 给，按合约存；昨结算价一个交易日内不变，`Mark` 收到与已存值不同的昨结算价 ⇒ 报错
- ⚠️ 灌进来的成交**不跑八项校验**（§4「两条并存的路径」）：它被当成柜台已经接受的事实。超量平仓照样报错 —— 那不是校验，是账算不下去
- ⚠️ §13 #18：账户级持仓盈亏与各持仓之和**口径相同**（差额是两次请求不同瞬），所以⑤求和是对的；对拍时两组字段要来自同一次请求

#### 3. 分期

| 期 | 内容 | 验收 |
|---|---|---|
| **F1** | 上面这些：`Choices` 与两个预设、`fee.PriceBasis`、`fee.Rounding` 别名、`New` / 出入金 / `Mark` / `ApplyTrade` / 查询 | 见 §4 验证 |
| F2 | `Settle(day, 结算价表, nextDay)`：全部持仓 `position.Settle` ⇒ 按结算价重算占用 ⇒ `account.Settle`。缺任何一个持仓合约的结算价 ⇒ 报错，不拿昨结算价顶 | 跨日结转夹具（快期 crossday、CTP 过夜种子）逐字段 |
| F3 | 报单路径：`Submit(day, order.Request)` 组装 `order.Facts`（`Available` 取 `account.Available()`、`Need` 由 `order.FreezeOf`）⇒ `match.Fill` ⇒ `ApplyTrade` | 被拒单的码与 ctperr 语料；冻结额与 frozen 夹具 |
| F4 | 挂单（引擎自己撮合时）：`order.Book` + `account.Freeze` / `Unfreeze` + 撤单；`State()` / `Restore()` | 冻结往返差 0（§13 #9） |

#### 4. 验证（F1）—— 两个口子各一条，都从生产的门面出发

- **快期**：`Rebuild` 改成调门面（`KQChoices()` + 显式 `FeeRounding = NoRounding`、`SideScope` 沿用 `ByInstrument`）。
  ⚠️ **不做「门面与 Rebuild 逐字段一致」那种过渡测试**：两份实现并存期间比对彼此，恰好就是本节开头说的那个形状。
  直接替换，`TestRebuildAccountFieldByField` 等既有对拍从此钉的是门面；替换前后各跑一次破坏验证，数不许变少
- **CTP**：交易日 20260915 的 `ctp-slices-20260915{,-2..-9}`。⚠️ 每份截面**只附本轮那个合约**的当日成交
  （-2/-3 是 `DCE.m2701` 的 2 笔，-4 起是 `SHFE.ag2702`，-9 带齐它全天 9 笔）。
  - ⚠️ **实现时改了**（原写「从过夜的 m2701 昨仓起点合并两个合约重放、比账户平仓盈亏」）：F1 没有结算，造不出昨仓；
    而 ag2702 的 9 笔从空仓开始、自成一段。⇒ `TestFacadeReplaysAg2702AgainstCTP` 只重放 ag2702，
    在 -4..-9 六个时点比**那一条持仓记录**的六个字段：手数、`OpenCost`、`UseMargin`、`PositionProfit`、`CloseProfit`、`Commission`
  - 全部取自**同一条记录**（同一次请求）：持仓盈亏的计价价用记录自己的 `SettlementPrice`（盘中柜台就用它算），
    不用行情快照 —— 两次请求中间价会动（§13 #18；-6 那份记录 15459、行情 15456）
  - ⚠️ **账户级字段不比**：账户手续费累计里有本批截面之外的合约（-3 → -4 之间只有一笔 ag 开仓，账户手续费涨了 2.52355、那一笔只该 2.324），
    账户平仓盈亏里有 m2701 的 −240
  - ⚠️ **手续费**：柜台比本库**恰好**多 0.005 × 成交手数（§13 #19，声明费率里没有这个每手常数；本样本每笔 1 手，与笔数同值）。
    测试钉的是「差恰好是这个常数」—— #19 收敛或本库手续费错一分都会红；**不是**「一致」
  - 首跑 6 份 × 6 字段全部对上；破坏 515（保证金基准）/ 516（手续费基准）/ 517（平今按后开先平）各自红在对应字段

#### 5. 它**不做**的

| 不做 | 为什么 |
|---|---|
| 替调用方选任何口径 | 两个口子量到的值相反的就有三项（手续费基准、保证金基准、浮盈算法）|
| 灌成交路径上的八项校验 | 那是 F3 报单路径的事；灌进来的成交是已发生的事实 |
| 从最新价推结算价、从昨结算价顶结算价 | §6.5：结算价是交易所的结算结果，推不出来 |
| 强平执行 | 决策 11 |

#### 6. F2：`Settle` —— 一个交易日在门面里结束（2026-09-15，实现之前写）

    func (s *Simulator) Settle(day types.TradingDay, prices map[types.InstrumentID]decimal.Decimal, nextDay types.TradingDay) error

`prices` 是**今结算价**，按合约给；`nextDay` 由调用方的日历给（§6.5：本库不推算交易日）。

##### 一步之内的顺序（§4 那条链，门面这一版）

    ① 查齐    day 是当前交易日、nextDay 晚于它；每一个**有仓**的合约都有结算价且为正
              —— 缺一个就报错，不拿昨结算价 / 最新价顶（§6.5：结算价推不出来）
              多给的（没仓的合约）忽略
    ② 兑现    按**今结算价**算全部持仓的持仓盈亏（与 Choices.Mark 无关：日终计价只有这一个价，pnl.Mark 的文档如此写）
              ⇒ account.SetPositionProfit ⇒ account.Settle（上日结存 = 结存；平仓盈亏、手续费、出入金清零；持仓盈亏归零）
    ③ 滚仓    每个持仓在**副本**上 position.Settle(day, 结算价, nextDay)：今仓 → 昨仓、基线 → 结算价（§13 #20 裁决后两种 PositionDateType 同一条路）
              空仓的持仓对象直接丢掉
    ④ 次日计价 每个合约：昨结算价 = 刚用的结算价；最新价 = 结算价（结算那一刻的计价价就是它，持仓盈亏因此为 0，与 ② 的归零一致）
    ⑤ 次日占用 按新基线、新昨结算价重算 ⇒ account.SetMargin(nextDay, …)
              （CTP 实测：结算后昨仓占用 = 昨结算价 × 乘数 × 率，§13 #1；#4 夹具 ① UseMargin 4737.6 = 3384 × 10 × 0.14）

- 实现时补的一步：`account.Settle` 之前先把**结算那一刻**的占用写进账户（按结算价计价的那次重算），让结算前的账户快照自洽；次日占用在 `Settle` 之后另写
- ⚠️ **原子性**：①③④⑤ 的计算全部在副本上先做完；账户那几步（SetPositionProfit → Settle → SetMargin）排在最后，
  前置条件（交易日、冻结额为零）在 ① 里先查。之后仍失败 ⇒ 失效态（F1 同一条规矩）
- ⚠️ **④ 之后次日第一个 `Mark` 若带着与结算价不同的昨结算价 ⇒ 报错**。这是 F1 那道「同日昨结算价不许变」的守卫在跨日上的延伸，
  而它顺带抓住一种静默错：**调用方喂给 `Settle` 的结算价与行情源次日给出的昨结算价不是同一个数**（例如拿收盘价当结算价）
- ⚠️ **冻结**：F2 仍没有挂单。`account.Settle` 带着冻结额报错那条保留，F4 再说

##### 两个手写 Provider 的 `PositionDateNotNeeded`（评审 F1 风险 3）

`conformance/fixture.specRules` 与 ctpfixture 的 `oneInstrumentRules` 都给 `PositionDateNotNeeded`，前提是那两条路**永不结算**。
F2 之后门面**能**结算了 ⇒ 若有人在这两条路上调 `Settle`，`position.Settle` 会报「NotNeeded 不许结算」—— 那道关口本来就在。
⇒ **不改这两个 Provider**；F2 加一条测试钉住「NotNeeded 的持仓上 Settle 报错、状态不动」，让前提从注释变成断言。

##### 验证

**CTP（#4 夹具，DCE.m2701 跨 20260914 → 20260915）**：交易日 20260914 开多 1 @3399（`OpenCost` 33990），按 3384 结算，次日重放 -2（开今 @3360）/ -3（`OF_Close` @3360），
逐份比 `DCE.m2701/2/1` 那条持仓记录：

    ''   结算后、次日开盘前：Position 1 / PositionCost 33840（基线推进）/ UseMargin 4737.6（昨仓按昨结算价）/ PositionProfit −240（计价价用记录的 SettlementPrice 3360）
    -2   开今之后：Position 2 / PositionCost 67440 / UseMargin 9441.6 = (3384 + 3360) × 10 × 0.14（今仓按开仓价、昨仓按昨结算价）
    -3   通用平仓之后：CloseProfit −240（消耗昨仓，#4）/ PositionCost 33600 / UseMargin 4704 / Commission 0.3（开 0.2 + 平 0.1；§13 #21 三个候选都预言 0.1，所以这一格钉住的是「本库在收敛前的做法与这一笔一致」，不判别候选）

⇒ 这一条同时钉住：结算滚仓、基线推进、昨仓保证金基准（CTP 预设的 MarginBasis 第一次在昨仓上被判别）、#4 消耗顺序，以及 #21 那一笔的手续费。
⚠️ 同时把 F1 的白盒 `seedHistory` 换掉：`TestUndatedCloseFeeTier` 改成真走 `Settle` 造昨仓。
⚠️ 20260914 那一侧只需要「开仓价 3399」与「结算价 3384」两个数；开仓那天的昨结算价、最新价只影响当日的持仓盈亏与手续费，不进这条比较。
保证金率取 -2 那条记录的 `MarginRateByMoney` 0.14（昨仓记录报 0，§13 #1 那一格记着）。

**⚠️ 账户级跨日（上日结存）不进这条对拍 —— 两对跨日截面都有一个解释不了的正差**：

    20260910 → 20260911   按结算价推出的结存 19999915.293   次日 PreBalance 19999915.32    差 +0.027
    20260914 → 20260915   19997481.3415                    19997481.46                   差 +0.1185

平仓盈亏只按「跳 × 乘数」的整数倍变、手续费只增不减 ⇒ 两个差都不来自结算前还有成交。候选：
(i) **结算时逐笔手续费重新取整到分**（盘中不取整，§13 #5；截断与四舍五入在已知的几笔上都给同向的小差）；(ii) 别的结算项（利息等，账户里那几个字段全为 0）。
⚠️ 两个日子都拿不到当日**全部**逐笔成交（status 夹具不带成交），⇒ 分不开，登记进 §13 #5，不进 F2 的验收。

**快期**：跨日结转对拍已有（`conformance/fixture/carry` 一族），F2 不改它们的实现；是否改走门面，等 F2 过审后单独一批（与 F1 替换 `Rebuild` 同一个手法、同一条「前后逐字段 diff 为空」的判据）。

##### 夜盘实验（登记，不在本批）

- **§13 #21**：E1 只有昨仓时裸平 1 手、E2 只有昨仓时显式平昨 1 手（分 a / b / c，见 §13 #21），大商所种子留 2 手、要先隔一次结算
- **§13 #5 结算取整**：当日做几笔「第三位小数截断与四舍五入结果不同」的成交，**拍下当日全部逐笔成交**与收盘后账户，次日读 PreBalance。
  工具缺口：现有 dump 只挂本轮合约的成交（`AttachTrades` 按合约），要一个按交易日取全部成交的出口

#### 7. F3：`Submit` —— 一笔报单过八项校验、按裁决成交、记账（2026-09-15，实现之前写）

    func (s *Simulator) Submit(day types.TradingDay, at time.Time, req order.Request) (match.Trade, error)
    func (s *Simulator) FreezeOf(day types.TradingDay, req order.Request) (order.Frozen, error)

链条：组装 `order.Facts` ⇒ `match.Fill`（它自己调 `order.Validate`）⇒ `ApplyTrade`。
被拒返回 `*match.RejectedError`（带 `Code()`，语料粒度内能说出柜台的码），没查成返回 `*match.UncheckedError`，调用方用 `errors.As` 分。
⚠️ 「没查成不成交」不改（§3「match 的形状」第 1 条：调用方该补事实，不该让 match 放行）⇒ **F3 的主要工作是让门面能把八项的事实都补上**。

##### 八项事实从哪来

| 项 | 事实 | 来源 | 缺时 |
|---|---|---|---|
| 可交易 | `Instrument` | `Rules` | 查不到 ⇒ 可交易 / 价位 / 涨跌停 / 手数四项都没查成（`order` 现有处理） |
| 时段 | `InSession` | **新增** `Config.Calendar`（`refdata.Calendar`）+ `Submit` 的 `at` | 见下 |
| 最小变动价位 | `Instrument.PriceTick` | `Rules` | — |
| 涨跌停 | 昨结算价 × `PriceLimitRatio`，按取整方向对齐 | `Mark` 给的昨结算价；**新增** `Config.TickRounding`（按交易所） | 没给该交易所 ⇒ 没查成 |
| 手数上下限 | `Instrument.Min/MaxLimitOrderVolume` | `Rules` | 上限为 0 ⇒ 没查成（`order` 现有处理：免费行情不下发） |
| 可平量 | 门面自己的持仓（没仓就给一个**空**持仓，不给 nil —— nil 在 `order` 里是「不知道」） | 门面 | — |
| 资金 | `Available` = `account.Available()`；`Need` = `FreezeOf` 的保证金 + 手续费 | 门面 | 规格在而算不出（缺昨结算价、§13 #21 分歧段）⇒ **直接报这个原因**（实现时改：塞进「没查成」只会说「保证金与手续费」，把真正缺的东西说丢了） |
| 限仓 | **新增** `Config.PositionLimits map[InstrumentID]int` | 调用方 | 没给该合约 ⇒ 没查成 |

- **时段**：`TradingDayAt(at, …)` 成功且等于 `day` ⇒ 在时段内；成功而不等于 `day` ⇒ **报错**（时刻与交易日互相矛盾，是调用方的错，不是拒单）；
  落在任何时段之外 ⇒ 不在时段内（拒）；没有该品种的时段表等 ⇒ 没查成。
  ⚠️ 这要 `refdata` 把「时段之外」与「查不了」分开：新增哨兵 `refdata.ErrOutsideSession`（`%w` 包着），不靠报错文案分。
  ⚠️ kq_facts 48：**快期自己不查时段**（313 笔里 217 笔落在时段外照收）⇒ 按快期口子对拍时这一项是盲区，不是待办
- **涨跌停取整**：`MeasuredTickRounding()` 只给实测过的两家 —— 上期所向下、大商所四舍五入（probes.md §12，七个合约反解）。能源中心 / 郑商所 / 广期所 / 中金所**不填**
- **限仓**：本库没有这份数据。⚠️ 回测调用方要自己给（给一个足够大的数也是一种声明，而且是调用方签的）；不给就不成交 —— 与「取整口径两个预设都留空」同一个手法

##### 冻结额：新增第七项口径 `FreezeMarginBasis`

开仓单冻的保证金按哪个价算，**两个口子实测相反**：

    CTP / SimNow  挂单价     ctp-frozen-20260910：LongFrozenAmount 30050 ⇒ 挂单价 3005；FrozenMargin 4808 = 3005 × 10 × 0.16
                              （按昨结算价 3164 会是 5062.4，否；按记录里的计价价 SettlementPrice 3146 会是 5033.6，也否 —— 评审 20260915 补）；
                              同一份的 FrozenCommission 3.01 = 3005 × 10 × 0.0001 + 0.005，也指向挂单价
    快期模拟      昨结算价   kq_facts 46：ag2702 昨结 16262 × 15 × 22% = 53664.6、i2701 昨结 740 × 100 × 11% = 8140，报单价都远低于昨结算价

⇒ `order.FreezeMarginBasis{Unmeasured, OrderPrice, PreSettlement}`，进 `Choices`，两个预设各填各的。
算法走 `margin.Compute` 的一条腿（今仓腿：`OpenPrice` = 挂单价 + `OpenTodayPreSettleHistory`；或昨结算价 + `PreSettleAll`），不另写「名义额 × 费率」—— 那是第二份实现。
冻结手续费：按 `FeeBasis` 取价（挂单价顶成交价的位置），档位与 `ApplyTrade` 同一个 `chargeUndated`（§13 #21 的限制一并继承）。

##### 它不做的（F3 范围外）

| 不做 | 为什么 |
|---|---|
| 挂单、撤单、冻结记账 | F4。按裁决（100% 立刻全量成交）`Submit` 要么成交要么不成交，没有「挂着」这个状态 —— `FreezeOf` 在 F3 只用来算 `Need` |
| 市价单、部分成交 | `match` 的裁决 |
| 快期那侧冻结的第二份实现（`conformance/fixture/frozen.go`） | 它自己算冻结额；迁到门面的 `FreezeOf` 是 F4 与 `Rebuild` 的 HasOrders 分支一起的事 |

##### 验证

- **CTP 冻结**：`FreezeOf` 按 CTP 预设对 `SHFE.rb2701` 买开 1 手 @3005 ⇒ 保证金 4808、手续费 3.01，比 `ctp-frozen-20260910` 的账户冻结字段（那一刻账上只挂这一笔，`LongFrozenAmount` 30050 是它的委托额）
- **码**：门面 `Submit` 在合成规则数据上逐条造出语料里的 4 种拒因 × 3 个交易所，`RejectedError.Code()` 等于语料的码 —— 核的是**门面组装的事实**让 `order` 拒在同一项（`order` 包那条测试用的是手写 `Facts`）
- **时段**：用 `sessions-20260908.json` 的真实时段表：时段内成交、时段外拒在 `CheckSession`、没有时段表的品种没查成、时刻与交易日矛盾报错
- **成交之后**：`Submit` 成交的账与 `ApplyTrade` 同一笔成交的账逐字段相同（两条路只在「成交之前」不同）
- **没查成不成交**：不给限仓 / 不给取整方向 / 不给日历，各自返回 `UncheckedError` 且点名缺什么，状态不动

#### 8. F4：挂单 —— `Place` / `Cancel` / `Fill`（2026-09-15，实现之前写）

F3 的 `Submit` 按裁决「通过即立刻全量成交」，没有「挂着」这个状态。F4 给**自己撮合的引擎**用：单子先挂上、冻结，何时成交由引擎决定。

    func (s *Simulator) Place(day types.TradingDay, at time.Time, id string, req order.Request) (order.Frozen, error)
    func (s *Simulator) Cancel(day types.TradingDay, id string) error
    func (s *Simulator) Fill(day types.TradingDay, id string) (match.Trade, error)
    func (s *Simulator) Live() []string

##### 语义

- **`Place`**：与 `Submit` 同一套事实组装（抽成一个方法，两处共用）⇒ `order.Validate`；被拒 / 没查成与 `Submit` 同样返回
  `*match.RejectedError` / `*match.UncheckedError`；通过 ⇒ `FreezeOf` ⇒ `account.Freeze`（保证金 + 手续费）⇒ `order.Book.Insert`。**不成交**
- **`Cancel`**：`Book.Remove` ⇒ `account.Unfreeze`。不在簿上报错（`Book` 已有这条）
- **`Fill`**：一笔挂单**全量**成交，**价 = 挂单价**（与 `match` 的裁决同一句：不做盘口、100% 全量；引擎决定的只是「何时」）
  ⇒ `Book.Remove` ⇒ `Unfreeze` ⇒ `ApplyTrade`。`ApplyTrade` 失败（例如 §13 #21 分歧段）⇒ 挂单与冻结**原样放回**，放不回则失效态
- ⚠️ **裸 CLOSE 在多笔挂单之间**（评审 20260915 打回后补）：冻结时的今昨拆分与成交时的消耗用同一个 `undatedSplit` —— 在**扣掉挂单冻住之后**的今昨里先平昨。
  原版冻结拿持有的昨仓拆（两笔裸平都冻昨 1）、成交按先平昨在整份持仓上消耗（先成交冻今的那笔去平别人冻住的昨仓），两处都会让挂得上的单成交不了。
  ⚠️ 有挂单时「在可平量里先平昨」是推得（§13 #4 只观测过没有挂单的情形）。⚠️ 已知陷阱：m2701 这类平今档 ≠ 平昨档的合约上，
  两笔裸平先成交平今的那笔之后，另一笔面对「只有昨仓的裸平」撞 §13 #21 分歧段 —— 挂得上、成交报错、留在簿上，只能撤
  ⚠️ **挂单时报出的今昨拆分是那一刻的投影，不随别的挂单撤销 / 成交重拆**（评审 20260915 走出的具体形状）：
  今1昨1 挂裸平 a（冻昨 1）、裸平 b（冻今 1），撤掉 a ⇒ 昨仓空出来了，而 b 仍冻着今 1 ⇒
  此时**显式平今被拒在可平量、显式平昨放行**，b 成交时按当时的可平量重拆（照样成交）。方向是保守的（多拒不多放）。
  要治得在撤单 / 成交后把剩下的裸 CLOSE 挂单按挂单顺序重拆一遍，而簿现在不记挂单顺序 —— 登记，不做
- ⚠️ **冻结的手续费与成交时收的手续费是两次计算**：挂单价 = 成交价时两者相等；本库成交价恒等于挂单价，所以相等是结构保证的，测试钉一格

##### 要改的地方

1. **`order.checkClosable` 扣掉已挂平仓单冻住的手数** —— 现在不扣，两笔平仓挂单能超出持仓。
   `order.Facts` 加 `Frozen order.Frozen`（被平那一侧已冻的今 / 昨手数，门面从 `Book.TotalOf` 取）；
   平今 ≤ 今仓 − 已冻今、平昨 ≤ 昨仓 − 已冻昨、裸 CLOSE ≤ 总仓 − 已冻总。`Submit` 也传它（挂着的平仓单同样占着可平量）
2. **`Settle` 在簿上有挂单时报错**：`account.Settle` 本来就拒绝带冻结结算；门面在前面先报「先撤单」。
   ⚠️ CTP 的当日有效单在收盘后由交易所撤销 —— 那是**文档**，本库没有观测；不替调用方自动撤
3. **`ApplyTrade`（灌成交）不许平掉挂单冻住的手数**：平完之后被平那一侧的剩余今 / 昨仓必须 ≥ 已冻的今 / 昨手数，否则报错、状态不动。
   灌进来的成交是柜台已接受的事实，柜台不会让它与挂单冲突；冲突只可能是调用方把挂单成交走了 `ApplyTrade` 而不是 `Fill`
4. **限仓**：挂着的开仓单算不算进限仓 —— **没有观测**。F4 不算（与 `order` 现有 `openVolumeAfter` 一致），登记

##### 验证

- **CTP 冻结三件套**（账户冻结字段 + 持仓记录的冻结手数），门面走 `Place`：
  - `ctp-frozen-20260910`：空仓，买开 1 @3005 ⇒ `FrozenMargin` 4808、`FrozenCommission` 3.01（本库按声明费率少 0.005，§13 #19）
  - `ctp-frozen-20260911`：rb2701 昨 1（20260910 开 @3148、按 3147 结算），卖出**平昨** 1 @3304（委托额 33040）
    ⇒ `FrozenMargin` 0、`FrozenCommission` 3.309（少 0.005）、多头记录 `ShortFrozen` 1（门面：多头侧已冻昨 1）
  - `ctp-frozen-20260914`：rb2701 今 1（@3105），卖出**平今** 1 @3272（委托额 32720）⇒ `FrozenCommission` 3.277（少 0.005）、`ShortFrozen` 1（已冻今 1）
  - 每份再比「可用 − 结存」：= −占用 − 冻结（这几份持仓盈亏都不为正，§13 #17 的排除项为 0）—— 冻结确实从可用里扣了，而且扣的是这个数
- **往返**：`Place` 再 `Cancel` ⇒ 账户快照与挂单前逐字段相同（state.md「报单 → 挂上 → 撤单 → 账户回到起点，差 0」是 CTP 上的观测）
- **可平量**：持仓 1 手时挂一笔平仓，第二笔平仓（挂单或 `Submit`）拒在可平量；撤掉第一笔后第二笔可以挂
- **`Fill`**：挂单成交的账 = 同价 `Submit` 的账；`Fill` 撞 #21 报错时挂单与冻结都还在
- **`Settle` 带挂单报错**、`ApplyTrade` 平掉冻住手数报错

##### 不做（F4 范围外）

| 不做 | 为什么 |
|---|---|
| 部分成交 | `match` 的裁决 |
| 自动撤当日有效单 | 没有观测；调用方显式撤 |
| 快期侧冻结（`conformance/fixture/frozen.go`）迁到门面 | 快期带委托的夹具全部带昨仓，而 `Rebuild` 不接昨仓 —— 要先让快期那侧的跨日重建走门面（`Settle`），是单独一批 |
| `State()` / `Restore()` | F5 |

#### 9. F5：`State` / `Restore` —— 状态整体导出、原样放回（2026-09-15，实现之前写）

    func (s *Simulator) State() (State, error)
    func Restore(cfg Config, st State) (*Simulator, error)

§4「状态即纯数据」的两条要求落地：整体导出、原样放回；**交易日、规则数据版本、合约集、口径对不上时直接报错**。

##### `State` 里有什么（全部是数据，没有函数、没有指针共享）

| 块 | 字段 | 从哪来 |
|---|---|---|
| 头 | `Day`、`RulesVersion`（`Rules.Version()`）、`Choices` | 模拟器 |
| 账户 | 上日结存、出入金、平仓盈亏、手续费、持仓盈亏、两个占用、三个冻结、`Algorithm` | 新增 `account.State` / `account.Restore` |
| 持仓 | 每条：合约、投机套保、`Day`、`PositionDateType`、多空两侧的明细（`Lot` 原样） | `position` 现有导出面 + 新增 `position.Restore` |
| 计价 | 每个合约：最新价 / 昨结算价及各自的 Has | 模拟器 |
| 挂单 | 每笔：编号、`order.Request`、`order.Frozen` | `Book.Get` |

- 小数一律序列化成**字符串**（§6.5：`float64` 往返会让费率变成 `0.07000000000000001`）—— `decimal.Decimal` 的 JSON 默认就是带引号的字符串，测试钉住。
  ⚠️ 那个默认是 shopspring 的**包级全局开关** `MarshalJSONWithoutQuotes`，调用方改了它存档就变成裸数字；读回仍然精确（decimal 从文本解析、不经 float），但对外格式变了 —— 登记，不去改别人的全局
- 失效态的模拟器 `State()` 报错：半截状态存下来再恢复，等于把失效洗掉了
- 限仓、取整方向、日历**不进** `State`：它们是规则输入，由 `Restore` 的 `cfg` 再给一次（与 `Rules` 同理）

##### `Restore` 查什么（加载路径走校验，不走「反正是自己写的」快路径，§6.5）

1. `cfg.Choices` 与 `st.Choices` **逐项相等**；`cfg.Rules.Version()` 与 `st.RulesVersion` 相等；`cfg.Day` 与 `st.Day` 相等 —— 任一不等报错
2. 每条持仓的合约在 `cfg.Rules` 里查得到，且 `PositionDateType` 与规则数据一致；每笔挂单的合约同样
3. **明细**逐片校验（新增 `position.Restore`，不用 `Side.Append` —— 它对手数 ≤ 0 的片**静默丢弃**、不查价格）：
   手数 > 0、开仓价与基线 > 0；昨仓片的开仓交易日早于持仓交易日、今仓片等于它；
   ⚠️ **昨仓片必须全部排在今仓片之前** —— 「先平昨 ≡ 先开先平」的前提（`position.CloseOrder` 的文档）在类型上不保证，恢复路径是它唯一可能被打破的入口
4. 账户：币种、盈亏算法（沿用 `account.New` 的实测取值检查）、冻结非负，恢复后 `Check()` 通过
5. 挂单：逐笔 `Book.Insert`（重复编号报错）；**挂单冻结合计必须等于账户冻结额**，持仓侧冻住的手数不超过持仓
   - 实现时补（评审 20260915）：**冻结金额逐笔重算** —— 开仓与指定今昨的平仓整份重算；裸 CLOSE 的手续费只核「某种今昨拆法算得出」，
     拆分只核合计与持仓。⚠️ 不在恢复时的持仓上逐项重算裸平：挂单之后持仓可能变了（挂裸平后再成交平今），那样会拒掉合法存档（实测）；
     也因此不需要记挂单顺序
6. **重算核对**：用恢复出来的持仓与计价重算占用与持仓盈亏（F1 的 `value()`），与存档里的账户数逐项相等 —— 不等就报错。
   ⚠️ 这条挡的是「存档被手改了一处」：只改账户里的占用，前面五条都查不出来

##### 验证

- **往返**：一段含开平、结算、挂单的操作序列跑到一半 `State` → JSON → `Restore`，两边接着跑剩下的序列，每一步账户与持仓逐字段相同
- **小数是字符串**：JSON 里费率 / 价格字段是带引号的串
- **拒绝**：口径改一项、规则版本不同、交易日不同、合约不在规则里、PositionDateType 不一致、明细手数为 0、昨仓片排在今仓片后、账户占用被手改、挂单冻结与账户不符、失效态导出 —— 各报错并点名
- 破坏：每条校验各一条

##### 不做

| 不做 | 为什么 |
|---|---|
| 存档格式的版本迁移 | v0.9.0 之前格式可以变；`State` 带一个格式版本号，对不上就报错，不迁移 |
| 跨规则版本恢复 | §4：隐式基线跟规则数据走，跨版本恢复算出来的每个数都可能错 |

#### 10. F6：快期夹具的跨日重建与挂单冻结走门面（2026-09-15，实现之前写；三个决策点使用者同日按实现方倾向拍板）

**为什么**：快期那侧还有两份记账逻辑没收进门面 ——
跨日持仓 `conformance/fixture.Carry` / `Reconstruct`（前一日成交 `Replay` → `position.Settle` → 当日 `ReplayFrom`），
挂单冻结 `FrozenOf`（持仓侧手数）/ `FrozenAccountOf`（账户侧金额，自己调 `fee` / `margin` / `order.Book`）。
它们与门面并存，就是 F1 开头说的「两份实现一起退化时对拍全绿」。

##### 三个决策点（使用者 2026-09-15 拍板：按实现方倾向）

怎么确认的（评审 20260915 要求写明）：实现方在对话里把三个决策点与各自的倾向摆给使用者，使用者回复「按你的倾向做，继续推进」。
⚠️ 确认只到了实现方这一侧（评审方没有向使用者复核，理由是分量比 #20 轻）；确认的是**倾向**，不是逐条复述后的签字；第 2 条延续使用者此前确认过的 §13 #20（快期大商所不滚昨仓那一格登记口子差），第 1、3 条不改 CTP 预设。

1. **UseHistory 上的裸 `CLOSE`**：`Choices` 加第八项 `UndatedCloseOnUseHistory {Unmeasured, AsYesterday}`。
   `KQChoices` 填 `AsYesterday`（kq_facts 32，两条独立证据；kq_facts 25 柜台接受），`CTPChoices` **留空**（simnow_pending#1 未裁决）。
   ⚠️ 与第一至七项不同：它的零值**合法** —— 零值时 UseHistory 上的裸 CLOSE 报错（现状），不是开户就报错；否则 CTP 预设开不了户
2. **快期上大商所跨结算不滚昨仓**（kq_facts 24），门面按 §13 #20 裁决跟 CTP 滚 ⇒ **迁移只覆盖上期所**；
   大商所那一格登记为口子差（#20 裁决时的承诺），不为快期再开一个「不滚」的口径
3. **柜台已接受的挂单走一条不校验的入口** `PlaceAccepted(day, id, req)`：只冻结记簿、不跑八项 —— 与 `ApplyTrade` 之于 `Submit` 同一个关系（§4「两条并存的路径」）。
   快期的活委托里有本库校验会拒的单（`SHFE.ag2702` 买开 @13009.9，零头 0.9 tick，快期照收，kq_facts 45；快期不查时段，kq_facts 48）。
   ⚠️ 它仍然守两条：冻住的手数不超过持仓（簿与持仓一致）、冻结额从可用里扣（资金不够就报错 —— 柜台接受了而本库账上钱不够，说明两边账不一致）

##### 分两步

- **F6a（门面能力）**：决策点 1、3 落进门面；裸 CLOSE 在 UseHistory 上 = 平昨 ⇒ 消耗昨仓、冻结昨仓、手续费走平昨档（快期三档同费率，kq_facts 18/34）；单测
- **F6b（迁移）**：`conformance/fixture` 新增在门面上重放「前一日成交 → 结算 → 当日成交 → 活委托」的辅助，
  `reconstruct_test` / `crossday_test` / `frozen_account_test` / `order_frozen_test` 逐条改从它取，**每替换一条，替换前后逐字段判定相同**（F1 替换 `Rebuild` 同一条判据），旧函数最后删
  - ⚠️ 失去的一道检查：`Replay` 的「三种消耗顺序各跑一遍查歧义」在门面上没有对应物（门面按先平昨 / 平昨写死）。快期上消耗顺序「结构性测不出」（kq_facts 40），那道检查在快期夹具上一直在答「分不开」—— 删掉它时在 silent-risks 登记

##### F6a 实现时定下的边界（2026-09-15）

- ~~**八项校验不跟第八项口径**~~ **评审 20260915 打回，改为跟**：上一版 `Submit` / `Place` 照旧拒 UseHistory 上的裸 CLOSE，
  于是快期口径下同一笔单 `ApplyTrade` 收、`Submit` 拒，而拒因原话是「本库拒绝按平昨处理：那个语义只在快期模拟上实测过」—— 调用方选的恰恰是快期口径，**报错对判据的描述与行为不一致**。
  现在 `validate` 进门也过 `datedOffset`：按平昨校验（超过昨仓拒在可平量，原话是平昨的原话，与快期拒因「平昨手数超过昨仓持仓量」同义）；
  改写得来的拒单**不给 CTP 拒因码，不论拒在哪一项**（这种单整笔都不在 CTP 语料里，simnow_pending#1；原写「码的语料是 CTP 上显式平昨的拒单」只覆盖可平量一种，评审 20260915 指出写窄了）；`Submit` 返回的成交保留委托上的 `CLOSE`（快期成交里上期所裸 CLOSE 记作 CLOSE，语料 20 笔）。
  零值口径（CTP）照旧拒、原话照旧。守卫 `TestUndatedCloseAsYesterdayInValidation`
  - 我原先的顾虑（按平昨校验会配上 CTP 码 30）由「改写的不给码」解决 —— 评审提的，我没想到拆开
- 改写只在入口做一次（`datedOffset`：`ApplyTrade` 进平仓分支、`FreezeOf` 进门）；`commission` 收到的已是改写后的，不再各判一次（写了第三处，破坏验证会是如预期仍然绿）
- 只改 `types.Close`：强平标志也不指定今昨，但 kq_facts 32 只观测过 CLOSE ⇒ 破坏 574 如预期仍然绿，盲区如实登记
- `Restore` 核挂单时**手数也核**：记作平昨的裸 CLOSE 在簿上仍是 `Close`，簿取的是存档自己的今 / 昨拆分；金额核对分不开（平昨档与拆分无关）
- `PlaceAccepted` 不做重复编号预检：`book.Insert` 报同一个错，冻结在回滚里解掉 —— 回滚因此有测试走到（破坏 581）
- 测试用的规则数据加了 `SHFE.rb2701`（UseHistory、开 1 / 平昨 2 / 平今 5 按手，两两不同）：`ag2702` 三档同费率，分不开收哪一档

##### F6b 修订：调用面比设计时看到的大，按两半分（2026-09-15，F6a 之后、F6b 实现之前写）

**设计时漏看的调用面**（上面「已核」只数了四个测试）：
`Carry` 还在 `carry_test`（守卫单测 5 条）、`carrywire_test`（`carryStartFor`，被 `fixture_test` 的全量持仓对拍用）、`crossday_test`；
`Reconstruct` 还在 **`cmd/oracle` 的 `live.go`**（`-carry` 实时对拍）；`FrozenOf` 在 `fixture_test` / `frozen_test` / `reconstruct_test`。
而 `cmd/oracle` 的规格（`BuildSpecs`：字典乘数 + 实测保证金率 + 实测 PositionDateType）**没有手续费率** —— 门面记账必须有它（`ApplyTrade` 每笔都算手续费），填零就是编数。

**探针**（F6b 第一个提交入库；冻结那一半随旧函数删，跨日那一半转正为等价守卫 TestReconstructMatchesFacade —— F7b 删 `Reconstruct` 时连文件一起删了）：

| 半边 | 比什么 | 结果 | 判别力（改一处再跑） |
|---|---|---|---|
| 冻结 | 24 份带委托的夹具：`FrozenAccountOf` 合计 ≡ 门面 `FreezeOf` 逐笔之和；每个合约 `FrozenOf` 多空今昨 ≡ 门面逐笔之和 | 0 差异 | 冻结保证金换报单价 → 2 处金额不同；第八项置零 → 5 笔裸 CLOSE 报错 |
| 跨日 | rb2701：`reconstruct_test` 的 49 组输入 + `carrywire_test` 挑法的 49 组，`Reconstruct` 与门面「前日成交 → `Settle` → 当日成交」逐片明细（开仓价、基线、昨仓标记） | 98 组相同 | 结算价 +1 → 98 组全不同；第八项置零 → 40 组不同 |

⚠️ 语料的覆盖是窄的：带委托的夹具 25 份，凑得齐规格的 24 份（`frozen_account_test` 同一筛法；差的一份 `exp-reject-tick-vs-limit-20260909-3` 缺 `DCE.i2701` 的昨结算价），
其中活委托 9 笔（rb 平仓 5、平今 2，ag / i 开仓各 1；全语料 10 笔，差的就是那一份里的 1 笔）；跨日只有 rb2701 一个合约（交易所结算价只有上期所 20260908 一天）。

**据此分两半**：

- **F6b-1 冻结：删 `FrozenOf` / `FrozenAccountOf` / `NakedClosePolicy`**。调用方改用门面上的辅助（每份夹具一个模拟器：委托涉及的合约给规格与昨结算价，`PlaceAccepted` 不需要持仓的那部分直接 `FreezeOf`）。
  `NakedCloseRefuse` 那几格的意图（不声明口径就拒）由第八项的零值承担，`frozen_test` 相应改写。没有非测试调用方
- **F6b-2 跨日：四个对拍测试（`crossday` / `reconstruct` / `carrywire`→`fixture_test`）改从门面取持仓；`Carry` / `Reconstruct` 暂留**，只剩 `cmd/oracle -carry` 与 `carry_test` 用。
  ⚠️ 留下的第二份实现用两道守着：对拍在门面上（门面退化 → 对柜台红）；探针的跨日那一半转正为**等价守卫**（`Reconstruct` 漂离门面 → 红）。两份一起同样退化才漏 —— 那正是对拍在门面上要抓的
  - 删它们要 `cmd/oracle` 有手续费率来源（候选：快期行情的每手手续费反解，`fee_test` 的 `feeRates` 就是这么标定的，只覆盖五个品种）⇒ 登记为 F7，不在 F6 里编
  - ⚠️ 失去的一道检查（原计划里已写）：对拍测试改走门面后，`ReplayFrom` 的三种消耗顺序歧义检查不再在对拍路径上跑；它仍在 `Reconstruct` 里（`-carry` 路径）。登记 silent-risks
- 同日重放 `Replay`（`fixture_test` / `margin_test` / `account_test` 的 `ReplayRealized` / `cmd/oracle`）**不在 F6 里** —— 它是 `PositionDateNotNeeded` 上的逐合约重放，与 `closeOffsetOf`（与门面 `datedOffset` 同义的第二处）一起记进 F7

##### F6b 落地（2026-09-15）

- **F6b-1**：`FrozenBook`（读委托 → 门面 `FreezeOf` → `order.Book`）替掉 `FrozenOf` / `FrozenAccountOf` / `NakedClosePolicy`；`specRules` 带实测 PositionDateType，`Rebuild` 与 `FrozenBook` 共用 `fixtureChoices`。
  指向旧函数的 8 条破坏改指到门面与 `FrozenBook`（132/133/137/139/140/143/151/152），新增 584–589。覆盖变化见 silent-risks「冻结对拍要规格与昨结算价」
- **F6b-2**：`ReconstructOnFacade` 接 `crossday` / `reconstruct` / `carryStartFor`→`fixture_test`；三条对拍迁移前后日志（去行号与耗时）逐行相同。
  `Carry` / `Reconstruct` 暂留，`TestReconstructMatchesFacade` 钉等价；破坏 97 改由它接住，新增 590–597
- F6 的收尾（删 `Carry` / `Reconstruct`、同日 `Replay` 与 `closeOffsetOf` 收进门面）登记为 F7，前提是 `cmd/oracle` 有手续费率来源

##### 已核

- 活委托 10 笔，全部带 `insert_date_time`；上期所 `CLOSE` 5 笔、`CLOSETODAY` 2 笔、开仓 3 笔（其中 2 笔大商所）
- `NakedCloseRefuse` 只在 `frozen_test.go` 四处用（意图「不声明口径就拒」，迁移后由门面口径的零值承担）
- 费率 / 保证金率：`ratesOf` / `ratesFor`；PositionDateType：`measured-rules-20260909.json`

#### 11. F7：快期夹具的同日重放、`-carry` 与实测规则数据收进一处（2026-09-15，实现之前写）

**为什么**：F6b 之后快期那侧还剩三样与门面并存的东西（F6b 修订里登记的），外加一处之前没登记的：

- 结转：`Carry` / `Reconstruct` / `ReplayFrom`（`cmd/oracle -carry` 与 `carry_test` 在用），靠 `TestReconstructMatchesFacade` 钉等价
- 同日重放：`Replay` / `ReplayRealized` / `closeOffsetOf`（与门面 `datedOffset` 同义的第二处）
- ⚠️ **新发现、之前没登记**：实测规则数据有两个家 —— `testdata/refdata/measured-rules-20260909.json`（`cmd/oracle` 读，保证金率 + PositionDateType）与 `conformance/fixture` 测试里的 `marginRates` / `positionDates` / `feeRates` 变量（对拍读）。
  两份**没有任何东西比过**；手续费率只在测试变量里有，这正是 `cmd/oracle -carry` 拿不到手续费率、F6b 删不掉 `Reconstruct` 的原因

##### 已核（20260915）

- `cmd/oracle` 的 `BuildSpecs` 只给**有实测保证金率**的品种出规格：json 里是 rb / m / i / cu / ag 五个；`feeRates` 恰好也是这五个 ⇒ 手续费率进 json 之后，`-carry` 的覆盖**不变小**
- json 与测试变量现值一致：保证金率五个品种逐个相同（rb .07 / m .07 / i .11 / cu .11 / ag .22），PositionDateType 两个合约相同（rb2701 UseHistory / m2701 NoUseHistory）
- 同日 `Replay` 的调用点：`fixture_test`（`TestReplayIsUnambiguous` / `TestReplayMatchesOracleVolumeAndPrice` / `TestPositionViewAgainstFixtureShowsTheGap` / `TestPositionViewAcrossAllFixtures` 的当日样本）、`margin_test`（`TestMarginAgainstFixturePositions`）、`account_test`（`ReplayRealized`）、`cmd/oracle` 的 `live.go`；`replay_test` 是它自己的单测
- `account_test`（`TestAccountAggregatesFromTrades`）与 `TestRebuildAccountFieldByField` **同一份夹具**（`status-20260908-7`）、比**同样两个字段**（commission / close_profit）：前者自己调 `fee.Compute` + `ReplayRealized`，后者走门面。
  前者独有的是三道守卫（平仓 ≥ 10 笔、柜台 close_profit 非零、手续费残差上界 1e-5）与一行**只打日志**的逐笔对冲口径 —— 本批全是今仓，两条口径必然相等，没有断言
- `cmd/oracle` 的 `compareOne` 还调 `fixture.MarginOf` 自己算保证金（与门面 `value` 同义）—— 记为候选，不进 F7

##### 分三步

- **F7a（数据一处）**：`feeRates` 的五个品种（按额 / 按手、标定合约、有无第二个月份可预测）进 json（新段 `commission_rate_by_product`，每行带出处 probes.md §10.2）；
  读 json 的函数从 `cmd/oracle/conformance` 挪到 `conformance/fixture`（主模块；`cmd/oracle` 本来就 import 它）；对拍测试的三个变量改从 json 读、删掉。
  判据：替换前后 `conformance/fixture` 全部测试的 -v 日志（去行号与耗时）逐行相同
- **F7b（结转一处）**：`cmd/oracle -carry` 改走 `ReconstructOnFacade`（规格有了手续费率）；删 `Carry` / `Reconstruct` 与等价守卫；`carry_test` 里 `Carry` 的守卫单测由 `carry_facade_test` 接（已有：拆两条基线、没成交、收盘空仓、结算价为零；缺的补齐：基线重合时 `Split` 说相同。`TestTonightSeedDiscriminatingPower` 直接用 `position`，不依赖 `Carry`，留着）
- **F7c（重放一处）**：同日样本改走门面（一个合约一个模拟器，`PositionDateNotNeeded` 即 `specRules` 的默认）；删 `Replay` / `ReplayFrom` / `ReplayRealized` / `closeOffsetOf` / `replay_test`
  - ⚠️ 失去的检查：`TestReplayIsUnambiguous`（三种消耗顺序在全部当日样本上一致）。当日样本全是今仓，三种顺序**结构上**消耗同一批（`ReplayFrom` 注释自己写着「start 为 nil 时结构上不可能触发」）⇒ 它一直在答「一致」，删掉登记 silent-risks

##### F7a 落地（2026-09-15）

- json 新段 `commission_rate_by_product`：五个品种，`classified` 显式写（rb / m 真；i / cu / ag 假 —— 只有一个月份，按额按手判不了，**不把测试变量里的「按额」当成实测结论照搬**）
- 读取挪到 `conformance/fixture/measured.go`，`cmd/oracle` 的 `MeasuredRules` / `LoadMeasuredRules` 改成别名；新增校验：重复条目、`classified` 必填、按额按手二选一、标定合约必填；`BuildSpecs` 缺手续费率不给规格
- 此前加载器与 `BuildSpecs` 一条单测都没有，补上（`measured_test` / `cmd/oracle/conformance/rules_test`）
- 替换前后 `conformance/fixture` 全包 -v 日志逐行相同（两行差异来自 `TestOrdersAcceptedOutsideSession` 遍历 map 的示例输出，重跑三次三个结果）
- 破坏 357/362/363/93/180 改指到 json（93 原为删行，json 删行会留尾逗号读不成，改成把 m2701 改成同一型）、171/173 改指到 measured.go；新增 602–610（602 预判错：没写 classified 是空指针 panic，不是缺省成 false）

##### F7b 落地（2026-09-15）

- `cmd/oracle -carry` 改走 `ReconstructOnFacade`（`Spec` 自 F7a 起带手续费率）；删 `Carry` / `Reconstruct`、等价守卫 `TestReconstructMatchesFacade`
- `Carry` 注释里「为什么非结转不可」「三个前提」并进 `ReconstructOnFacade` 的注释；`carry_test` 里与门面测试重复的三条删掉，基线重合那条改在门面结转的结果上跑（`TestSplitSaysSameWhenBaselinesCoincide`）
- ⚠️ 此前 `-carry` 那条路一条测试都没有 —— 换实现时没有任何东西会红 ⇒ 补 `TestCompareCarriesThroughFacade`（入库夹具当实时截面，给 / 不给 `-carry` 两种）
- `closeOffsetOf` 仍在 `cmd/oracle` 的同日重放上起作用（传实测 PositionDateType）⇒ 补 `TestReplayTranslatesUseHistoryBareClose` 直接钉它，F7c 一起删
- 破坏：348 / 349 / 351 / 97 / 595–597 改由门面上的测试接；350 / 352 删掉（锚的 `Carry` 守卫没了，门面同一道守卫是 591 / 590）；新增 611（`-carry` 接线）

##### F7c 前期探针（2026-09-15，实现之前写）：门面要计价输入，同日样本八成没有

⚠️ **原计划「同日样本改走门面」按原样做会丢掉大半覆盖**：门面每记一笔成交都要算手续费（快期口径按昨结算价），有仓时要最新价与昨结算价（占用、持仓盈亏）；
`Replay` 两样都不要。探针（一次性，不入库）数了有成交的「夹具 × 合约」：

    152 个，其中带昨仓 26（F6b 已走门面）
    当日样本 126：缺规格 0 / 缺昨结算价 98 / 缺最新价 0 / 齐全 28
    缺昨结算价的 98 个全在交易日 20260908（行情进夹具之前采的），九个合约各 7～12 个

⇒ 直接迁，`TestReplayMatchesOracleVolumeAndPrice` / `TestPositionViewAcrossAllFixtures` 的当日样本从 126 掉到 28。

**救回的路（探针第二步）**：98 个样本**全部**能在同一交易日、同一合约的别的夹具里找到昨结算价，且**值唯一**（0 个多值）。
交易所前一日结算价那条路这次**没评估成**（`shfe-kx20260907.json` 是交易所原始格式，探针没解析），不下结论。

##### F7c 决策点 3（新增，实现之前定）：同日样本缺昨结算价时，借同日兄弟夹具的值喂门面吗

它碰到本仓库反复写的那条：**不拿别处的数顶替**。候选：

- (a) **借，但只借给门面当计价前提，结构上不让它进任何比对**（倾向）：
  这批样本比的是**持仓手数与开仓价**（`volume_*` / `open_price_*` 等不依赖昨结算价的字段），借来的数只让门面跑得起来。
  「不拿别处的顶替」防的是**被比的那个数**两侧同源或来源不明；这里它不被比。
  结构上的守法：辅助函数只返回 `*position.Position`，不返回模拟器 / 账户；保证金对拍（`margin_test`）照旧只用夹具**自己**报的昨结算价，缺就跳过；
  取值要求同日同合约**唯一**，多值或没有就跳过并计数
- (b) 这 98 个样本留在 `Replay` 上，只迁齐全的 28 个 —— 两份实现并存且按「有没有行情」分岔，比现在更难读
- (c) F7c 不做，同日 `Replay` 与 `closeOffsetOf` 作为登记在册的第二份实现留着

⚠️ 倾向 (a) 的理由也有代价：一个「能让门面在缺输入时跑起来」的辅助，是将来被顺手拿去比保证金的那种东西 —— 所以它的返回类型必须窄。

✅ **使用者 2026-09-15 选 (a)**（经 AskUserQuestion：「借同日值，只当前提」）。它碰到的是本仓库的一条原则，所以没有按实现方倾向自定。
决策点 1（`account_test` 删掉、三道守卫挪进 `TestRebuildAccountFieldByField`）与 2（`MarginOf` 不进 F7）按实现方倾向做，评审可以提异议。

##### F7c 落地（2026-09-15）

- 删 `Replay` / `ReplayFrom` / `ReplayRealized` / `closeOffsetOf` / `copyLots` / `signature`、`replay_test`、`account_test`；新增 `ReplayOnFacade`（只返回持仓，昨结算价由调用方显式给）
- 对拍测试的取持仓统一走 `positionOf`：**带昨仓就结转**（`carryStartFor` + `ReconstructOnFacade`），否则当日重放、昨结算价按 `sameDayPre`（自己有用自己的，没有借同日同合约唯一值）
  - ⚠️ **实现中撞出的旧错**：旧 `Replay` 对带昨仓的合约也只重放当日成交。20260909 那批 rb2701 有一笔裸 CLOSE —— 柜台平的是昨仓，旧重放手里只有今仓就拿今仓去平，量上恰好对得上、不报错；
    对拍只比没有昨仓的那一侧，于是一直没露头。门面在 `PositionDateNotNeeded` 下拒绝裸 CLOSE，不将错就错 ⇒ 这类合约改走结转。探针第一步把带昨仓的合约整个跳过了，所以没数到它们
- `cmd/oracle` 同日重放走 `ReplayOnFacade`，昨结算价只用实时截面自己的，缺就报错
- 判据：迁移前后 `conformance/fixture` 全包与 `cmd/oracle/conformance` 的 -v 日志（去行号与耗时、排序）—— 所有比对的判定计数不变；`cmd/oracle` 逐行相同；
  差异只有被删测试的日志、新测试、借值计数（98）、`margin_test` 的「多手持仓」诊断计数 14 → 31（带昨仓的合约现在拿到的持仓含昨仓明细；保证金那一行「对得上 31 / 未触发 14 / 失败 0」不变）
- `account_test` 的三道守卫（平仓 ≥ 10、柜台 close_profit 非零、手续费残差 ≤ 1e-5）挪进 `TestRebuildAccountFieldByField`
- ⚠️ **全量扫描撞出的收获**：破坏 81（「保证金对拍去掉『跳过带昨仓的方向』」）从红变绿 —— 带昨仓的合约走结转后，那些方向的保证金**本来就对得上**。
  于是 `margin_test` 那道跳过删掉、接回比对：对得上 31 → 48（多出的 17 个正是原先跳过的方向），失败 0；破坏 81 随之退役（`retiredBreakNums`）
- 评审 20260915 两个 nit（合并后另一批）：`TestReplayMatchesOracleVolumeAndPrice` 那道同形的跳过也删掉 —— 带昨仓的 26 个方向照比，手数全对；
  平过仓那一侧的 open_price 按已登记的口子 simnow_pending#10 归类（声明抽成 `openPriceAfterCloseDeviation`，与 `reconstruct_test` 共用）：对得上 108 → 140、已知口子 20、失败 0；
  「取不到门面前提而跳过」非零即红（今天 0 个，破坏 622 如预期仍然绿、登记为守将来）；`cmd/oracle` 缺昨结算价整次报错、缺规格跳过的理由写进注释
- 破坏：337 / 338 / 339 / 366 / 97 / 101 删（锚的旧重放代码没了）；341 / 365 / 367 改指门面（平仓方向、平仓盈亏方向、快期手续费基准）；368 改由 pnl 单测接、从「如预期仍然绿」变成期望红

##### 决策点（实现方倾向，F7c 之前定）

1. **`account_test` 怎么办**。候选：
   - (a) **删掉，三道守卫挪进 `TestRebuildAccountFieldByField`**（倾向）：两条测试比的是同一件事，留着就是第二份实现；逐笔对冲口径那一行没有断言，挪不挪都不损失验证
   - (b) 门面的 `ApplyTrade` 返回这笔成交的已实现结果（逐日盯市 / 逐笔对冲两个口径），`account_test` 改从它取 —— 导出面变大，而快期上两个口径分不开（本批全今仓；昨仓样本上柜台 close_profit 只给逐日盯市一个数），新增的导出字段在快期对拍里没有判别力
2. **`fixture.MarginOf`**（`cmd/oracle` 与 `crossday` / `reconstruct` 在用）：不进 F7，登记。它是「持仓 → 保证金」的第二份实现，但调用方要的是**逐方向**的数（view 的 `margin_long` / `margin_short`），门面只给合计 —— 要它就得先定门面给不给逐合约逐方向的占用

#### 12. F8：持仓 → 保证金的翻译收进门面（2026-09-16，实现之前写）

**为什么**：F7 之后快期侧只剩这一处第二份实现。⚠️ 重复的**不是保证金算法**（只有 `margin` 包一份），
是「持仓 → `margin.Leg`」这层**翻译**：门面的 `value()` 与 `conformance/fixture.MarginOf` 各写了一遍。
翻译恰恰是今昨维度被压没的地方（`IsHistory`、逐笔而非按方向合计、`MaxMarginSide`、基准）——
两处各自决定这几项，一起退化时对拍照样全绿。

##### 已核（20260916）

- `margin.Compute` **已经**返回逐组逐方向的分解（`Result.Groups`：`LongCompany` / `ShortCompany` / 合并后），门面缺的只是出口
- `MarginOf` 的调用点 7 处：`margin_test`（4）、`fixture_test`（2）、`crossday_test`、`reconstruct_test`、`cmd/oracle/conformance/live.go`
- `margin_test` 里三条单测直接测它：空仓报错、缺输入（乘数 / 昨结算价不为正）报错、费率至少三档
- 快期口径用 `margin.ByInstrument`（`fixtureChoices`），组键就是合约；`MaxMarginSide` 在这个口子上全为假（kq_facts 5）

##### 形状（实现方倾向）

1. **门面开一个只读出口**：`(s *Simulator) MarginGroups() []margin.GroupResult` —— 最近一次计价算出的分组分解，原样给出。
   ⚠️ 逐方向的数**只有在组键是合约时**（`SideScope = ByInstrument`，或未启用大边）才与柜台的 `margin_long` / `margin_short` 对得上：
   `ByProduct` 下一组跨多个合约，「这个合约的多头占用」没有定义。⇒ 对拍处**断言组键等于合约**，不做隐式回退
2. **与 F7c 那条守法的冲突要正面处理**：`ReplayOnFacade` 刻意只返回持仓，为的是让**借来的昨结算价**进不了保证金比对（§11 决策点 3，使用者定）。
   而保证金要从同一个模拟器里取 ⇒ 给它加一个显式参数 `borrowedPre bool`：借来的时候**不返回**保证金（返回结构里 `HasMargin=false`），
   不是靠调用方自觉。返回值因此从 `*position.Position` 变成 `Replayed{Position, MarginLong, MarginShort, HasMargin}`
3. **迁移**：7 个调用点改从门面取；删 `fixture.MarginOf`；`margin_test` 的三条单测改成门面上的等价
   （空仓 / 缺计价输入在门面里本来就报错，费率多档那条与保证金率表有关、留在原处）

##### F8 落地（2026-09-16）

- 门面：`valuation` 留下 `margin.Compute` 的 `Groups`，`MarginGroups()` 只读给出（结算后给次日那一份、恢复时按存档重算、返回副本）；破坏 623–627，529 因同一行改动改指
- `conformance/fixture`：`ReplayOnFacade` / `ReconstructOnFacade` 返回 `Replayed{持仓, 逐方向占用, HasMargin}`；
  `ReplayOnFacade` 加参数 `borrowedPre` —— 借来的昨结算价**不返回**占用，F7c 那条守法从文档变成结构上的
- 删 `fixture.MarginOf` / `IsNoPosition` / `margin.go`；7 个调用点（`margin_test` / `fixture_test` ×2 / `crossday_test` / `reconstruct_test` / `cmd/oracle`）改从门面取
- 取的是**交易所口径**（与柜台比的那一档，原 `MarginOf` 的选择）；公司口径含券商加收，本批费率上两者恒等（待实测 #14），要它走 `MarginGroups`
- `MarginOf` 那两条守卫单测在门面侧接住：空仓 ⇒ `HasMargin=false`（不是 0）、乘数不为正 ⇒ 报错（`TestReplayOnFacadeFlatGivesNoMargin`）
- 破坏 360 / 361 / 364 / 620 改指到门面与 `Replayed`
- 判据：迁移前后 `conformance/fixture` 与 `cmd/oracle/conformance` 的 -v 日志（去行号与耗时、排序）——
  `cmd/oracle` 逐行相同；夹具侧只差三处：**接上保证金的样本 31 → 40**（结转来的那些以前因为夹具自己没报昨结算价而拿不到占用，现在按交易所结算价给），
  对得上 +27 / 未实现 −27（正是那 9 个样本的字段），**失败数不变**；以及删掉 / 新增的那几条测试

##### ⚠️ 风险与盲区（实现之前先写下）

- 大边（`MaxMarginSide`）在快期上未启用，`ByProduct` 下的逐方向占用**测不到**；本条只在 `ByInstrument` 上有覆盖
- `MarginGroups` 是新的导出面（门禁④要记进 roadmap）：它把「最近一次计价」的中间结果露出去，调用方可能据此以为门面缓存了逐合约占用 —— 文档要写明它随每次 `Mark` / `ApplyTrade` / `Settle` 重算
- `cmd/oracle` 的实时对拍用的是 `spec.Margin`（实测保证金率），与门面口径一致；迁移后那条路的 `pre` 仍取实时截面自己的

#### 13. F9：`Advance` —— 一根 K 线推进盘中（2026-09-17，实现之前写）

§4 的「双层时钟」里 `Advance(bar)` 是盘中那一层的动词，§6.5 定过它的入参；`doc_debt` 一直挂着 `Bar` / `Advance`（v0.2.0）。
那两节写在门面**之前**。门面后来按 F1–F8 长出了 `Mark` / `Submit` / `Place` / `Cancel` / `Fill` / `Settle` / `State` / `Restore` ——
⇒ 本节回答的是：**在今天的门面上，`Advance` 还新增什么**。

##### 已核（20260917，读代码，不读文档）

| 事实 | 出处 | 对设计的影响 |
|---|---|---|
| `Mark(day, Quote{Last, PreSettlement})` 按新价重算占用与持仓盈亏；**同一交易日昨结算价不许变** | `trade.go` | 计价不必重写：`Advance` 用 `Close` 调它 |
| `Place` 收 `at time.Time`，时段校验在 `Place` 时做；`Fill(day, id)` 成交价 = 挂单价、全量 | `place.go`、design §8 | 成交不必重写：`Advance` 逐笔调 `Fill` |
| **委托簿是 `map`，`Live()` 按 id 字典序返回** —— 不记挂单先后 | `order/freeze.go` | 一根 K 线上多笔挂单同时可成交时，**没有真实顺序可用**（F4 已登记过「簿不记挂单顺序」）|
| `Quote` 只有最新价与昨结算价，没有高低价 | `simulator.go` | 触发判定要的 `High` / `Low` 只能来自 `Bar` |
| ⚠️ `doc_debt` 表里 `Restore` 仍写「v0.9.0 未落地」，而它 F5 就在 `state.go` 里了；这张表**没有守卫** | `state.md`、`state.go:96` | 过期陈述，本批改掉；守卫另记 |

⇒ **`Advance` 不引入新的记账路径**：守卫 → 按规则挑出可成交的挂单 → 逐笔 `Fill` → `Mark(Close)`。
这句话本身就是验证的主轴：**`Advance(bar)` 的账 ≡ 同一串 `Fill` 加一次 `Mark` 的账**。

##### 形状（实现方倾向，待评审）

    type Bar struct {
        Instrument types.InstrumentID
        TradingDay types.TradingDay
        Start, End time.Time              // §6.5 的 Ts / TsEnd
        High, Low, Close decimal.Decimal
    }

    type Filled struct{ ID string; Trade match.Trade }

    type Advanced struct {
        Filled    []Filled
        Unchecked []order.Unchecked   // 这根 K 线上没能查的守卫项（例如算不出涨跌停），与 Place 的「没查成」同形
    }

    func (s *Simulator) Advance(bar Bar) (Advanced, error)

§6.5 的「本库只要五样」不变：`Volume` / `Turnover` / `OpenInterest` 仍不要；`Open` 仍不要（见决策点 2）。

##### 一根 K 线之内的顺序（约定）

    ① 守卫    bar.TradingDay == 当前交易日            不同 ⇒ 报错「先 Settle」（§4 那道守卫在门面上的落点）
              Start < End；Low ≤ Close ≤ High；Low > 0
              有 Calendar ⇒ K 线按 [Start, End) 读；Start 落在某个时段 s 里、End ≤ s.End（跨午夜的 s 按 CrossesMidnight 折算）、
                            所属交易日 = bar.TradingDay
              High / Low 超出本合约当日涨跌停 ⇒ 报错（数据错，不裁；涨跌停与 Place 同一来源：refdata × MeasuredTickRounding）
                            ⚠️ 算不出涨跌停（没 Mark 过昨结算价 / Config 没给 TickRounding / 规格没比例）⇒ **跳过这一项**，
                            记进返回的 Unchecked —— 不报错（报错会让没配 TickRounding 的调用方完全用不了 Advance）
    ② 挑单    簿上**本合约**的挂单，按挂单先后逐笔判「这根 K 线上成不成」（决策点 1、3）
    ③ 成交    逐笔 s.Fill(id)
    ④ 计价    s.Mark(Quote{Last: Close})              —— 昨结算价不动（不带 HasPreSettlement）
    ⑤ 返回    本根成交了哪些

- ⚠️⚠️ **K 线的 End 是右开的，守卫不许按单点查 End**（评审 20260917 条件 1）：`refdata.Session.Contains` 左闭右开（「23:00:00 属于收盘之后」）。
  一根 10:14–10:15 的 K 线 End = 10:15 正好是时段结束点，`TradingDayAt(End)` 会答「时段之外」⇒ **每个时段的最后一根（10:15、11:30、15:00、23:00、夜盘收尾）都会被误拒**。
  ⇒ 语义：K 线按 [Start, End)；「同一个连续时段」= Start ∈ s 且 End ≤ s.End。⚠️ 这要**新增一个 Calendar 查询**：`TradingDayAt` 只答单点、不返回时段边界 ——
  F9b 点名一个「按时刻返回所在时段及其边界（与所属交易日）」的查询，不用两次单点查询拼
- ⚠️ **原子性**：①–④ 任何一步失败，**整根 K 线不推进**（状态回到调用前）。门面已有 `State` / `Restore`，回滚用它们，不另写一套 ——
  但**怎么用**要写清（评审 20260917 条件 3）：
  - `Restore` 是构造函数（返回新的 `*Simulator`），而 `Simulator` **不存 `Config`**（rules / calendar / tickRounding / positionLimits 散在字段里）
    ⇒ 原地回滚 = 推进前 `State()` 快照 → 失败时从 `s` 的字段拼回 `Config`、`Restore` 出新实例 → `*s = *新实例`。结构上可行（没有 sync 字段），F9c 实现时用测试钉住「回滚后逐字段相同」
  - **代价**：每次快照是一次完整深拷贝，回测几百万根 K 线不可忽视 ⇒ **只在这根 K 线上确实有候选挂单时才快照**。
    没有候选时只剩 ④ `Mark`，而 `Mark` 自己算完才 commit，本身就是原子的
  - ⚠️ **确定性的失败会卡住**：某一笔 `Fill` 因资金 / 可平量失败，每次重推同一根 K 线都会在同一笔上失败，`Advance` 永远推不过去。
    ⇒ 报错**点名委托号与拒因**；调用方的出路是撤掉那一笔再重推。文档写明这条出路
- ⚠️ **本合约之外的挂单不动**：一根 K 线只说一个合约的价格。同一时刻多个合约的先后由引擎逐根调用决定
- ⚠️ **先成交后计价**：成交价是挂单价、与 `Close` 无关；计价在后 ⇒ 返回时的持仓盈亏是「这根 K 线结束那一刻」的

##### 三个决策点（⚠️ 都是**建模选择**：K 线看不到逐笔路径与排队位置，任何对拍都证伪不了它们）

**决策点 1：挂单什么时候算「被触到」**

    触价即成   买单 Low ≤ P、卖单 High ≥ P        乐观：等于假设排在队首
    穿价才成   买单 Low <  P、卖单 High >  P      保守：价格恰好摸到挂单价的那根不成

- 倾向：**新增第九项口径 `Choices.RestingFill`，零值报错，两个预设都不给值**。
  理由：前八项口径都有实测或裁决撑着，而这一项**结构上测不了**（夹具不留逐笔行情），不该让预设替调用方选 ——
  与 `TestPresetsLeaveExactlyTheUnmeasuredCellsEmpty`「预设只填实测过的格子」同一原则（评审 20260917 同意）
- ⚠️ 零值在哪里报错要写死（评审 20260917）：
  - `New` **接受**零值（同第八项 `UndatedCloseOnUseHistory`），否则用 CTP 预设开不了户
  - F9b（只守卫 + 计价、不撮合）**不要求**它
  - F9c 的撮合入口**不论簿上有没有挂单**都在零值时报错 —— 比「第一次挂单之后才报」更可预期
- ⚠️ 涨跌停封板的 K 线（`High == Low == 涨停`，买单挂在涨停价）在「触价即成」下会成交 —— 实盘封板时排队多半成不了。
  不单独建模，写进 `RestingFill` 的导出面文档，作为「触价即成」**已知的乐观一侧**

**决策点 2：成交价**

    挂单价（现状）   与 match / Fill 的裁决同一句
    跳空时取开盘价   K 线一开盘就越过挂单价时，实盘成交在开盘价（集合竞价或对手价）

- 连续竞价里**被动成交的价就是挂单价**（价格优先、时间优先，成交价取先挂的那一方）—— ⚠️ 这是**文档级**说法，本库没有实测。
  它可以实测（见「前置探针」），测通之后「挂单价」对连续竞价里的挂单就不再是裁决而是规则
- 跳空那一侧按挂单价，对挂单方是**保守**的（实盘开盘价更有利）；要做对就得收 `Open`，而 §6.5 刻意没收它
- 倾向：**本版一律挂单价，不收 `Open`**；跳空保守写进导出面文档（与 match 包「100% 全量成交 / 保守 / 乐观」三处由测试钉住同一手法）

**决策点 3：同一根 K 线上几笔挂单都可成交时，按什么顺序**

    按挂单先后    簿要记序号（Place 时自增），State / Restore 带上
    按 id 字典序  现状 —— 不对应任何真实顺序

- 顺序**在账上看得见**：两笔平仓挂单先后成交会消耗不同的今 / 昨仓，而 §13 #21 的 (a) 让档位依赖「平仓前今仓量」（⚠️ 20260918 (a) 被 §13 #23 推翻为 (f)：档位依赖「当日开仓量 − 已按平今档收过的手数」—— 同样依赖先后，这条论证不变）；资金与可平量也在逐笔之间变化
- 倾向：**按挂单先后**。⚠️ 附带一个存档问题 —— **走现成的格式版本号，不按「挂单缺序号」推断**（评审 20260917 条件 2）：
  - `State` 已有 `Format`，`Restore` 先查它；§9「不做」表写的是「v0.9.0 之前格式可以变，版本号对不上就报错，不迁移」
  - ⇒ F9a 加序号时**抬 `StateFormat`**，旧存档统一在版本号那一步报错，**不给迁移口**
  - ⚠️ 不另写「读到不带序号的挂单就报错」：它会让**没有挂单的旧存档照样恢复成功**，而那份存档的格式其实已经不对了；两条判据并存还会分岔
  - 也不按 id 补一个顺序：补出来的顺序与真实挂单顺序在存档里长得一模一样

##### 前置探针（实现之前跑，结果定决策点 2 的证据等级）

**P-resting-price**：SimNow 日盘，在一个流动性好的合约上挂一笔**离最新价几跳**的限价买单，等行情走到那个价位成交，读成交记录的 `Price`。

    预言（连续竞价规则）   成交价 = 挂单价
    否则                   按实际成交价记为观测，决策点 2 重开

⚠️ 这一笔会产生手续费：**不在有待读结算读数的交易日跑**（例如 20260917 的 §13 #5）。⚠️ 可能等很久不成交 —— 工具要带超时撤单，撤单本身会不会收费先单独核一次。

##### 分期

    F9a  委托簿记挂单序号；State / Restore 带上；抬 StateFormat（旧存档在版本号那一步报错，不迁移）
    F9b  Advance 只做 ① 守卫 + ④ 计价（不撮合）+ 新增「按时刻返回所在时段及边界」的 Calendar 查询
    F9c  ② ③ 撮合 + Choices.RestingFill（第九项口径）+ 导出面文档写明乐观 / 保守两侧

每一期单独送审；F9c 等 P-resting-price 的结果再动。
⚠️ **决策点 1（新增第九项口径、预设不给值）与决策点 3（存档格式变更）都改导出面** ⇒ 照 F6 的先例，**动 F9a / F9c 之前摆给使用者确认**；
评审同意不等于使用者裁决（评审 20260917）。F9b 不改导出的口径与存档，不在此列。
前置探针 P-resting-price 在 SimNow 模拟账户上下单，跑不跑、哪天跑由使用者定。

##### 验证

- **等价**（主轴）：同一个起点，`Advance(bar)` 之后的 `State()` ≡ 按同样顺序手动 `Fill` 这几笔、再 `Mark(Close)` 之后的 `State()`，逐字段
- **触发边界**：`Low == P` 两个口径各一格（触价成、穿价不成）；`Low > P` 两个口径都不成；卖单对称；本合约之外的挂单不动
- **顺序**：同一根 K 线上两笔平仓挂单（今 1 昨 1，一笔裸平、一笔平今），按挂单先后成交，消耗与手续费档位与「先后对调」不同 —— 这一格就是决策点 3 在账上可见的证据
- **守卫**：交易日不同 ⇒ 报错且状态不动；时段外、跨两个时段 ⇒ 报错；High / Low 越涨跌停 ⇒ 报错；`RestingFill` 零值 ⇒ F9c 报错、F9b 不报
- **右开**：每个时段**最后一根**（End = 10:15 / 11:30 / 15:00 / 23:00、跨午夜夜盘的收尾）**不许被拒** —— 这一格专门防单点查 End；End 超出时段结束 1 秒 ⇒ 报错
- **算不出涨跌停**：没 Mark 昨结算价 / 没给 TickRounding ⇒ 不报错、`Unchecked` 里点名这一项
- **原子性**：第二笔 `Fill` 失败（例如资金 / 可平量）⇒ 第一笔也不算、价格也不动；报错里点名那一笔的委托号；撤掉它之后重推成功
- **快照只在有候选时**：没有候选挂单的 K 线不做 `State()`（用计数或钩子钉住，不靠读代码）
- **序号**：`State` → `Restore` 往返保留挂单先后；旧 `StateFormat` 的存档（**包括没有挂单的**）`Restore` 在版本号那一步报错
- ⚠️ **盲区写明**：决策点 1、3 在任何对拍上都不会红 —— 夹具没有逐笔行情。它们的对错只由「选择被显式做出、两侧偏离写在导出面上」来保证，不由观测保证

##### 不做（F9 范围外）

| 不做 | 为什么 |
|---|---|
| 部分成交、盘口、FAK / FOK | match 的裁决；K 线没有深度 |
| 开盘价 / 集合竞价价 | 决策点 2：本版挂单价、跳空保守；要做需要 `Open`，§6.5 刻意不收 |
| 条件单、止损单 | §6.5 提到 `High` / `Low` 用于触发；本版只做限价挂单，条件单是另一层（先触发、再变成限价单进簿） |
| `Bar` 上带涨跌停覆盖值 | §6.5 允许；本版不收 —— 同一合约同一交易日会有两个涨跌停来源（覆盖值与 refdata），而 `Place` 用的是后者 |
| 自动撤当日有效单、强平执行 | F4 与决策 11 同样的理由：没有观测 / 只建模判据 |
| 同一时刻多个合约的先后 | 引擎逐根调用决定；本库不排跨合约的队 |

##### ⚠️ 风险与盲区（实现之前先写下）

- **一根 K 线横跨休息**（例如 10:10–10:35）：端点都在时段内、交易日相同，中间却有 15 分钟不交易。守卫只看两个端点会漏 ⇒ 要求「同一个连续时段」，而不是「两个端点各自在某个时段里」
- ⚠️ **反方向的错更常见**：单点查 End 会把**每个时段的最后一根**误拒（时段右开）—— 评审 20260917 抓到的，设计初稿里没有
- **夜盘跨午夜的 K 线**：`TradingDay` 由 `Calendar` 判，不由 `Start` 的自然日判（与 `TradingDayAt` 同一条规矩）
- **`Close` 当最新价**：盘中持仓盈亏按 `Close` 计，与 `Choices.Mark` 口径的关系要在 F9b 核一遍 —— 不同口径下「最新价」这个输入是不是同一个东西
- **决策点 3 引入的序号是新的持久化字段**：抬 `StateFormat`，F5 的往返测试要跟着扩；旧存档报错是有意的（§9「不迁移」），要写进 roadmap（导出面 / 存档兼容性变更）

#### 14. F10：结算时手续费逐笔截断到分（2026-09-18，实现之前写）

##### ⚠️⚠️ 20260921 改写：前提换成「结算时按笔重算」（实现之前写；下面 20260918 的原文保留，凡与本小节冲突的以本小节为准）

✅ **20260921 夜已实现**（`fee.Parts`、`trade.go` `commission` 第三个返回值、`account.AddCommission(day, fee, atSettle)`，StateFormat 3）。

**为什么改**：原文的前提是「结算时对盘中逐笔费截断到分」(i-t)。它在两次结算单上都被否了（0918 两手单第一笔、0921 十二笔三手单全错）。
结算单（`testdata/refdata/ctp-settlement-*.json`，`oracle ctp-settle-trades` 读出）直接给出结算时的逐笔手续费；20260921 事前登记的一轮（cn-futures-rules §13 #5）十二笔全对：
**结算费 = 按额部分在第三位小数 d ≥ k 时进一分、否则舍去，k ∈ {1 … 5}**；k = 6 与银行家舍入被否。

**前置条件已满足、长期规则现在适用**（原文把「CTP 单样本」写成前置条件）：
- CTP 侧已不是单样本：0918（四笔两手单）与 0921（十二笔三手单）两张结算单逐笔印证，0911 / 0914 / 0915 / 0917 四张结算单与同一口径相容；0914 那条 +0.1185、0918 那条 +0.032 的残差都被结算单解释。
- 快期侧实测相反（结算不重新取整：20260908 → 09、0909 → 10 两对截面上日结存原样带零头，见上面「已核」表第一行；随 F10 实现登记进 `kq_facts`）⇒ 同一件事两个口子实测相反、CTP 不再是单样本 ⇒ **按长期规则跟 CTP**，快期一侧只做对拍声明。

**k 取 5（四舍五入到分）—— 使用者 20260921 夜定，双方各自确认**（实现方会话里使用者选「先按四舍五入实现」；评审方随后在评审会话里用 AskUserQuestion 向使用者本人核过，答「是，先按四舍五入实现」）：实验只钉住 k ∈ {1 … 5}；「没观测就报错」在这里不可用 —— d 由成交价决定，几乎每个交易日都会碰到 d ∈ {1 … 4}，结算时报错等于结算不能用（评审 20260921 指出）。
两条路摆给了使用者：再测一次把 k 收到一个值，或先按四舍五入实现、k ∈ {1 … 4} 登记为盲区；**使用者选后者**。⇒ k ∈ {1 … 4} 进 silent-risks，排一个事前登记的实验（挑 d ∈ {1 … 4} 的成交）。

**新形状**（替换原文「形状」一节里的截断那一条）：
- 结算费（每笔成交）= **四舍五入到分(按额部分) + 手数 × 按手费率**。按额部分 = 手数 × 手续费基准价 × 乘数 × 按额费率（`fee.Compute` 里那一项，取整口径之前）
  - ⚠️ 本库盘中**不收**那个每手 0.005 的常数项（§13 #19 还开着）。上面这个写法与常数项无关：CTP 结算单 = 按额部分四舍五入（按额品种 j / ag / rb）或整分的按手费（按手品种 m / MA），两种品种都对得上
  - ⚠️ **两项都不为零的品种没有观测**（登记为盲区）：公式是按两项各自结算的推得。⚠️ 这个盲区目前是**理论上的**：`ctp-commission-rates-20260914 / 20260915` 两份声明快照（各约 120 个合约）里**没有一个合约的按额与按手费率同时非零**（评审 20260921 查、实现方复核）
- 盘中不变（`Choices.FeeRounding` 照旧管盘中）；`account.AddCommission(day, fee, atSettle)` 的签名、`settleCommission` 进存档、`Settle` 按结算口径算次日结存 —— 原文的这几条不变
- ⚠️ 结算费**不再保证 ≤ 盘中费**：四舍五入会进位。原文 `AddCommission` 的约束 `0 ≤ atSettle ≤ fee` 改成下面这个**写死的界**（评审 20260921 提、实现方复核并更正一处）：
  盘中费 = FeeRounding(按额 + 按手)，结算费 = 四舍五入(按额) + 按手；按手部分两边相同 —— 前提是按手部分本身是整分（两份声明快照里按手费率全是整分：0 / 0.1 / 0.2 / 0.75 / 1 / 1.25 / … / 20）。差只来自按额部分的取整：

      FeeRounding = 不取整        结算 − 盘中 ∈ (−0.005, +0.005]
      FeeRounding = 截断到分      结算 − 盘中 ∈ {0, +0.01}      ⚠️ 能**恰好**取到 0.01（例 按额 35.685：截断 35.68、四舍五入 35.69）
      FeeRounding = 四舍五入到分  结算 − 盘中 = 0

  ⇒ 统一的界 **|atSettle − fee| ≤ 0.01（每笔）且 atSettle ≥ 0**；单测另外钉住「四舍五入到分时恰为 0」「截断到分时能取到 0.01」。
  ⚠️ 评审给的是「< 0.01」—— 截断口径那一格会被它误拒，实现方复核时更正为「≤」
- 存档：`StateFormat` **2 → 3**（F11 已用了 2；原文「抬一格」的使用者确认照录）

**验收**（替换原文的「先红后绿」）：
- 0921 结算单十二笔：门面按上面的公式重算每笔结算费，逐笔等于结算单 `Fee`；再加 0918 四笔两手单（含 23.59 → 23.58 那一笔，(i-t) 在那里给 23.59）
- 次日结存：0918 → 0921 的上日结存用门面结算推出来，与柜台 19995806.40 一分不差（原文那条 +0.028 的对拍换成这一条，那一条在新口径下也要对）
- 盲区的守卫：d ∈ {1 … 4} 的一笔在单测里点名「本库按 k = 5 收」，将来测到 k ≠ 5 时这一格要红


cn-futures-rules §13 #5 在 CTP 上命中 (i-t)（⚠️ 20260917 那一次；20260918 的前置补测**未复现**，七个候选都没对上 —— 前置条件没满足，本节停在设计）。
🔎 **20260918 夜结算单读出来之后，本节的前提要改**：结算时柜台**按笔重算**手续费（事后候选 (s)：截断到分(按额部分 + 每笔 0.005)），而不是「对盘中逐笔费截断」—— 两者只在多手单上分岔。(s) 未登记、未验，见 §13 #5；验过之后本节的「截断盘中费」改成「按结算口径重算」，`AddCommission` 的第二个参数由门面按 (s) 算：**盘中** `Commission` 是逐笔费的原样累加（柜台读数带 `16.855000000000004` 这类尾巴），
**结算**时次日 `PreBalance` 按「每笔费截断到分再求和」算 —— 交易日 20260917 十笔、残差 +0.028，五个预言里只有它相等。本库 `Settle` 还没接。

##### 已核（20260918，读数据与代码）

| 事实 | 出处 | 对设计的影响 |
|---|---|---|
| **快期不截断**：20260908 收盘结存 998995.3227（手续费 394.6773 带零头），20260909 `pre_balance` 仍是 998995.3227；20260909 → 20260910 零头 .3738 原样带过去（当日手续费 0.9489；逐笔截断的话次日结存**至少**多 0.0089，0908 那对至少多 0.0073） | `testdata/probes/status-20260908-8.json`、`status-20260909.json`、`status-20260910.json` | 两个口子相反。⚠️⚠️ **但 CTP 那一侧只有 20260917 一个样本** —— 长期规则（§5、记忆 `ctp-over-kq`）明写排除「CTP 只有一个样本」⇒ **不能直接按长期规则跟 CTP**，先补测（见下「前置条件」）。初稿在这里写了「按长期规则跟 CTP」，是漏看了排除项（评审 20260918 拦下） |
| 账户只存手续费的**和**；逐笔的费在 `commit` 之后就没了 | `account/account.go` `AddCommission`、`trade.go` `commit` | 结算时算不回 Σ 截断(费_i) ⇒ **必须在记账那一刻就另累加一份** |
| 快期跨日对拍里**没有**一条拿本库结算出的次日结存与柜台比（跨日重建都从夹具的 `pre_balance` 起步） | `conformance/fixture/*_test.go` | 改了之后快期侧不会有测试自动红 ⇒ 口径差要**主动**写进对拍声明，不能等它红 |
| `AddCommission` 的生产调用点只有一处（`trade.go` 的 `commit`） | grep | 改签名的波及面小 |

##### 形状（实现方倾向，待评审）

- `account.AddCommission(day, fee, atSettle decimal.Decimal)`：**改签名**，同时给「盘中计入的」与「结算时计入的」两个数。
  约束：`0 ≤ atSettle ≤ fee`（截断只会变小，不会变负）。⚠️ 不另开一个 `AddSettleCommission`：两次调用分开时，漏调后者的调用方会让结算按「整笔都不算」算，**静默多出一整笔手续费的钱** —— 改签名让每个调用点在编译期被迫表态
- 账户新增存储项 `settleCommission`（Σ 截断(费_i)），进 `account.State`；`account.Settle`：`PreBalance(次日) = Balance() + commission − settleCommission`，然后两者一起清零
- 截断规则**不住在 account**：账户只管记两个数。门面在 `commit` 里对**每一笔成交**用现成的 `fee.TruncateToCent.Apply`（`internal/decimalx`，向零截断到 0.01；费非负，向零 = 向下）
  ⚠️ 初稿写的是「新增函数 `fee.TruncateToCent`」—— 写实现时才发现 `fee.TruncateToCent` 早已是一个**取整口径常量**（`fee.Rounding` 的取值），新增同名函数编译不过；改用它的 `Apply`，不新增导出名
- ⚠️ **与 `Choices.FeeRounding` 的关系**（初稿漏了，写实现时撞见）：`FeeRounding` 管的是**盘中**逐笔费怎么算（§13 #5 盘中那一半没收敛，零值报错、两个预设都不填），它不动。
  结算截断作用在**按 `FeeRounding` 算出的逐笔费**上：盘中选 `TruncateToCent` / `HalfUpToCent` 时逐笔费已是整分，结算截断是恒等；选 `NoRounding`（CTP 盘中读数的形状）时结算截断才起作用。
  ⇒ 两者组合不出矛盾，不需要第十项口径；「快期结算不截断」只在对拍声明里出现
- 盘中一切不变：`Balance` / `Available` / 风险度都用原样累加的 `commission` —— 与 CTP 盘中读数一致
- `futsim.StateFormat` **抬一格**（`account.State` 多了一项）。旧存档在版本号那一步报错，不迁移。⚠️ 不写死目标号：F9a 也要抬，谁先落谁用下一个号（评审 20260918）

##### 决策点（要使用者确认的只有一个）

1. **抬 `StateFormat`（抬一格）**：导出常量取值变化、旧存档读不进来。照 F9 决策点 3 的先例，**动手之前摆给使用者**。
   ✅ **使用者已确认（20260918，实现方会话里答「可以升」）**：StateFormat 1 → 2，旧存档报错不迁移。
   ⚠️ F9a 也要抬一次；两批谁先落谁抬，另一批再抬一次（v0.9.0 之前格式可以变，抬两次没有代价）—— 不为了省一次把两批绑在一起。
   使用者当时答的是「1 → 2」这个具体问题；「抬一格」是评审 20260918 为防编号撞车提的写法，意思相同（本批若先于 F9a 落，就是 1 → 2）
2. **跟 CTP 之前先补测**（使用者 20260918 定「先补测一次」）：这一条确认**先在评审方一侧**取得（评审用 AskUserQuestion 问到），随后实现方在自己的会话里向使用者复核，答「是，先补测」。
   补测命中 (i-t) 之后，它才落进长期规则的范围（同一件事两边实测相反、CTP 不再是单样本），那时再按长期规则跟 CTP、快期一侧只做对拍声明，不做成第十项口径。
   ⇒ **补测是 F10 的前置条件**：实现分支 `impl-f10` 停在按成交截断的那一版，补测的结果决定截断挂在哪一层

##### 前置条件：补测（事前登记；20260918 日盘造成交、当晚读交易日 20260921 的 `PreBalance`）

⚠️ **要同时回答两个问题**：结算时重新取整了吗（20260917 的五个候选再判一次）；「逐笔」是哪一种粒度（每笔成交 / 每张单 / 每手）。
粒度要分开需要**多手单**：使用者 20260918 同意 `PROBE_MAX_VOLUME` 临时调到 2（当天测完改回 1）。

候选（对当日全部逐笔柜台手续费增量）：

    (iii)   不重新取整                     残差 = 0
    (i-t)   每笔成交截断到分               残差 = Σ_成交 (费 − 截断(费))
    (i-o)   每张单截断到分                 残差 = Σ_单 (单内费合计 − 截断(单内费合计))
    (i-l)   每手截断到分                   残差 = Σ_成交 (费 − 手数 × 截断(费 ÷ 手数))
    (i-r)   每笔成交四舍五入到分           残差 = Σ_成交 (费 − 四舍五入(费))
    (i-T)   当日合计截断到分               (i-R) 当日合计四舍五入到分
    (ii)    以上都不是

⚠️ 两阶段登记，与 20260917 同一个做法：① 本节（候选与做法）早于任何成交；② 收盘后按柜台记下的逐笔增量把每个候选的残差**算出来并提交**，早于当晚读 `PreBalance`。
⚠️ 两个候选在当天的费上同值 ⇒ 第二次登记时说出来，不等看到结果再说。**(i-t) 与 (i-o) 只有一张单拆成多笔成交时才分得开** —— 模拟盘上两手单多半一笔成交完，那时两者今天分不开，照实记。
⚠️ (i-l) 的「每手费」定义为 费 ÷ 手数 —— 若 0.005 常数项按笔收而不按手收（§13 #19 未定），这个定义本身就是一个假设；两手单的柜台增量顺带给 #19 一个数据点。
⚠️ 判法沿用 `oracle settle-residual`（残差按 Round(6) 等值判，不往最近的凑）；过夜腿是 #23 的两手种子（`DCE.m2701`、`CZCE.MA701`），结算价取次日行情的 `PreSettlementPrice`。

##### 验收（先红后绿）

- **CTP 结算对拍**（新增，放 `conformance/ctpfixture`）：从 `ctp-status-20260917.json` 的上日结存、平仓盈亏起账，逐笔喂 `testdata/refdata/ctp-fee-deltas-20260917.json` 的十笔费，按结算价算持仓盈亏，`Settle` 之后次日 `PreBalance` 要**等于** `ctp-slices-20260918.json` 的 19997514.88。
  ✅ **实现之前已跑过一次（20260918，临时测试、未入库）**：本库现在的 `account.Settle` 给 19997514.852，柜台 19997514.88，差 **0.028** —— 红的形状与 §13 #5 的残差一致。
  ⚠️ 数据的一处缺口照实写：`ctp-fee-deltas-20260917.json` 只有 `DCE.j2701` 那六笔；之前的四笔（E1 / E2 各 0.2、郑商所种子各 2）只以合计 4.4（第一笔的 `commission_before`）出现。四笔各自整分 ⇒ 按合计喂与逐笔喂截断结果相同，但这是**从 state.md 的记录推得**，对拍里要断言「4.4 是整分」并写明这个推断
  ⚠️ 截断由 `fee.TruncateToCent` 算、再交给账户 —— 对拍经过库自己的截断函数，不在测试里手写截断（否则是拿测试自己的算术验测试）
- **逐笔 vs 合计**：同一组费，按「合计截断」会差 0.020 —— 单元测试里点名这一格，防止有人把截断挪到结算时对合计做
- **Restore 往返**：盘中有带零头的费时 `State` → `Restore` → `Settle`，与不经存档的结算逐字段相同（`settleCommission` 丢了会在这里红，而且只在这里红）
- **快期口径差声明**：对拍声明表里加一行「结算取整：快期不取整、本库跟 CTP 逐笔截断」，附上面两对截面

##### ⚠️ 风险与盲区（实现之前先写下）

- **逐笔 = 每笔成交、每张单、还是每手，分不开**：20260917 十笔全是一手一单一笔成交。本库按**成交**截（`commit` 的粒度）；多手成交在一笔里时三种读法会分岔 —— 登记为 §13 #5 的残余问题，不假装量过
- **只有一个柜台、一个交易日**（§13 #5 本身不进 `rules_measured`）：「跟 CTP」是规则裁决，不是证据升级
- **盘中那一半仍是盲区**：CTP 盘中「不取整」与「取到五位或更细」分不开（§13 #5 原文），本设计只动结算那一刻
- 冻结手续费（挂单）不进 `settleCommission`：冻结在成交时解冻、按成交重算，结算时有冻结本来就报错（F4）

#### 15. F11：裸平按「当日开仓额度」收档 —— §13 #23 的 (f) 接进门面（2026-09-18，实现之前写）

✅ **20260918 夜已实现**（`quota.go`、`trade.go` `chargeUndated` / `bookQuota`、`state.go` `Quotas`，StateFormat 2）。下文是实现之前写的设计，照原样保留。

§13 #23 在大商所与郑商所各一次事前登记的实验里都收敛到 (f)：**平今档手数 = min(平仓量, 当日开仓量 − 当日已按平今档收过的手数)**。
本库 `chargeUndated` 现按被推翻的 (a)（min(平仓量, 平仓前今仓量)）收，「额度用完之后再平今仓」收错档（silent-risks 100）；郑商所在两档不同时整笔报错。

##### 已核（20260918，读代码）

| 事实 | 出处 | 对设计的影响 |
|---|---|---|
| 档位只由 `commission(tr, todayBefore)` 的第二个参数决定：`min(平仓量, todayBefore)` 走平今，其余平昨 | `trade.go` `commission` / `chargeUndated` | (f) 与 (a) **公式同形**，只换输入：`todayBefore` → **剩余额度** `当日开仓量 − 已按平今档收过的手数` |
| `commission` 有三个调用方：`ApplyTrade`（成交）、`FreezeOf`（挂单冻结）、`checkOrderFreeze`（恢复时核挂单冻结，遍历 k = 0…手数） | `trade.go`、`submit.go`、`state.go` | 三处都换成额度；`checkOrderFreeze` 的遍历语义不变（k 仍是「平今档手数的上界」） |
| `Fill` = 解冻 → `ApplyTrade`：成交时按**当时**的状态重算，不按冻结额收 | `place.go` | 额度在成交那一刻消耗；挂单不预占额度 |
| 持仓明细只有**还在账上**的片；平掉的今仓、当日开过几手都不在 | `position` | 额度**推不出来**，必须另记（这也是修好之前没法先改成报错的原因，silent-risks 100） |

##### 形状（实现方倾向，待评审）

- 门面按 **(合约, 投保标志, 持仓方向)** 记两个整数：`openedToday`（当日开仓手数）、`chargedToday`（当日已按平今档收过的手数）。开仓 `openedToday += 手数`；任何一笔按平今档收费的平仓 `chargedToday += 平今档手数`
- 裸 CLOSE 的档位：`平今档手数 = min(平仓量, openedToday − chargedToday)`，其余平昨 —— 进 `commission` 的是这个额度，不再是平仓前今仓
- `Settle` 把全部计数清零（交易日翻过去；夜盘属下一交易日由调用方的 `Settle` 时点保证，郑商所 / 大商所的 `OpenVolume` 都实测清零）
- 实测交易所名单 `undatedCloseMeasuredOn` 从 {大商所} 扩到 {大商所, 郑商所}；其余 NoUseHistory 交易所（广期所等）两档不同时照旧整笔报错
- 存档：`futsim.State` 新增 `Quotas []QuotaState{Instrument, Hedge, Direction, OpenedToday, ChargedToday, ExplicitToday}`，**`StateFormat` 抬一格**（与 F10、F9a 谁先落谁用下一个号）；存档字段的指纹守卫现在只在 `impl-f10` 分支上（F10 停着）—— F11 若先落，就把那条守卫一起带过来，不让「加字段忘了抬号」再次无人看守
- `Restore` 核：计数非负、`explicitToday ≤ chargedToday ≤ openedToday`、该方向今仓手数 ≤ `openedToday`（今仓都来自今天的开仓）；不许有存档交易日之外的计数。（评审提的「chargedToday ≤ 当日平仓手数合计」要多记一个数，现有几条已挡住「计数丢了」，不加）

##### 决策点

1. **抬 `StateFormat`（抬一格）**：导出面 + 旧存档读不进来 ⇒ 摆给使用者（F9 / F10 的先例）
   ✅ **使用者已确认（20260918 夜，实现方会话里答「可以抬一格」）**
2. 三条**推得、未实测**的外推：**一律「报错不猜」**，与本库既有做法一致（#21 往郑商所推时使用者定「其余报错不猜」、郑商所 X0 的静默错修成整笔报错、`chargeUndated` 注释「没有可用的规则 —— 不猜」）。
   ⚠️ 初稿把它们写成「不是决策点、按推得的收」—— 那等于在修 silent-risks 100 的同时预先开三个同形的洞（评审 20260918 拦下）。选报错不需要使用者破例，所以**不是**使用者的决策点；每一条都另排事前登记的实验，测到了再放开。
   - **多手裸平、0 < 额度 < 平仓量** ⇒ 报错。按 min 拆 / 全今 / 全昨三种读法给不同的数；额度为 0 或 ≥ 平仓量时三者同值，照收。实验全是一手一笔
   - **显式平今扣不扣额度** ⇒ 另记 `explicitToday`（`chargedToday` 里有几手来自显式平今）。之后一笔裸平，按「扣」与「不扣」两种读法算出的平今档手数不同 ⇒ 报错；相同就收（例如额度本来就够、或本来就为 0）。
     ⚠️ 显式平今这一笔**本身**收平今档照旧（按标志收）：大商所会把显式平昨改写成平仓（§13 #21 E2），显式平今在两所上改不改写没量过 —— 这是既有行为，不在本批改动里，登记为同一个盲区
   - **额度分不分方向** ⇒ 两个方向本来都记。一笔裸平按「分方向」（本方向开仓 − 本方向已按平今档收）与「不分方向」（两方向开仓合计 − 两方向已按平今档收合计）两种读法算出的平今档手数不同 ⇒ 报错；典型形状是当日开过反方向的仓。只做单方向的调用方永远走不到这一支
   - 三条的判定都放在一个纯函数里（给定计数与平仓量，返回平今档手数或「读法分岔」的错误），单元测试逐格钉住「分岔 ⇒ 报错 / 同值 ⇒ 照收」，不靠读代码

##### 验收（先红后绿）

- **CTP 对拍（新增）**：把 `ctp-slices-20260921{,-2..-10}` 两个交易所的序列（种子昨 1 → 开今两手 → 裸平 ×3）从门面重放，逐笔手续费要等于柜台增量（0.1/0.1/0.2、6/6/2）。⚠️ 实现之前先跑一次看它红：大商所第 3 笔本库收 0.1（差 0.1），郑商所整笔报错。✅ 20260918 写设计时已用合成规则在门面上实跑同一序列（临时测试、未入库）：大商所三笔都收平今档（第 3 笔按 (f) 应收平昨），郑商所第 1 笔就整笔报错 —— 与这句一致
- **回溯全部已有裸平观测**：大商所 0915 / E1 / E2、郑商所 X1 / X2 / X0（`TestQuotaCandidatesRetrodictPastObservations` 那六笔）从门面走一遍，(f) 下全对 —— 郑商所 X1 平在开仓之前（额度 0 ⇒ 平昨）这一格专门防「按当日开仓量而不扣已平」的错读
- **存档往返**：额度用掉一半时 `State` → `Restore` → 再裸平，与不经存档的收费相同；计数丢了只会在这里红
- **结算清零**：跨一次 `Settle` 之后额度从 0 起算（否则昨天的开仓会让今天的裸平收平今档）
- **三条外推的报错**：多手裸平且 0 < 额度 < 平仓量 ⇒ 报错、额度 0 或足够 ⇒ 照收；显式平今之后的裸平两种读法分岔 ⇒ 报错、同值 ⇒ 照收；当日开过反方向且两种读法分岔 ⇒ 报错。每一格报错后状态不动
- **挂单**：冻结时按当时额度、成交时按成交时额度；两笔裸平挂单都按同一个额度冻、先成交的那笔用掉额度后第二笔按剩余收

##### ⚠️ 风险与盲区（实现之前先写下）

- 只有一个柜台，两个交易所各一次三笔（§13 #23 不进 `rules_measured`）
- 上面三条外推都选了**报错**：代价是调用方在那几种形状上拿不到数（多手裸平、显式平今之后的裸平、当日开过反方向的仓）。报错信息要点名是哪一条、哪个 §13 条目，调用方能拆成一手一手报或改用显式标志绕开；每条排一个事前登记的实验，测到了再放开
- 计数是「当日」的：调用方跨交易日不调 `Settle`、直接用新交易日 `ApplyTrade`，门面本来就报错（`usable`），计数不会跨日漂
- `ApplyTrade` 的灌成交路径：调用方从柜台成交记录重放时，开平标志是柜台改写后的（大商所显式平昨记成 `'1'`）—— 重放出来的计数与柜台一致，与「调用方当初发的是什么标志」无关

#### 16. F12：大商所涨跌停价往里收 —— §13 #24 接进规则层（2026-09-22，实现之前写）

§13 #24 有条件收敛（20260922 跨日事前登记，三个判别样本全对）：大商所的涨跌停价 = 昨结 × (1 ± 比例)，**涨停向下、跌停向上**对齐最小变动价位（往里收）。
本库 `MeasuredTickRounding` 的大商所一格是四舍五入 ⇒ 大约一半的交易日差一跳（silent-risks 101）。

##### ⚠️ 前提（照实写进实现）

收敛依赖「SimNow 的涨跌幅比例为整百分比（6%）」：放开比例时四舍五入（m ≈ 5.98%）与往外收也能解释 0921 + 0922 全部样本，只有往里收的可行区间含整 6%。
两头向下 / 两头向上无论比例都被否 —— 这一半不依赖前提。⇒ `TickInward` 与 `MeasuredTickRounding` 的注释都要写明这个前提；拿到独立的比例来源之前它是假设。

##### 已核（20260922，读代码）

| 事实 | 出处 | 对设计的影响 |
|---|---|---|
| `snapToTick` 上下两边用**同一个**方向（注释里还专门写了「不是上取上、下取下」—— 那是上期所的实测） | `refdata/refdata.go` | 往里收上下方向不同 ⇒ `PriceLimits` 要按取整方式分别给上下两边的方向，不能再只调一个 `snapToTick` |
| `PriceLimits` 的生产调用点只有 `order` 的涨跌停校验一处；取整方式由 `Config.TickRounding`（`MeasuredTickRounding()`）给 | `order/order.go:319`、`submit.go` | 改一处表、一处函数；取整方式在配置里、**不在存档里** ⇒ **不抬 `StateFormat`** |
| `TickRounding` 零值是「未指定」、使用即报错；现有取值 1 … 4 | `refdata/refdata.go` | 新取值**追加在末尾**（`TickInward` = 5），不改已有取值 |

##### 形状（实现方倾向，待评审）

- `refdata.TickInward`（新导出取值）：涨停向下、跌停向上。`String()` 给「往里收」
- `PriceLimits`：`TickInward` 时上边 `Floor`、下边 `Ceil`；其余取整方式照旧上下同向
- `MeasuredTickRounding()` 的大商所一格：`TickHalfUp` → `TickInward`（注释写前提与 §13 #24 出处）；上期所照旧 `TickFloor`；郑商所 / 广期所照旧没有（不外推）
- `TickHalfUp` 的注释从「实测大商所是这一种」改成历史：它曾是 kq_facts 22 在三种候选里的判定，已被 §13 #24 推翻
- ⚠️ **`snapToTick` 补 default**（评审 20260922 条件 1）：现在的 switch 只列了向下 / 向上 / 四舍五入、没有 default —— 不认识的取值会把「价 ÷ 跳」原样乘回去，**静默返回一个没对齐跳的价**。
  加了 `TickInward` 常量却漏了 `PriceLimits` 里的分支（或将来再加第 6 种），本库就会给出错的涨跌停价而不报错。
  ⇒ 不认识的取值让 `PriceLimits` 返回 `ok = false`，与「没指定取整」同形（由 `order` 记进 Unchecked）；配一格单测（越界取值 ⇒ `ok = false`）与一条破坏（删掉 `TickInward` 分支 ⇒ 正向断言红，而不是静默给理论价）

##### 要改的现行说法（F12 之后会过期；评审 20260922 条件 2 列出、实现方复核并补了一处）

- `refdata/refdata.go:188`（`TickHalfUp` 注释「实测大商所是这一种」）、`:217`（`PriceLimits` 注释「上期所向下取整、大商所四舍五入」）
- `order/order.go:326`（跳过涨跌停校验的原因文字「上期所向下、大商所四舍五入」）
- `docs/design.md:505`（「`MeasuredTickRounding` 只给实测过的两家 —— 上期所向下、大商所四舍五入」）
- `refdata/refdata_test.go:335 / 339 / 358`（注释与用例名「大商所 i 四舍五入（实测）」）
- `refdata/build_from_measured_test.go:193` 注释、`simulator_test.go:63` 注释（「m2701 的 6% 按大商所四舍五入对齐后是 3587 / 3181」）
- ⚠️ 后面几处的**数不变**：3384 × 6% 与 734.5 × 9% 在四舍五入和往里收下给同一个价 ⇒ 测试不会红 —— 这正是它们容易被漏改的原因，只改措辞
- 带日期的历史实测记录（`probes.md:2255` 等）保留

##### 验收（先红后绿）

- CTP 侧 `TestMeasuredTickRoundingAgainstCTPQuotes`：`knownDivergence` 里的 `m2701 / 20260921`、`m2705 / 20260922` **转成正向断言**（本库给出柜台的价）；`discriminating` 的「换成另一个方向就对不上」对大商所改成与 `TickHalfUp` 比
- 快期侧 `TestLimitRatioFromQuotes`：候选集加 `TickInward`；`kqKnownDivergence` 里 3415 那个样本转成正向（只命中往里收）；大商所的登记方向改成「往里收」
- `refdata` 单测：往里收在「上边小数 ≥ .5、下边小数 < .5」（四舍五入会往外走）那一格给出与四舍五入不同的价
- ⚠️ 实现之前先跑一次：把 `MeasuredTickRounding` 改成 `TickInward` 之前，上面第一条的正向断言会红（本库给四舍五入的价）

##### ⚠️ 风险与盲区

- 前提（整百分比）是假设；SimNow 调了比例时 CTP 侧对拍会红，那是比例变了而不一定是取整错了
- 只有一个柜台（CTP）的事前登记样本；快期 20260909 的 3415 是回溯样本（同向）
- 郑商所 / 广期所的取整方向没有观测：`MeasuredTickRounding` 里照旧没有它们，调用方拿不到就报错，不猜

### 为什么这样切

**纯函数层与状态层的分界是这套结构的主轴。** `fee` / `margin` / `pnl` 只做计算：
给定持仓、规则与价格，返回一个数。它们不知道账户存在，因此：

- 可以**独立于状态**被验证——对拍时可以只喂一组输入核对一个公式，
  而不必先把账户推到某个状态
- 不会产生循环依赖——它们不 import 任何状态层的包
- cn-futures-rules.md 里每一条**待实测**的规则，都落在这三个包中的某个函数上。
  实测结果一到，改的是一个函数，不是翻遍全仓

依赖是一个有向无环图，箭头一律从下往上：

```
types ─┬→ ctperr                       错误码，只依赖 types 的枚举
       └→ refdata ─→ {fee, margin, pnl} ─┬→ {position, order, settle, risk, view}
                                          │
internal/decimalx ────────────────────────┘   取整与舍入口径，被 fee / margin / pnl
                                              三个纯函数包共同依赖

account ─→ {settle, risk, view}
order   ─→ match
futsim  ─→ 以上全部
```

`internal/decimalx` 在图里有位置不是形式：**待实测 5（手续费的取整口径）落在它身上**，
而 `fee` / `margin` / `pnl` 三个包都要用同一套舍入规则。三处各写各的，
就会出现「同一笔钱在两个包里差一分」这种谁都不报错的分歧。

**硬性约束：主模块的依赖树只有 `github.com/shopspring/decimal` 一个。**

`refdata/live` 引入 `net/http`，故独立成子包；`cmd/oracle` 需要 WebSocket 客户端
与 CTP 绑定（后者自带数十 MB 的二进制），故做成**嵌套模块**（自带 `go.mod`）——
若写进主模块，即便使用者从不引用该工具，依赖仍会出现在他们的模块图里。

⚠️ **判别实验的执行器 `probe` 必须和 `conformance` 一起待在这个嵌套模块里。**
它要连天勤 WebSocket 与 SimNow CTP，放进主模块会当场破掉上面那条硬约束——
而**破掉的方式是使用者 `go get` 之后才在自己的模块图里看见一个 WebSocket 依赖，
本仓库这边没有任何报错**。两者合成一个模块的两个子命令，还顺带共用凭据读取、
传输层与夹具目录，这三样本来就重叠。

`cmd/refdata-sync` 留在主模块：它只用 stdlib 与本库，不引入任何第三方依赖。

代价是根目录的 `go build ./...` 不含 `cmd/oracle`，需单独进入执行。
CI 要分别对两个模块跑，漏掉第二个就等于对拍工具永远没被编译过。

> **包边界在 v1.0.0 之前可以调整。** 若某个边界在实现中被证明会逼出别扭的类型
> （典型症状：为了跨包传参而定义一堆只用一次的结构体），就合并它，并在
> [roadmap.md](./roadmap.md) 里记一条变更。**结构是为了让规则各安其位，不是为了好看。**

---

## 4. API 形态：双层时钟

这是与参照项目最大的架构差异，也是本库存在的理由。

```
盘中     Advance(bar)          撮合 → 更新持仓与冻结 → 浮动盈亏随最新价变
                               ↓  不结算、不动昨结算价基线、不滚今昨仓
日终     Settle(settlement)    逐日盯市 → 兑现盈亏 → 重算保证金 → 今仓转昨仓 → 出结算单
```

⚠️ **20260917**：本节与 §6.5「`Advance` 的入参」写在门面**之前**。门面长出来以后，`Advance` 在它上面新增什么、
三个决策点与分期，以「门面的形状」§13（F9）为准；本节的「双层时钟」与「两个动词不能合并」仍然成立。

`Advance` 与 `Settle` 是**两个不同的动词**，不能合并成一个「推进一步」。理由：

1. **结算价不是行情**。它是交易所的结算结果，各所规则不同、含无成交时的特殊处理，
   无法从 K 线推出。它必须由调用方在结算时显式给出
2. **一个交易日可以横跨三个自然日**（周五夜盘 → 周六凌晨 → 周一日盘），
   中间**没有结算**。按自然日自动结算会凭空多结算两次
3. **结算是唯一把「今仓」变成「昨仓」的地方**。它漏掉或重复执行，后续每一天的
   平今/平昨判定、手续费、保证金基线全部错位，而且**不会报错**

`Settle` 一步之内的顺序是确定的，且每一步依赖前一步（cn-futures-rules.md §8）：

```
持仓盈亏 → 汇总平仓盈亏与手续费 → 结出今结存 → 按今结算价重算保证金
        → 判风险度 → 今仓转昨仓、基线推进 → 生成结算单
```

### 两条并存的路径

内置撮合不是强制的。引擎若有自己的撮合逻辑，可直接灌成交。内置撮合必然要做假设
（K 线驱动、无盘口深度、全成或不成），有自己撮合逻辑的引擎不该被这些假设绑架。

内置撮合最实在的价值是**涨跌停与最小变动价位的校验**，以及**报单冻结的自动管理**
——这两项手工做最容易漏，而漏了会让回测比真实账户「有钱」。

### 状态即纯数据

持仓（含逐笔明细）、账户、挂单不含任何隐藏状态，`State()` 整体导出、`Restore()` 原样放回。
参数扫描的断点续跑、事件重放、把中途状态存下来事后复盘都要它。

⚠️ **`Restore` 在交易日、规则数据版本或合约集不匹配时直接报错**，而不是将就着跑。
中国期货的状态里藏着一个隐式基线（昨结算价与今昨仓划分），跨交易日恢复一个
不匹配的状态，账面上完全正常，算出来的每一个数都是错的。

---

## 5. 验收标准：双口子对拍

「100% 模拟」若不能被自动验证就只是一句口号。本项目的验证有**两个真值来源**，
角色不同，缺一不可。

### 口子一：天勤快期模拟（主对拍）

[已验证](./probes.md)：DIFF 协议是 **JSON over WebSocket + JSON Merge Patch（RFC 7386）**，
业务截面里有：

| 截面 | 字段 |
|---|---|
| `trade/{user}/accounts/CNY` | **23 个**（DIFF 文档只列了 18，实测多 5 个，见 [probes.md](./probes.md) §6.2）：`pre_balance` `static_balance` `balance` `available` `margin` `frozen_margin` `frozen_commission` `commission` `close_profit` `position_profit` `float_profit` `risk_ratio` … |
| `trade/{user}/positions/{symbol}` | 28 个：`volume_long_today/his` `volume_short_today/his` 及各自的冻结、`open_price_*` `open_cost_*` `position_price_*` `position_cost_*` `float_profit_*` `position_profit_*` `margin_*` `order_volume_*` |
| `orders` / `trades` | 报单与成交明细 |
| `quotes/{symbol}` | `volume_multiple` `price_tick` `margin` `commission` `upper_limit` `lower_limit` `pre_settlement` `settlement` … |

**为什么它是主口子：**

- **纯 Go 可达**：JSON + WebSocket，不需要 cgo、不需要 C++ SDK、不需要 DLL
- **零门槛**：使用者已有免费的快期账户
- **7×24 可用**：非交易时段也能连上做查询类对拍
- **`float_profit` 与 `position_profit` 并列、`open_cost` 与 `position_cost` 并列**
  ——它原生就分开了「按开仓价」与「按昨结算价」两个基线，正是本库最核心的那条区分

### 口子二：SimNow（CTP，权威裁决）

CTP 是国内柜台的事实标准，SimNow 就是 CTP 本身。它比天勤更权威，但门槛更高
（需注册、Windows 走 `syscall` 加载 DLL、Linux 要 cgo）。

**它负责天勤给不了的三件事：**

1. **两套盈亏口径的分别验证**。天勤只给一个 `close_profit`；CTP 给
   `CloseProfitByDate` **与** `CloseProfitByTrade`，两个都要对上
2. **完整的规则数据真值**。天勤的 `quotes.{symbol}.margin` / `.commission` 是
   「每手保证金 / 每手手续费」的**单一数值**；CTP 给的是 **6 个手续费率**
   （开/平昨/平今 × 按金额/按手数）与 **4 个保证金率**（多/空 × 按金额/按手数），
   外加 `PositionDateType`、`MaxMarginSideAlgorithm` 这两个决定今昨仓与大边的开关。
   ⚠️ **用天勤的单一数值做规则数据，会丢掉「平今费率」这个维度**——日内策略的
   手续费几乎全落在那上面
3. **裁决天勤与真实柜台的口径差**。快期模拟是天勤自己的模拟撮合与结算，
   不是 CTP 柜台。⚠️ **它与真实柜台可能存在差异，而这个差异本身也需要被测量**，
   否则「与快期模拟一致」会被误读成「与真实账户一致」

⚠️⚠️ **长期规则（使用者 2026-09-16 夜定）：快期与 CTP 在同一件事上实测相反时，本库一律跟 CTP，不再逐条上报裁决。**
原话「跟CTP。后续此类问题也都已CTP为准」，是在裁决 §13 #22 时说的（此前 #20、#22 都是逐条上报）。
⚠️ 出处：**双方各自确认**：实现方会话里使用者说了原话；评审方随后在评审会话里用 AskUserQuestion 向使用者本人核了两件事 —— #22 是不是使用者裁决（「是，我裁决的」）与长期规则的**窄读法范围**（「对，就这么窄」）（20260916 夜）。⇒ 下面的 ✅ / ❌ 范围是**使用者确认过的**，不是实现方的推断。

- ✅ 管的是：**同一件事两边都有实测、且答案相反** ⇒ 照 CTP 实现，§13 写明「按长期规则跟 CTP」，快期那一侧的观测**保留并声明**为口径差
- ❌ 不管：只有快期有观测而 CTP 没测过（那是缺证据，去测）；**范围外推**（如 §13 #21 只在大商所有观测，推不推到郑商所 / 广期所 —— 另一种问题，另行裁决）；
  CTP 只有一个样本、分歧可能出在样本上（先补实测）

### 验收标准本身

沿用参照项目已被证明有效的一条：

> **逐字段比对，每个字段落进三档之一：**
>
> 1. **值对得上，且本次样本内被触发过** —— 唯一算通过的一档
> 2. **已声明不建模，且带到期版本**（`NotModeledUntil`）
> 3. **值对得上，但本次样本内从未被触发** —— ⚠️ **单独计数，不算通过**
>
> 三档之外的，判失败。

#### ⚠️ 第四档：已知的**口子差异**，而它必须被卡死

三档放不下一种情形，而那种情形已经真实出现了：

> 按额手续费的基准价，本库按 CTP 建模（**成交价**），
> 而快期模拟实测用的是**昨结算价**（[probes.md](./probes.md) §7）。
> 两边各自都对，值却不同 —— 按三档只能判**失败**。

所以有第四档：**已知口子差异**。但它是这套判据里唯一可能被滥用的一档 ——
「对不上就说它是口子差异」会让整套验收失效。所以它有三条硬门槛，缺一不成立：

1. **必须给出处**：哪一份夹具、哪一次实测量到的这个差异
2. **必须指名裁决者**：`state.md` 的 `simnow_pending` 里对应的那一条
3. **必须写明本库选了哪一边**，以及选它的理由

⚠️ 没有这三样的「差异」一律判失败。**一个可以随口声明的豁免档，
比没有这一档更坏**——它会把真实的不一致洗成「已知」。

第三档是后加的，堵的是**零值假通过**：字段两边都是零、本次样本从未触发它，
对拍照样判「一致」。本仓库第一次连上快期模拟就撞见了它的样子——空仓时
`balance == ctp_balance == 1000000.0`，看起来两个口径完全一致，实际什么都没证明。
展开见 [fidelity.md](./fidelity.md) §2。

挑着比永远发现不了「有个字段我压根没建模」。参照项目在补上全字段对拍的当天
就暴露出 35 处差异与 4 个从未建模的字段。

另加一条中国期货特有的、也是最有力的：

> **结算单对拍。** 结算单是逐日盯市的最终陈述，期初结存、当日盈亏、手续费、
> 保证金占用、风险度全在一张表上互相勾稽。它对上了，说明整条日终链路是对的。

⚠️ **对拍测试零次迭代也必须判失败。** 夹具被清空或字段改名导致反序列化成空，
循环一次不执行，测试照样绿——而一条真实数据都没核对。这是参照项目踩过的坑，
本项目从第一个对拍测试就带上迭代次数断言。

---

## 6. 明确排除在 v1.0 之外

| 项 | 理由 |
|---|---|
| 期权 | 保证金模型完全不同（权利金、Delta、组合保证金），值得单独一个大版本 |
| 组合保证金 / 套利组合 | 需要交易所的组合定义与审批数据，回测拿不到 |
| 套保与套利持仓的保证金优惠 | 需要交易所的套保额度审批数据。**但字段从第一天就带上** |
| 多币种账户 | 国内绝大多数账户只有 CNY。字段留位，逻辑不做 |
| 实物交割的仓单流转 | 与持仓核算关系不大，且个人客户不进交割月 |
| 连续涨跌停的扩板与临时保证金上调 | 是交易所的临时公告，非公式。可由使用者通过规则数据注入 |
| 强平的执行 | 见 ADR 11 |

---

## 6.5 与上游数据层的接口约定

上游是 `futures-tickflow-go`（行情 → 带指标的可步进多周期视图）。两库是回测引擎的
两个上游：它给视图，本库收成交与结算。**两边互不 import** —— 它要
`coder/websocket`，本库主模块的依赖树硬约束是只有 `shopspring/decimal`。
转换发生在它那侧的 `adapter/` 独立嵌套模块里。

### ⚠️ 枚举取值不绑定线格式，因为线格式有两套

`types` 最初定为「取值与 CTP 线格式一致」。**这条改了**，理由是本库实际对接两个口子，
而它们的线格式并不相同：

| | 买卖方向 | 开平标志 |
|---|---|---|
| CTP | `'0'` / `'1'`（单字符） | `'0'` `'1'` `'3'` `'4'` … |
| DIFF | `BUY` / `SELL` | `OPEN` / `CLOSE` / `CLOSETODAY` |

⚠️ 挑其中一套做内部取值，等于**默默偏袒一个口子**，另一个口子的转换就会散落在
调用处——而散落的转换是本项目一直在防的那类：它们不在一个地方，
所以没有任何一处能被完整地检查。

因此 `types` 的取值**规范化**（Go 惯用命名），两套映射在包内显式成对给出，
并配**往返测试**：`FromCTP(x.CTP()) == x`、`FromDIFF(x.DIFF()) == x`，
且两个方向都要有**未知取值必须报错**的守卫——
⚠️ 解析失败时返回零值会让「没见过的开平标志」变成「开仓」。

### 合约键用交易所线格式

`futsim` 的合约键取 **CTP / 交易所线格式**（郑商所三位年月：`CZCE.SA701`），
理由是 `view` 包必须与 CTP 结构体、DIFF 业务截面**字段级同构**，
本库 `testdata/probes/` 里的 `instrument_id` 已经是这个形态。

⚠️ **但规则数据不能只按线格式做键。** 郑商所三位年月十年一轮回：
`TA701` 既是 2027 年 1 月也是 2037 年 1 月。

| 用途 | 按线格式做键 | 为什么 |
|---|---|---|
| 持仓 / 委托 / 成交 | ✅ 安全 | 相隔十年的两个合约**不可能同时持有**，键不会撞 |
| `refdata`（合约规格 / 费率 / 保证金率 / 档位） | ⚠️ **不安全** | 一份覆盖十年以上的快照里，`TA701` 会撞 |

`refdata` 的键必须是**四位规范形式**，或 `(线格式, 生效区间)` 二元组。
这一条只在跨十年的历史回测上才显形，**十年以内的样本上两种做法给出同一个数**
—— 又一个「在最常见的样本上，错误答案等于正确答案」。

### `Advance` 的入参：本库只要五样

| 字段 | 要不要 | 用途 |
|---|---|---|
| `TradingDay` | ✅ **必须** | 结算触发、今昨仓划分。见下方守卫 |
| `Ts` / `TsEnd` | ✅ | 时序与事件时间戳 |
| `High` / `Low` | ✅ | 触发判定（涨跌停、条件单）。⚠️ 一根 K 线只给区间不给路径，触发顺序是**约定**不是还原 |
| `Close` | ✅ | 浮动盈亏与盘中风险度 |
| `Volume` `Turnover` `OpenInterest` | ❌ **不要** | 本库不建模盘口深度，持仓量与本账户核算无关 |

**涨跌停价本库自己算**：`昨结算价 × (1 ± 涨跌幅比例)`，按最小变动价位取整，
比例来自 `refdata`。`Bar` 上可选带一个覆盖值，有则优先。
⚠️ 两者都没有时，涨跌停校验**跳过并给出原因**，不拿别的价推算。

### `TradingDay` 的类型

```go
type TradingDay int32   // yyyymmdd，如 20260907
```

可比较、可排序、8 字节记录里省位，且**是一个具名类型**——防的是它和自然日混用。
`view` 包序列化成字符串（`"20260907"`），那是 CTP / DIFF 的线格式，不是内部表示。

### 交易日历与时段：数据结构，不是算法

`refdata` 里这一块的全部设计意图是一句话：**把「交易日 ≠ 自然日」变成一次查表，
而不是一次推算。**

```go
type ClockTime  int32          // 一天内的墙钟秒数，0 ≤ t < 86400
type Session    struct{ Start, End ClockTime; CrossesMidnight bool }
type SessionTable struct{ Exchange types.Exchange; Product string; Day, Night []Session }

type Calendar struct{ /* 交易日列表（升序）+ 各品种时段表 */ }

func (c *Calendar) TradingDayAt(t time.Time, ex types.Exchange, product string) (types.TradingDay, error)
```

核心操作只有 `TradingDayAt`：给一个墙钟时刻和一个品种，答它属于哪个交易日。

判定规则，两条：

	落在**日盘**时段  → 交易日 = 该自然日（且该自然日必须在交易日列表里）
	落在**夜盘**时段  → 交易日 = 该自然日**之后的下一个交易日**

⚠️ 「下一个交易日」**查列表得到，不由「下一个工作日」推**。
长假前后这两者会分岔，而分岔的那几天恰好是保证金上调、风险最高的时候。

⚠️ **落在任何时段之外时报错，不猜。** 收盘后到夜盘开盘之间的时刻不属于任何交易日，
这是一个事实，不是一个需要填充的空缺。返回「最近的那个交易日」看起来友好，
但它会让「引擎在非交易时段推进了一根 K 线」这件事**没有任何动静**。

#### 时区用固定 +08:00，不用 `time.LoadLocation`

中国自 1991 年起不再实行夏令时，此后所有交易日的偏移恒为 +08:00。
用固定偏移而非 IANA 时区，换来两件事：

- **不引入 `time/tzdata`**（那是 stdlib，但会给二进制多带约 450 KB 的时区库），
  也不依赖宿主机装了系统时区数据库——Windows 上默认没有
- 行为**不随宿主机环境变化**：`time.Local` 在不同机器上是不同的东西，
  而一个「在开发机上对、在服务器上错」的日期换算，正是本项目在防的那类静默失败

⚠️ 代价写在这里：**1991 年之前的时刻会算错**（当时有夏令时）。
中国期货市场的电子交易数据不覆盖那段时期，所以这个代价是零；
但它是一个**已知边界**，不是一个疏忽。

### 快照持久化：两处会静默出错的地方

#### 小数一律序列化成**字符串**

费率经过一次 `float64` 往返就不再是原来的数：`0.07` 变成 `0.07000000000000001`。
它不会报错，只会让每一笔保证金差一个极小的量，而那个量**逐日累加进结存**。

⚠️ 本项目已经量到过这个形状：夹具里存着 `commission: 126.95459999999999`，
而恒等式核对因此只能写成「在 float64 表示误差内成立」，不能写成「精确成立」。
**持久化是本库自己能控制的那一段，没有理由在这里再引入一次。**

#### 加载路径必须走**同一个** `Builder`

快照文件是可以手改的。若 `Load` 直接把字段塞进 `Snapshot`，
`Builder` 在构造期的全部校验——键的规范形式、费率非负、孤儿费率、缺失字段——
**在加载路径上就全部失效**。

⚠️ 这与「两个实现一起退化」是同一个形状，只是两条路径一条是构造、一条是加载。
所以 `Load` 反序列化之后**必须重新走一遍 `Builder`**，
而不是走一条「反正文件是我们自己写的」的快路径。

#### 快照要自述来源与证据等级

```json
{ "version": ..., "evidence": "measured-kq | measured-ctp | documented | placeholder", ... }
```

⚠️ **`placeholder` 这一档存在的理由**：一份内置快照如果是空的但能加载成功，
调用方拿到的会是「查不到这个合约」，而不是「本库还没有参考数据」。
两者在代码里长得一样，在排查时差得很远。
所以 `Load` **拒绝** `placeholder`，除非调用方显式声明它知道自己在做什么。

⚠️ 而 `measured-kq` 这一档同样不许被读成「规则如此」：
快期模拟已经量到至少一条**已知偏离真实 CTP** 的取值（按额手续费的基准价）。
证据等级跟着数据走，是为了让它在**离开产生它的那次实测之后**仍然带着限定。

#### 时段表与交易日列表都是**数据**，而且证据等级不同

	交易日列表    来自交易所公告 / 上游数据层    —— 本库不内置推算
	时段表        目前是**文档**级              —— 只有 rb / m 的夜盘时段被间接实测过

⚠️ 内置快照里的时段表必须标出证据等级，且 `Calendar` 必须允许整体替换。
「螺纹夜盘 21:00–23:00」这类值在交易所调整时段时会变，
而**一个写死在代码里的时段表，改的时候不会有人想起它**。

### ⚠️ 结算触发：约定在引擎，**守卫在本库**

上游提议的引擎侧契约是对的：

```go
if bar.TradingDay != prev.TradingDay { sim.Settle(...) }   // 先结上一日，再 Advance
```

但**本库不能只依赖调用方守约**。`Advance` 收到一根 `TradingDay` 与当前不同、
而本交易日尚未 `Settle` 的 K 线时，**直接报错**。

理由：「忘了结算」是本项目静默风险清单的**第 1 条**——账永远是平的，
只是平今费率一直按平昨收、保证金基线停在开仓日，**全程没有任何动静**。
把它变成一个 `error`，是这条守卫存在的全部意义。

### float64 / decimal 的边界

上游实测（本库已复核）：

```
价格 float64 → decimal 往返：6 个品种 × 2000 刻度，逐位无损
金额乘法：沪金 512.34 × 1000 × 8
    float64 = 4098720.0000000005
    decimal = 4098720             差 5e-10
```

所以边界是：**上游只传价格与量，绝不预先算任何金额**；
`价格 × 乘数 × 手数` 这类乘法一律发生在 `futsim` 内部，用 decimal。

### ⚠️ 结算价可以由上游供，但必须区分「零」与「无此值」

上游能从公开日线提供**当日结算价**（不是交割结算价，后者仍归本库不建模）。
本库复核了那份数据：

> `RB0` 4237 根，2009-03-27 至 2026-09-04。**其中 326 根（7.7%）的结算价字段是 `0`，
> 而且不集中在开头** —— 一直散到 2024-09-25。

⚠️ **按品种复查后，情况比 7.7% 严重得多**（本库 2026-09-07 实测，与上游数据层的
复查逐行吻合）：

| 主连 | 总根数 | `s == 0` | 占比 | 2020 年后仍为零 |
|---|---|---|---|---|
| `AG0` 沪银 | 3487 | 6 | 0.2% | 3 |
| `RB0` 螺纹 | 4237 | 326 | 7.7% | 3 |
| `TA0` PTA | 4791 | 922 | 19.2% | 0 |
| `CU0` 沪铜 | 5273 | 1364 | 25.9% | 3 |
| `M0` 豆粕 | 5276 | 1589 | 30.1% | 108（2020 年 106） |
| **`T0` 国债** | 2339 | 2060 | **88.1%** | **1509，逐年 242~243 直到 2026** |
| **`IF0`/`IH0` 股指** | 2339 | 2307 | **98.6%** | **1619，逐年 242~243 直到 2026** |

⚠️ **中金所品种（股指 IF/IH/IC、国债 T/TF/TS）在这个源上基本没有结算价，
而且不是历史遗留** —— 2020 到 2026 每年都缺满。商品品种主要缺在早期。

### 但结算价本身是有的，缺的是那个源

本库复查了中金所**自己的**日行情（`probes.md` §2 已验证可达）：

```
http://www.cffex.com.cn/sj/hqsj/rtj/202609/04/index.xml
IC2609 → presettlementprice 7720.4   settlementprice 7604.8
纯期货合约 28/28 全部有 settlementprice，且全部非零
```

所以正确的说法不是「股指回测拿不到结算价」，而是
**「别从那个源拿，从中金所官网拿」**。

⚠️ 这一整段的教训值得单记：**上游主动推荐一个数据源时，
「字段存在」和「值可用」是两件事，而推荐方往往只验了前者。**
它与该上游自己提醒本库的另一条情报（某实时接口冻结两年、
HTTP 200、字段齐全、数值合理、内容是两年前那一帧）**完全同族**——
结构完整、数值合法、内容是错的。

⚠️ **字段永远存在，值有 7.7% 是零。** 拿它直接喂 `Settle`，逐日盯市会用
结算价 0 算出一整天的灾难性盈亏，而**全程不报错**——因为字段确实在那儿。

这正是本库反复写的那条：**零值不是安全的默认，「没有」和「是零」必须分开。**

因此接口约定：**上游把 `s == 0` 映射成 `NaN`（它对分钟线的缺失已经这么做了），
不要映射成 `0.0`。** 本库的 `Settle` 在收到 NaN 或缺失时报错，收到 0 时也报错
——⚠️ 因为**没有任何品种的结算价会是零**，0 只可能是缺失的伪装。

并且：**结算价的可用性按 `(品种, 区间)` 报，不给全局承诺。**
一句「17 年全有」会让使用者在股指上直接撞墙，而撞的时候数据看起来完全正常。

顺带一条复核结果：这 4237 根里 **97.7% 的结算价 ≠ 收盘价**，
再次印证「结算价无法从 K 线推出」。

---

## 7. 外部依赖

⚠️ **本节不复述状态。** 柜台通路、账号、以及各项实测进展一律以
[probes.md](./probes.md) 为唯一来源——这里放一条链接，链接不会过期，因为它不含事实。

凭据在 `.env`（已在 `.gitignore`，不入库），模板见 [`.env.example`](../.env.example)。

⚠️ **「能连上柜台」与「规则已实测」是两件事**，进度上极易混为一谈。
后者以 [cn-futures-rules.md](./cn-futures-rules.md) §13 的判别实验为准（条数见 [state.md](./state.md)），
且「实测」必须带**来源**与**性质**两个坐标，见该文件表头。
